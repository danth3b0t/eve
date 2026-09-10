package lifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/envfile"
	"eve/internal/files"
	"eve/internal/git"
	"eve/internal/ports"
	"eve/internal/private"
	"eve/internal/provider/convex"
	"eve/internal/resolve"
	"eve/internal/state"
)

type SyncOptions struct {
	OverwriteManaged bool
	ProviderFactory  convexFactory
}
type SyncResult struct {
	Workspace       state.Workspace
	RestartRequired bool
}
type syncPlan struct {
	HeadOID, ManifestSHA256 string
	Manifest                *config.Manifest
	Owners                  map[string]map[string][]string
	Images                  []files.Image
	Unchanged               bool
}

func (o SyncOptions) factory() convexFactory {
	if o.ProviderFactory != nil {
		return o.ProviderFactory
	}
	return defaultConvexFactory
}

func SyncWorkspace(ctx context.Context, s *state.Store, g *git.Client, workspaceID string, options SyncOptions) (SyncResult, error) {
	lock, err := s.LockWorkspace(workspaceID)
	if err != nil {
		return SyncResult{}, err
	}
	defer lock.Close()
	step, err := lock.SyncStep(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	if step.State == "complete" {
		if _, err := completeSyncCleanup(ctx, s, lock, step); err != nil {
			return SyncResult{}, err
		}
		step, err = lock.SyncStep(ctx)
		if err != nil {
			return SyncResult{}, err
		}
	}
	switch step.State {
	case "pending":
		plan, err := prepareSyncPlan(ctx, s, g, lock, step, options)
		if err != nil {
			return SyncResult{}, err
		}
		if plan.Unchanged {
			return SyncResult{Workspace: step.Workspace, RestartRequired: false}, nil
		}
		if err := stageSyncPlan(ctx, s, lock, step.Workspace.ID, plan); err != nil {
			return SyncResult{}, err
		}
		step, err = lock.SyncStep(ctx)
		if err != nil {
			return SyncResult{}, err
		}
		return finishSync(ctx, s, g, lock, step)
	case "images_inflight":
		if err := resumeSyncImages(ctx, s, lock, step); err != nil {
			return SyncResult{}, err
		}
		step, err = lock.SyncStep(ctx)
		if err != nil {
			return SyncResult{}, err
		}
		return finishSync(ctx, s, g, lock, step)
	case "staged", "publish_inflight":
		return finishSync(ctx, s, g, lock, step)
	default:
		return SyncResult{}, &domain.Error{Code: "E_SYNC_STATE", Message: "unrecognized sync journal state"}
	}
}

func prepareSyncPlan(ctx context.Context, s *state.Store, g *git.Client, lock *state.LockedWorkspace, step state.SyncStep, options SyncOptions) (syncPlan, error) {
	checkout, err := g.Verify(ctx, step.Identity)
	if err != nil {
		return syncPlan{}, err
	}
	if checkout.Branch != step.Workspace.Branch {
		return syncPlan{}, &domain.Error{Code: "E_GIT_IDENTITY", Message: "workspace branch changed before sync"}
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return syncPlan{}, err
	}
	allocation, err := s.Allocation(ctx, step.Workspace.ID)
	if err != nil {
		return syncPlan{}, err
	}
	raw, err := g.Manifest(ctx, step.Identity.Path, checkout.HeadOID)
	if err != nil {
		return syncPlan{}, err
	}
	manifest, err := config.Parse(raw)
	if err != nil {
		return syncPlan{}, err
	}
	if err := syncTopology(&step.Workspace.Manifest, manifest); err != nil {
		if err := g.CheckPublication(ctx, step.Identity, step.Workspace.Branch, checkout.HeadOID); err != nil {
			return syncPlan{}, err
		}
		return syncPlan{}, err
	}
	if err := ports.CheckEndpoints(ctx, allocation, nil); err != nil {
		return syncPlan{}, err
	}
	resources, err := lock.Resources(ctx)
	if err != nil {
		return syncPlan{}, err
	}
	keyID, key, err := s.HMACKey(ctx)
	if err != nil {
		return syncPlan{}, err
	}
	model, err := syncImagesModel(ctx, s, lock, step, keyID, key, manifest, allocation, resources, options)
	if err != nil {
		return syncPlan{}, err
	}
	hash := sha256.Sum256(raw)
	unchanged := true
	for _, image := range model.Images {
		if !bytes.Equal(image.Data, image.Preimage) {
			unchanged = false
			break
		}
	}
	return syncPlan{HeadOID: checkout.HeadOID, ManifestSHA256: hex.EncodeToString(hash[:]), Manifest: manifest, Owners: model.Owners, Images: model.Images, Unchanged: unchanged}, nil
}

func syncTopology(old, next *config.Manifest) error {
	bad := func(reason string) error { return &domain.Error{Code: "E_CREATION_ONLY_CHANGE", Message: reason} }
	if old.Version != next.Version || old.Workspace.PortBlockSize != next.Workspace.PortBlockSize || !slices.Equal(old.Workspace.Copy, next.Workspace.Copy) {
		return bad("generation, copy set and port-block dimensions must not change during sync")
	}
	if !maps.EqualFunc(old.Services, next.Services, func(a, b config.Service) bool {
		return a.Path == b.Path && a.EnvFile == b.EnvFile && a.Host == b.Host && a.Scheme == b.Scheme && a.Port == b.Port && a.AllowTracked == b.AllowTracked && maps.Equal(a.Ports, b.Ports) && keysSubset(a.Env, b.Env)
	}) {
		return bad("service identities, destinations, listeners and existing environment keys must not move or disappear")
	}
	if !maps.EqualFunc(old.Resources, next.Resources, func(a, b config.Resource) bool {
		return a.Provider == b.Provider && a.Path == b.Path && a.Project == b.Project && a.EnvFile == b.EnvFile && a.CredentialProfile == b.CredentialProfile && a.TTL == b.TTL && a.Region == b.Region && keysSubset(a.Env, b.Env)
	}) {
		return bad("resource identities, project bindings, TTL and existing environment keys must not move or disappear")
	}
	return nil
}
func keysSubset(old, next map[string]string) bool {
	for key := range old {
		if _, ok := next[key]; !ok {
			return false
		}
	}
	return true
}

type syncModel struct {
	Images []files.Image
	Owners map[string]map[string][]string
}
type modelResources struct {
	Outputs     map[string]resolve.ResourceOutputs
	Secrets     map[string]map[string]string
	SecretOwner map[string]string
}
type currentDestination struct {
	Data             []byte
	PreimageIdentity *domain.FileIdentity
	Tracked          bool
}

func syncModelResources(ctx context.Context, s *state.Store, lock *state.LockedWorkspace, resources []state.Resource) (modelResources, error) {
	out := modelResources{Outputs: map[string]resolve.ResourceOutputs{}, Secrets: map[string]map[string]string{}, SecretOwner: map[string]string{}}
	for _, r := range resources {
		if r.State != "configured" || r.CredentialID == "" {
			return out, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "sync requires each recorded remote resource to remain configured"}
		}
		secret, err := lock.DeployKeyCredential(ctx, r)
		if err != nil {
			return out, err
		}
		outputs := resolve.ResourceOutputs{URL: r.Outputs["cloud_url"], SiteURL: r.Outputs["site_url"], Deployment: r.Outputs["deployment"], Name: r.Outputs["name"], Reference: r.RemoteReference}
		if outputs.URL == "" || outputs.SiteURL == "" || outputs.Name == "" {
			return out, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "recorded resource outputs are incomplete"}
		}
		out.Outputs[r.ResourceKey] = outputs
		out.Secrets[state.ResourceEnvFile(r)] = map[string]string{"CONVEX_DEPLOYMENT": outputs.Deployment, "CONVEX_DEPLOY_KEY": secret}
		out.SecretOwner[state.ResourceEnvFile(r)] = r.ResourceKey
	}
	return out, nil
}
func configureRemoteSync(ctx context.Context, s *state.Store, lock *state.LockedWorkspace, workspace state.Workspace, resources []state.Resource, resolved *resolve.Result, factory convexFactory) error {
	for _, r := range resources {
		_, token, err := managementCredential(ctx, s, r.Spec.Profile)
		if err != nil {
			return err
		}
		api, err := factory(token)
		if err != nil {
			return err
		}
		project, err := api.ValidateProject(ctx, r.Spec.Project)
		if err != nil {
			return err
		}
		observed, err := recordedDeployment(r)
		if err != nil {
			return err
		}
		intent := convex.Intent{ProjectID: project.ID, Reference: r.RemoteReference, Region: r.Spec.Region, StartMS: workspace.CreatedAtMS, ExpiresMS: r.IntendedExpiresAtMS}
		d, err := api.Inspect(ctx, intent, observed)
		if err != nil {
			return err
		}
		if d.URL != r.Outputs["cloud_url"] {
			return &domain.Error{Code: "E_PROVIDER_IDENTITY", Message: "recorded deployment URL no longer matches state"}
		}
		secret, err := lock.DeployKeyCredential(ctx, r)
		if err != nil {
			return err
		}
		if err := configureResourceEnv(ctx, api, d, secret, resolved.RemoteEnv[r.ResourceKey]); err != nil {
			return err
		}
	}
	return nil
}

