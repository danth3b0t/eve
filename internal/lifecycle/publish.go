package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"

	"eve/internal/domain"
	"eve/internal/envfile"
	"eve/internal/files"
	"eve/internal/git"
	"eve/internal/ports"
	"eve/internal/private"
	"eve/internal/resolve"
	"eve/internal/state"
)

// PublishFiles completes a LOCAL-ONLY initial generation, not runtime health.
// The caller must have approved the manifest/native launch contract and retain
// this store's workspace lock. No launcher or repository setup command is run.
func PublishFiles(ctx context.Context, s *state.Store, g *git.Client, w *state.LockedWorkspace) (state.Workspace, error) {
	p, err := w.Publication(ctx)
	if err != nil {
		return state.Workspace{}, err
	}
	keyID, key, err := s.HMACKey(ctx)
	if err != nil {
		return state.Workspace{}, err
	}
	if keyID != p.Intent.KeyID {
		return state.Workspace{}, &domain.Error{Code: "E_HMAC_KEY", Message: "publication key differs from its recorded intent"}
	}
	if p.Workspace.State == "prepared" {
		if err := purgeImages(ctx, s, w, p, key); err != nil {
			return p.Workspace, err
		}
		return s.Workspace(ctx, p.Workspace.ID)
	}
	r, err := s.Repository(ctx, p.Workspace.RepositoryID)
	if err != nil {
		return state.Workspace{}, err
	}
	lock, err := s.LockRepository(r.ID)
	if err != nil {
		return state.Workspace{}, err
	}
	defer lock.Close()
	if _, err := registeredSource(ctx, s, g, r); err != nil {
		return state.Workspace{}, err
	}
	raw, err := g.Manifest(ctx, r.SourcePath, p.Workspace.HeadOID)
	if err != nil {
		return state.Workspace{}, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != p.Workspace.ManifestSHA256 {
		return state.Workspace{}, &domain.Error{Code: "E_STATE_INTENT", Message: "publication manifest differs from its committed snapshot"}
	}
	checkout, err := g.Verify(ctx, p.Identity)
	if err != nil {
		return state.Workspace{}, err
	}
	if err := outsideState(s, checkout); err != nil {
		return state.Workspace{}, err
	}
	allocation, err := s.Allocation(ctx, p.Workspace.ID)
	if err != nil {
		return state.Workspace{}, err
	}
	images, owners, err := publicationImages(ctx, s, w, p, key, allocation)
	if err != nil {
		return state.Workspace{}, err
	}
	pub, err := files.OpenPublisher(ctx, g, p.Identity, p.Workspace.Branch, p.Workspace.HeadOID, &p.Workspace.Manifest, images)
	if err != nil {
		return state.Workspace{}, err
	}
	defer pub.Close()
	if err := ports.CheckEndpoints(ctx, allocation, nil); err != nil {
		return state.Workspace{}, err
	}
	if !p.Started {
		if err := w.StartPublication(ctx); err != nil {
			return state.Workspace{}, err
		}
	}
	for _, f := range p.Files {
		if err := pub.Publish(ctx, f.Path, func(receipt domain.FileIdentity) error { return w.RecordTemporary(ctx, f.Path, receipt) }); err != nil {
			return state.Workspace{}, err
		}
		if f.State != "succeeded" {
			if err := w.RecordPublished(ctx, f.Path); err != nil {
				return state.Workspace{}, err
			}
		}
	}
	if err := ports.CheckEndpoints(ctx, allocation, nil); err != nil {
		return state.Workspace{}, err
	}
	observed, err := pub.Check(ctx)
	if err != nil {
		return state.Workspace{}, err
	}
	for _, f := range p.Files {
		if !observed[f.Path] {
			return state.Workspace{}, &domain.Error{Code: "E_PUBLICATION_RECONCILE", Message: "not every destination has its exact published image"}
		}
	}
	if err := w.CompletePublication(ctx, owners); err != nil {
		return state.Workspace{}, err
	}
	p, err = w.Publication(ctx)
	if err != nil {
		return state.Workspace{}, err
	}
	if err := purgeImages(ctx, s, w, p, key); err != nil {
		return p.Workspace, err
	}
	return s.Workspace(ctx, p.Workspace.ID)
}

func publicationImages(ctx context.Context, s *state.Store, w *state.LockedWorkspace, p state.Publication, key *private.Key, a state.Allocation) ([]files.PublicationFile, map[string]map[string][]string, error) {
	if !a.Ready {
		return nil, nil, &domain.Error{Code: "E_PUBLICATION_PLAN", Message: "accepted allocation is required"}
	}
	resources, err := w.Resources(ctx)
	if err != nil {
		return nil, nil, err
	}
	input := localInputs(p.Workspace, a)
	input.Resources = map[string]resolve.ResourceOutputs{}
	for _, r := range resources {
		outputs, ok := r.Outputs["cloud_url"]
		site := r.Outputs["site_url"]
		name := r.Outputs["name"]
		if !ok || site == "" || name == "" || r.State != "configured" || r.CredentialID == "" {
			return nil, nil, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "resource is not fully provisioned or configured"}
		}
		input.Resources[r.ResourceKey] = resolve.ResourceOutputs{URL: outputs, SiteURL: site, Deployment: r.Outputs["deployment"], Name: name, Reference: r.RemoteReference}
	}
	resolved, err := resolve.Resolve(&p.Workspace.Manifest, input)
	if err != nil {
		return nil, nil, err
	}
	expected := map[string]resolve.File{}
	owners := map[string]map[string][]string{}
	for _, f := range resolved.Files {
		expected[f.Path] = f
		owners[f.Path] = f.Owners
	}
	for _, r := range resources {
		secret, err := w.DeployKeyCredential(ctx, r)
		if err != nil {
			return nil, nil, err
		}
		path := state.ResourceEnvFile(r)
		file := expected[path]
		if file.Values == nil {
			file = resolve.File{Path: path, Values: map[string]string{}, Owners: map[string][]string{}}
		}
		if file.Owners == nil {
			file.Owners = map[string][]string{}
		}
		if owners[path] == nil {
			owners[path] = file.Owners
		}
		for name, value := range map[string]string{"CONVEX_DEPLOYMENT": "dev:" + r.RemoteName, "CONVEX_DEPLOY_KEY": secret} {
			file.Values[name] = value
			file.Owners[name] = []string{"resources." + r.ResourceKey}
			owners[path][name] = file.Owners[name]
		}
		expected[path] = file
	}
	objects, err := s.PendingObjects()
	if err != nil {
		return nil, nil, err
	}
	defer objects.Close()
	var images []files.PublicationFile
	for _, f := range p.Files {
		data, err := readImage(ctx, objects, key, p.Workspace.ID, f.Path, f.StagedRef, f.StagedHMAC, f.Size)
		if err != nil {
			return nil, nil, err
		}
		image := files.Image{Path: f.Path, Mode: os.FileMode(f.Mode), Tracked: f.Tracked, Data: data, PreimageIdentity: f.Preimage}
		if f.Preimage != nil {
			image.Preimage, err = readImage(ctx, objects, key, p.Workspace.ID, f.Path, f.PreimageRef, f.PreimageHMAC, f.Preimage.Size)
			if err != nil {
				return nil, nil, err
			}
		}
		desired, native := expected[f.Path]
		if len(f.Values) != len(desired.Values) {
			return nil, nil, &domain.Error{Code: "E_FILE_INTENT", Message: "managed key set differs from frozen resolution", Path: f.Path}
		}
		if native {
			doc, err := envfile.Parse(data)
			if err != nil {
				return nil, nil, err
			}
			for name, value := range desired.Values {
				actual, err := doc.GeneratedValue(name)
				if err != nil {
					return nil, nil, err
				}
				if actual != value || !private.Equal(key.Value(p.Workspace.ID, f.Path, name, actual), f.Values[name]) {
					return nil, nil, &domain.Error{Code: "E_STAGE_CHANGED", Message: "staged managed value differs from its frozen intent", Path: f.Path}
				}
			}
			delete(expected, f.Path)
		}
		images = append(images, files.PublicationFile{Image: image, Temp: files.TemporaryPath(f.Path, f.StagedRef), Receipt: f.Receipt, Published: f.State == "succeeded"})
	}
	if len(expected) != 0 {
		return nil, nil, &domain.Error{Code: "E_FILE_INTENT", Message: "a native destination is missing from staging"}
	}
	return images, owners, nil
}
func purgeImages(ctx context.Context, s *state.Store, w *state.LockedWorkspace, p state.Publication, key *private.Key) error {
	if p.Workspace.State != "prepared" {
		return &domain.Error{Code: "E_PUBLICATION_STATE", Message: "retain images until publication completes"}
	}
	objects, err := s.PendingObjects()
	if err != nil {
		return err
	}
	defer objects.Close()
	for _, f := range p.Files {
		if err := objects.RemoveVerified(ctx, f.StagedRef, f.Size, func(data []byte) bool { return private.Equal(key.File(p.Workspace.ID, f.Path, data), f.StagedHMAC) }); err != nil {
			return err
		}
		if f.Preimage != nil {
			if err := objects.RemoveVerified(ctx, f.PreimageRef, f.Preimage.Size, func(data []byte) bool { return private.Equal(key.File(p.Workspace.ID, f.Path, data), f.PreimageHMAC) }); err != nil {
				return err
			}
		}
		if err := w.RecordImagesPurged(ctx, f.Path); err != nil {
			return err
		}
	}
	return nil
}
