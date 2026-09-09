package lifecycle

import (
	"context"
	"os"
	"path/filepath"

	"eve/internal/domain"
	"eve/internal/files"
	"eve/internal/git"
	"eve/internal/private"
	"eve/internal/resolve"
	"eve/internal/state"
	"github.com/google/uuid"
)

// StageFiles freezes every image before any application-file publication. plan
// must be the approved PlanGit.Files snapshot. On reentry it is ignored: only
// exact protected objects named in the durable intent can complete staging.
// Missing/partial objects remain unresolved, never rebuilt from today's source.
// The caller retains this store's workspace lock across lifecycle boundaries.
func StageFiles(ctx context.Context, s *state.Store, g *git.Client, locked *state.LockedWorkspace, plan *files.Plan) (state.FileStep, error) {
	step, err := locked.FileStep(ctx)
	if err != nil {
		return state.FileStep{}, err
	}
	if step.Workspace.State != "creating" {
		return state.FileStep{}, &domain.Error{Code: "E_FILE_STEP_STATE", Message: "initial staging is already complete"}
	}
	r, err := s.Repository(ctx, step.Workspace.RepositoryID)
	if err != nil {
		return state.FileStep{}, err
	}
	repoLock, err := s.LockRepository(r.ID)
	if err != nil {
		return state.FileStep{}, err
	}
	defer repoLock.Close()
	if _, err := registeredSource(ctx, s, g, r); err != nil {
		return state.FileStep{}, err
	}
	checkout, err := g.Verify(ctx, step.Identity)
	if err != nil {
		return state.FileStep{}, err
	}
	if checkout.HeadOID != step.Workspace.HeadOID || checkout.Branch != step.Workspace.Branch {
		return state.FileStep{}, &domain.Error{Code: "E_GIT_REF_CHANGED", Message: "target changed before staging"}
	}
	if err := outsideState(s, checkout); err != nil {
		return state.FileStep{}, err
	}
	var images []files.Image
	if step.State == "pending" {
		if plan == nil {
			return state.FileStep{}, &domain.Error{Code: "E_FILE_PLAN", Message: "approved source snapshot is required for the first staging attempt"}
		}
		allocation, err := s.Allocation(ctx, step.Workspace.ID)
		if err != nil {
			return state.FileStep{}, err
		}
		if !allocation.Ready {
			return state.FileStep{}, &domain.Error{Code: "E_FILE_STEP_STATE", Message: "accepted allocation is required before staging"}
		}
		images, err = plan.Prepare(ctx, g, step.Identity, localInputs(step.Workspace, allocation))
		if err != nil {
			return state.FileStep{}, err
		}
	}
	keyID, key, err := s.HMACKey(ctx)
	if err != nil {
		return state.FileStep{}, err
	}
	objects, err := s.PendingObjects()
	if err != nil {
		return state.FileStep{}, err
	}
	defer objects.Close()
	if step.State == "pending" {
		intent := imageIntent(step.Workspace.ID, keyID, key, images)
		if err := locked.StartFiles(ctx, intent); err != nil {
			return state.FileStep{}, err
		}
		// StartFiles has committed ALL references before the first sensitive write.
		for i, f := range intent.Files {
			if f.Preimage != nil {
				if err := objects.Create(ctx, f.PreimageRef, images[i].Preimage); err != nil {
					return state.FileStep{}, err
				}
			}
			if err := objects.Create(ctx, f.StagedRef, images[i].Data); err != nil {
				return state.FileStep{}, err
			}
		}
		step, err = locked.FileStep(ctx)
		if err != nil {
			return state.FileStep{}, err
		}
	}
	if step.Intent.KeyID != keyID {
		return state.FileStep{}, &domain.Error{Code: "E_HMAC_KEY", Message: "staging key differs from the recorded machine key"}
	}
	if err := verifyImages(ctx, objects, key, step); err != nil {
		return state.FileStep{}, err
	}
	if step.State == "inflight" {
		if err := locked.RecordFiles(ctx); err != nil {
			return state.FileStep{}, err
		}
	}
	return locked.FileStep(ctx)
}