// The caller must explicitly stop the ordinary launcher before SyncWorkspace.
// Current managed-value evidence is compared, never recovered from plaintext.
func syncImagesModel(ctx context.Context, s *state.Store, lock *state.LockedWorkspace, step state.SyncStep, keyID string, key *private.Key, manifest *config.Manifest, allocation state.Allocation, resources []state.Resource, options SyncOptions) (syncModel, error) {
	input := localInputs(step.Workspace, allocation)
	resourceLinks, err := syncModelResources(ctx, s, lock, resources)
	if err != nil {
		return syncModel{}, err
	}
	input.Resources = resourceLinks.Outputs
	resolved, err := resolve.Resolve(manifest, input)
	if err != nil {
		return syncModel{}, err
	}
	desired := map[string]map[string]string{}
	owners := map[string]map[string][]string{}
	for _, file := range resolved.Files {
		desired[file.Path] = maps.Clone(file.Values)
		owners[file.Path] = file.Owners
	}
	for path, secrets := range resourceLinks.Secrets {
		if desired[path] == nil {
			desired[path] = map[string]string{}
			owners[path] = map[string][]string{}
		}
		for name, value := range secrets {
			desired[path][name] = value
			owners[path][name] = []string{"resources." + resourceLinks.SecretOwner[path]}
		}
	}
	managed, err := lock.ManagedFiles(ctx)
	if err != nil {
		return syncModel{}, err
	}
	managedValues, err := lock.ManagedValues(ctx)
	if err != nil {
		return syncModel{}, err
	}
	current := map[string]currentDestination{}
	for _, file := range managed {
		data, identity, err := files.ReadDestination(ctx, step.Identity, file.Path)
		if err != nil {
			return syncModel{}, err
		}
		current[file.Path] = currentDestination{Data: data, PreimageIdentity: identity, Tracked: file.Tracked}
		if _, ok := desired[file.Path]; !ok {
			return syncModel{}, &domain.Error{Code: "E_CREATION_ONLY_CHANGE", Message: "existing managed destination cannot disappear"}
		}
	}
	for _, value := range managedValues {
		doc, err := envfile.Parse(current[value.Path].Data)
		if err != nil {
			return syncModel{}, err
		}
		actual, docErr := doc.GeneratedValue(value.EnvKey)
		if docErr != nil || !private.Equal(key.Value(step.Workspace.ID, value.Path, value.EnvKey, actual), value.HMAC) {
			if !options.OverwriteManaged {
				return syncModel{}, &domain.Error{Code: "E_MANAGED_VALUE_CHANGED", Message: "EVE-managed value was edited; review it or use --overwrite-managed", Path: value.Path}
			}
		}
	}
	if len(current) != len(desired) {
		return syncModel{}, &domain.Error{Code: "E_CREATION_ONLY_CHANGE", Message: "existing managed destinations cannot move or disappear"}
	}
	images := make([]files.Image, 0, len(current))
	for name, data := range desired {
		origin := current[name]
		doc, err := envfile.Parse(origin.Data)
		if err != nil {
			return syncModel{}, err
		}
		image, err := doc.Apply(data)
		if err != nil {
			return syncModel{}, err
		}
		images = append(images, files.Image{Path: name, Mode: 0600, Tracked: origin.Tracked, Data: image, Preimage: origin.Data, PreimageIdentity: origin.PreimageIdentity, Values: maps.Clone(data)})
	}
	if err := configureRemoteSync(ctx, s, lock, step.Workspace, resources, resolved, options.factory()); err != nil {
		return syncModel{}, err
	}
	return syncModel{Images: images, Owners: owners}, nil
}

func stageSyncPlan(ctx context.Context, s *state.Store, lock *state.LockedWorkspace, workspaceID string, plan syncPlan) error {
	keyID, key, err := s.HMACKey(ctx)
	if err != nil {
		return err
	}
	exported := imageIntent(workspaceID, keyID, key, plan.Images)
	exported.HeadOID = plan.HeadOID
	exported.ManifestSHA256 = plan.ManifestSHA256
	exported.Owners = plan.Owners
	exported.Manifest = plan.Manifest
	if err := lock.StartSyncImages(ctx, exported); err != nil {
		return err
	}
	objects, err := s.PendingObjects()
	if err != nil {
		return err
	}
	defer objects.Close()
	for i, f := range exported.Files {
		if f.Preimage != nil {
			if err := objects.Create(ctx, f.PreimageRef, plan.Images[i].Preimage); err != nil {
				return err
			}
		}
		if err := objects.Create(ctx, f.StagedRef, plan.Images[i].Data); err != nil {
			return err
		}
	}
	if err := resumeSyncImages(ctx, s, lock, state.SyncStep{}); err != nil {
		return err
	}
	return nil
}
func resumeSyncImages(ctx context.Context, s *state.Store, lock *state.LockedWorkspace, step state.SyncStep) error {
	if step.Intent.KeyID == "" {
		var err error
		step, err = lock.SyncStep(ctx)
		if err != nil {
			return err
		}
	}
	_, key, err := s.HMACKey(ctx)
	if err != nil {
		return err
	}
	objects, err := s.PendingObjects()
	if err != nil {
		return err
	}
	defer objects.Close()
	if err := verifyImages(ctx, objects, key, state.FileStep{Workspace: step.Workspace, Intent: step.Intent}); err != nil {
		return err
	}
	return lock.RecordSyncImages(ctx)
}
func finishSync(ctx context.Context, s *state.Store, g *git.Client, lock *state.LockedWorkspace, step state.SyncStep) (SyncResult, error) {
	if step.State == "" || step.Intent.KeyID == "" {
		var err error
		step, err = lock.SyncStep(ctx)
		if err != nil {
			return SyncResult{}, err
		}
	}
	if step.State == "images_inflight" {
		if err := resumeSyncImages(ctx, s, lock, step); err != nil {
			return SyncResult{}, err
		}
		var e error
		if step, e = lock.SyncStep(ctx); e != nil {
			return SyncResult{}, e
		}
	}
	_, key, err := s.HMACKey(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	objects, err := s.PendingObjects()
	if err != nil {
		return SyncResult{}, err
	}
	defer objects.Close()
	images := make([]files.PublicationFile, 0, len(step.Intent.Files))
	for _, record := range step.Intent.Files {
		data, err := readImage(ctx, objects, key, step.Workspace.ID, record.Path, record.StagedRef, record.StagedHMAC, record.Size)
		if err != nil {
			return SyncResult{}, err
		}
		image := files.Image{Path: record.Path, Mode: 0600, Tracked: record.Tracked, PreimageIdentity: record.Preimage, Data: data, Values: map[string]string{}}
		if record.Preimage != nil {
			image.Preimage, err = readImage(ctx, objects, key, step.Workspace.ID, record.Path, record.PreimageRef, record.PreimageHMAC, record.Preimage.Size)
			if err != nil {
				return SyncResult{}, err
			}
		}
		images = append(images, files.PublicationFile{Image: image, Temp: files.TemporaryPath(record.Path, record.StagedRef), Receipt: recordReceipt(step, record.Path), Published: recordState(step, record.Path) == "succeeded"})
	}
	publisher, err := files.OpenPublisher(ctx, g, step.Identity, step.Workspace.Branch, step.Intent.HeadOID, step.Intent.Manifest, images)
	if err != nil {
		return SyncResult{}, err
	}
	defer publisher.Close()
	if _, err := publisher.Check(ctx); err != nil {
		return SyncResult{}, err
	}
	if !step.PublicationStarted {
		if err := lock.StartSyncPublication(ctx); err != nil {
			return SyncResult{}, err
		}
	}
	for _, f := range step.Intent.Files {
		if err := publisher.Publish(ctx, f.Path, func(receipt domain.FileIdentity) error { return lock.RecordSyncTemporary(ctx, f.Path, receipt) }); err != nil {
			return SyncResult{}, err
		}
		if recordState(step, f.Path) != "succeeded" {
			if err := lock.RecordSyncPublished(ctx, f.Path); err != nil {
				return SyncResult{}, err
			}
		}
	}
	if _, err := publisher.Check(ctx); err != nil {
		return SyncResult{}, err
	}
	if err := lock.CompleteSync(ctx, step.Intent.Owners); err != nil {
		return SyncResult{}, err
	}
	step, err = lock.SyncStep(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	if _, err := completeSyncCleanup(ctx, s, lock, step); err != nil {
		return SyncResult{}, err
	}
	workspace, err := s.Workspace(ctx, step.Workspace.ID)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Workspace: workspace, RestartRequired: true}, nil
}
func recordReceipt(step state.SyncStep, name string) *domain.FileIdentity {
	for _, f := range step.Files {
		if f.Path == name {
			return f.Receipt
		}
	}
	return nil
}
func recordState(step state.SyncStep, name string) string {
	for _, f := range step.Files {
		if f.Path == name {
			return f.State
		}
	}
	return ""
}
func completeSyncCleanup(ctx context.Context, s *state.Store, lock *state.LockedWorkspace, step state.SyncStep) (state.Workspace, error) {
	if step.Intent.Manifest == nil {
		return state.Workspace{}, &domain.Error{Code: "E_SYNC_STATE", Message: "sync cleanup intent is incomplete"}
	}
	_, key, err := s.HMACKey(ctx)
	if err != nil {
		return state.Workspace{}, err
	}
	objects, err := s.PendingObjects()
	if err != nil {
		return state.Workspace{}, err
	}
	defer objects.Close()
	for _, f := range step.Intent.Files {
		if f.Preimage != nil {
			if err := objects.RemoveVerified(ctx, f.PreimageRef, f.Preimage.Size, func(data []byte) bool {
				return private.Equal(key.File(step.Workspace.ID, f.Path, data), f.PreimageHMAC)
			}); err != nil {
				return state.Workspace{}, err
			}
		}
		if err := objects.RemoveVerified(ctx, f.StagedRef, f.Size, func(data []byte) bool { return private.Equal(key.File(step.Workspace.ID, f.Path, data), f.StagedHMAC) }); err != nil {
			return state.Workspace{}, err
		}
		if err := lock.RecordSyncImagesPurged(ctx, f.Path); err != nil {
			return state.Workspace{}, err
		}
	}
	return s.Workspace(ctx, step.Workspace.ID)
}