func localInputs(w state.Workspace, a state.Allocation) resolve.Inputs {
	in := resolve.Inputs{Workspace: resolve.Workspace{ID: w.ID, Slug: filepath.Base(w.Path), Branch: w.Branch}, Ports: map[resolve.Endpoint]int{}}
	if a.Size != 0 {
		in.Workspace.PortBase = &a.Base
	}
	for _, ep := range a.Endpoints {
		in.Ports[resolve.Endpoint{Service: ep.Service, Name: ep.Name}] = ep.Port
	}
	return in
}

func imageIntent(workspace, keyID string, key *private.Key, images []files.Image) state.FileIntent {
	in := state.FileIntent{KeyID: keyID}
	for _, image := range images {
		f := state.FileRecord{Path: image.Path, Tracked: image.Tracked, Preimage: image.PreimageIdentity, StagedRef: uuid.NewString(), StagedHMAC: key.File(workspace, image.Path, image.Data), Size: int64(len(image.Data)), Mode: uint32(image.Mode), Values: map[string]string{}}
		if f.Preimage != nil {
			f.PreimageRef = uuid.NewString()
			f.PreimageHMAC = key.File(workspace, image.Path, image.Preimage)
		}
		for name, value := range image.Values {
			f.Values[name] = key.Value(workspace, image.Path, name, value)
		}
		in.Files = append(in.Files, f)
	}
	return in
}

func verifyImages(ctx context.Context, objects *private.Objects, key *private.Key, step state.FileStep) error {
	for _, f := range step.Intent.Files {
		if _, err := readImage(ctx, objects, key, step.Workspace.ID, f.Path, f.StagedRef, f.StagedHMAC, f.Size); err != nil {
			return err
		}
		if f.Preimage != nil {
			if _, err := readImage(ctx, objects, key, step.Workspace.ID, f.Path, f.PreimageRef, f.PreimageHMAC, f.Preimage.Size); err != nil {
				return err
			}
		}
	}
	return nil
}
func readImage(ctx context.Context, objects *private.Objects, key *private.Key, workspace, path, ref, mac string, size int64) ([]byte, error) {
	data, err := objects.Read(ctx, ref, size)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size || !private.Equal(key.File(workspace, path, data), mac) {
		return nil, &domain.Error{Code: "E_STAGE_CHANGED", Message: "protected image differs from its durable intent; do not restage or publish", Path: path}
	}
	return data, nil
}

// StagedImage reads exactly one journaled image and verifies its full-file HMAC.
// It does not authorize replacement: the publisher must recheck Git/policy,
// preimage identity/content, links, aliases and endpoints at the mutation boundary.
func StagedImage(ctx context.Context, s *state.Store, locked *state.LockedWorkspace, name string) (files.Image, error) {
	step, err := locked.FileStep(ctx)
	if err != nil {
		return files.Image{}, err
	}
	if step.State != "succeeded" {
		return files.Image{}, &domain.Error{Code: "E_FILE_STEP_STATE", Message: "all images must be staged before reading a publication image"}
	}
	id, key, err := s.HMACKey(ctx)
	if err != nil {
		return files.Image{}, err
	}
	if id != step.Intent.KeyID {
		return files.Image{}, &domain.Error{Code: "E_HMAC_KEY", Message: "staging key differs from the recorded machine key"}
	}
	objects, err := s.PendingObjects()
	if err != nil {
		return files.Image{}, err
	}
	defer objects.Close()
	for _, f := range step.Intent.Files {
		if f.Path == name {
			image := files.Image{Path: f.Path, Mode: os.FileMode(f.Mode), Tracked: f.Tracked, PreimageIdentity: f.Preimage}
			image.Data, err = readImage(ctx, objects, key, step.Workspace.ID, f.Path, f.StagedRef, f.StagedHMAC, f.Size)
			if err != nil {
				return files.Image{}, err
			}
			if f.Preimage != nil {
				image.Preimage, err = readImage(ctx, objects, key, step.Workspace.ID, f.Path, f.PreimageRef, f.PreimageHMAC, f.Preimage.Size)
			}
			if err != nil {
				return files.Image{}, err
			}
			return image, nil
		}
	}
	return files.Image{}, &domain.Error{Code: "E_FILE_INTENT", Message: "file does not belong to the staged transaction"}
}
