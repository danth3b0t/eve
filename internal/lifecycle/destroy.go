package lifecycle

import (
	"context"
	"errors"
	"os"
	"time"

	"eve/internal/domain"
	"eve/internal/files"
	"eve/internal/git"
	"eve/internal/ports"
	"eve/internal/private"
	"eve/internal/state"
)

type DestroyOptions struct {
	Approved, DiscardChanges, AssumeStopped bool
	Probe                                   ports.Prober  // nil uses real bounded TCP probes
	ProviderFactory                         convexFactory // test/provider transport injection; nil uses real HTTPS
}
type DestroyResult struct {
	Workspace state.Workspace
	Warning   string
}

func destroyedAbsence(id domain.GitIdentity) (bool, error) {
	for _, name := range []string{id.Path, id.AdminDir} {
		_, err := os.Lstat(name)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, &domain.Error{Code: "E_GIT_RECONCILE", Message: "removed path is inaccessible, not proven absent", Path: name}
		}
	}
	return true, nil
}

func ownedEdit(ctx context.Context, s *state.Store, w *state.LockedWorkspace, step state.DestroyStep) (func(string) (bool, error), error) {
	_, key, err := s.HMACKey(ctx)
	if err != nil {
		return nil, err
	}
	return func(name string) (bool, error) {
		hmac, mode, err := w.OwnedFile(ctx, name)
		if err != nil {
			return false, nil
		}
		verify := func(data []byte) bool { return private.Equal(key.File(step.Workspace.ID, name, data), hmac) }
		if err := files.CompareManagedContent(ctx, step.Identity, name, os.FileMode(mode), verify); err != nil {
			var d *domain.Error
			if errors.As(err, &d) && d.Code == "E_FILE_CHANGED" {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}, nil
}

func preGitDestroySafety(ctx context.Context, s *state.Store, g *git.Client, step state.DestroyStep, opts DestroyOptions) (*domain.GitIdentity, git.RemovalCheck, error) {
	repo, err := s.Repository(ctx, step.Workspace.RepositoryID)
	if err != nil {
		return nil, git.RemovalCheck{}, err
	}
	source, err := registeredSource(ctx, s, g, repo)
	if err != nil {
		return nil, git.RemovalCheck{}, err
	}
	if step.Workspace.Path == source.Identity.Path || step.Workspace.Path == repo.CommonDir {
		return nil, git.RemovalCheck{}, &domain.Error{Code: "E_GIT_OWNERSHIP", Message: "recorded pre-Git path overlaps the source"}
	}
	if err := outsideState(s, source); err != nil {
		return nil, git.RemovalCheck{}, err
	}
	allocation, err := s.Allocation(ctx, step.Workspace.ID)
	if err != nil {
		return nil, git.RemovalCheck{}, err
	}
	if err := ports.CheckStopped(ctx, allocation, opts.Probe, opts.AssumeStopped); err != nil {
		return nil, git.RemovalCheck{}, err
	}
	_, pathErr := os.Lstat(step.Workspace.Path)
	if errors.Is(pathErr, os.ErrNotExist) {
		return nil, git.RemovalCheck{Warning: "A partial pre-Git checkout directory is absent; Git metadata is not broadly pruned."}, nil
	}
	if pathErr != nil {
		return nil, git.RemovalCheck{}, &domain.Error{Code: "E_GIT_RECONCILE", Message: "recorded pre-Git path is inaccessible, not proven absent", Path: step.Workspace.Path}
	}
	checkout, err := g.Inspect(ctx, step.Workspace.Path)
	if err != nil {
		return nil, git.RemovalCheck{}, err
	}
	identity := checkout.Identity
	if identity.CommonIdentity != repo.CommonIdentity || checkout.Branch != step.Workspace.Branch || checkout.HeadOID != step.Workspace.HeadOID {
		return nil, git.RemovalCheck{}, &domain.Error{Code: "E_GIT_IDENTITY", Message: "pre-Git checkout no longer matches durable creation intent", Path: step.Workspace.Path}
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return nil, git.RemovalCheck{}, err
	}
	reference, err := git.CreationReference(step.CreateOperationID)
	if err != nil {
		return nil, git.RemovalCheck{}, err
	}
	check, err := g.CheckRemoval(ctx, identity, step.Workspace.Branch, reference, git.RemovalOptions{DiscardChanges: opts.DiscardChanges})
	return &identity, check, err
}
func destroySafety(ctx context.Context, s *state.Store, g *git.Client, step state.DestroyStep, opts DestroyOptions, owned func(string) (bool, error)) (git.RemovalCheck, error) {
	r, err := s.Repository(ctx, step.Workspace.RepositoryID)
	if err != nil {
		return git.RemovalCheck{}, err
	}
	source, err := registeredSource(ctx, s, g, r)
	if err != nil {
		return git.RemovalCheck{}, err
	}
	if err := outsideState(s, source); err != nil {
		return git.RemovalCheck{}, err
	}
	if step.Identity.Path == source.Identity.Path || step.Identity.CommonIdentity != source.Identity.CommonIdentity || step.Identity.AdminDir == step.Identity.CommonDir {
		return git.RemovalCheck{}, &domain.Error{Code: "E_GIT_OWNERSHIP", Message: "destroy identity no longer belongs to the registered source checkout"}
	}
	allocation, err := s.Allocation(ctx, step.Workspace.ID)
	if err != nil {
		return git.RemovalCheck{}, err
	}
	if err := ports.CheckStopped(ctx, allocation, opts.Probe, opts.AssumeStopped); err != nil {
		return git.RemovalCheck{}, err
	}
	reference, err := git.CreationReference(step.CreateOperationID)
	if err != nil {
		return git.RemovalCheck{}, err
	}
	return g.CheckRemoval(ctx, step.Identity, step.Workspace.Branch, reference, git.RemovalOptions{DiscardChanges: opts.DiscardChanges, OwnedEdit: owned})
}
func removeStaleBranch(ctx context.Context, s *state.Store, g *git.Client, step state.DestroyStep) error {
	if !step.NewBranch {
		return nil
	}
	scratch, err := s.ScratchDir()
	if err != nil {
		return err
	}
	return g.RemoveBranch(ctx, step.Identity, step.Workspace.Branch, step.Workspace.HeadOID, scratch)
}

// DestroyLocal removes only a completed LOCAL-ONLY generation 1 workspace, or
// resumes the exact same removal. Approval is external and cannot be inferred.
// Exact unchanged EVE-created branch metadata is pruned after local/remote
// effects; pre-existing branches and process supervision remain outside this call.
func DestroyLocal(ctx context.Context, s *state.Store, g *git.Client, w *state.LockedWorkspace, opts DestroyOptions) (DestroyResult, error) {
	step, err := w.DestroyStep(ctx)
	if err != nil {
		return DestroyResult{}, err
	}
	if step.State == "cleanup_pending" {
		absent, err := destroyedAbsence(step.Identity)
		if err != nil || !absent {
			return DestroyResult{}, &domain.Error{Code: "E_GIT_RECONCILE", Message: "the recorded removed path returned; no claims are released without verified absence", Path: step.Identity.Path}
		}
		return finishDestroy(ctx, s, g, w, step)
	}
	if !opts.Approved {
		return DestroyResult{}, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "destroy requires explicit approval; no local mutation was started"}
	}
	if step.State == "pre_git_ready" || step.State == "pre_git_inflight" {
		identity, check, err := preGitDestroySafety(ctx, s, g, step, opts)
		if err != nil {
			return DestroyResult{}, err
		}
		if step.State == "pre_git_ready" {
			if err := w.StartDestroy(ctx); err != nil {
				return DestroyResult{}, err
			}
			if step, err = w.DestroyStep(ctx); err != nil {
				return DestroyResult{}, err
			}
		}
		if identity != nil {
			if _, _, err := preGitDestroySafety(ctx, s, g, step, opts); err != nil {
				return DestroyResult{}, err
			}
		}
		if err := remoteDestructionForWorkspace(ctx, s, w, step.Workspace, opts); err != nil {
			return DestroyResult{}, err
		}
		if identity != nil {
			scratch, err := s.ScratchDir()
			if err != nil {
				return DestroyResult{}, err
			}
			reference, err := git.CreationReference(step.CreateOperationID)
			if err != nil {
				return DestroyResult{}, err
			}
			if err := g.Remove(ctx, *identity, step.Workspace.Branch, reference, scratch, git.RemovalOptions{DiscardChanges: opts.DiscardChanges, RemoveBranch: step.NewBranch, ExpectedOID: step.Workspace.HeadOID}); err != nil {
				return DestroyResult{}, err
			}
		}
		result, err := finishDestroy(ctx, s, g, w, step)
		result.Warning = check.Warning
		return result, err
	}
	if step.State == "ready" && step.Workspace.State == "prepared" && step.Workspace.Generation == 1 {
		// Reconcile any delayed create-image snapshot purge before retaining fingerprints.
		if _, err := PublishFiles(ctx, s, g, w); err != nil {
			return DestroyResult{}, err
		}
	}
	checkAbsent, err := destroyedAbsence(step.Identity)
	if err != nil {
		return DestroyResult{}, err
	}
	if step.State == "inflight" && checkAbsent {
		if err := remoteDestructionForWorkspace(ctx, s, w, step.Workspace, opts); err != nil {
			return DestroyResult{}, err
		}
		if err := removeStaleBranch(ctx, s, g, step); err != nil {
			return DestroyResult{}, err
		}
		return finishDestroy(ctx, s, g, w, step)
	}
	r, err := s.Repository(ctx, step.Workspace.RepositoryID)
	if err != nil {
		return DestroyResult{}, err
	}
	owned, err := ownedEdit(ctx, s, w, step)
	if err != nil {
		return DestroyResult{}, err
	}
	check, err := destroySafety(ctx, s, g, step, opts, owned)
	if err != nil {
		return DestroyResult{}, err
	}
	if step.State == "ready" {
		if err := w.StartDestroy(ctx); err != nil {
			return DestroyResult{}, err
		}
		if step, err = w.DestroyStep(ctx); err != nil {
			return DestroyResult{}, err
		}
	}
	if err := remoteDestructionForWorkspace(ctx, s, w, step.Workspace, opts); err != nil {
		return DestroyResult{}, err
	}
	err = func() error {
		// Only shared Git deletion uses the repository mutex; remote and file work
		// remain under the workspace lock so independent operations can overlap.
		repoLock, err := s.LockRepository(ctx, r.ID)
		if err != nil {
			return err
		}
		defer repoLock.Close()
		if _, err := destroySafety(ctx, s, g, step, opts, owned); err != nil {
			return err
		}
		scratch, err := s.ScratchDir()
		if err != nil {
			return err
		}
		reference, err := git.CreationReference(step.CreateOperationID)
		if err != nil {
			return err
		}
		return g.Remove(ctx, step.Identity, step.Workspace.Branch, reference, scratch, git.RemovalOptions{DiscardChanges: opts.DiscardChanges, RemoveBranch: step.NewBranch, ExpectedOID: step.Workspace.HeadOID, OwnedEdit: owned})
	}()
	if absent, absentErr := destroyedAbsence(step.Identity); absent && absentErr == nil {
		journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		result, finishErr := finishDestroy(journal, s, g, w, step)
		result.Warning = check.Warning
		return result, finishErr
	}
	if err != nil {
		return DestroyResult{}, err
	}
	result, finishErr := finishDestroy(ctx, s, g, w, step)
	result.Warning = check.Warning
	return result, finishErr
}
func remoteDestructionForWorkspace(ctx context.Context, s *state.Store, w *state.LockedWorkspace, workspace state.Workspace, opts DestroyOptions) error {
	if workspace.Manifest.Resources == nil || len(workspace.Manifest.Resources) == 0 {
		return nil
	}
	factory := opts.ProviderFactory
	if factory == nil {
		factory = defaultConvexFactory
	}
	return destroyResources(ctx, s, w, workspace, factory)
}

func finishDestroy(ctx context.Context, s *state.Store, g *git.Client, w *state.LockedWorkspace, step state.DestroyStep) (DestroyResult, error) {
	allocation, err := s.Allocation(ctx, step.Workspace.ID)
	if err != nil {
		return DestroyResult{}, err
	}
	if err := ports.CheckEndpoints(ctx, allocation, nil); err != nil {
		code := ""
		var d *domain.Error
		if errors.As(err, &d) {
			code = d.Code
		}
		if code == "E_PORT_OCCUPIED" {
			if step.State == "inflight" {
				journal, journalCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer journalCancel()
				if journalErr := w.RecordCleanupPending(journal); journalErr != nil {
					return DestroyResult{}, journalErr
				}
			}
			workspace, _ := s.Workspace(ctx, step.Workspace.ID)
			return DestroyResult{Workspace: workspace}, &domain.Error{Code: "E_CLEANUP_PENDING", Message: "local worktree is gone, but an allocated endpoint still has a listener; repeated destroy can release claims after assessment"}
		}
		return DestroyResult{}, err
	}
	resources, err := w.Resources(ctx)
	if err != nil {
		return DestroyResult{}, err
	}
	for _, resource := range resources {
		if resource.CredentialID != "" {
			if err := s.DeleteCredentialObject(ctx, resource.CredentialID); err != nil {
				return DestroyResult{}, err
			}
		}
	}
	if err := w.RecordDestroyed(ctx); err != nil {
		return DestroyResult{}, err
	}
	workspace, err := s.Workspace(ctx, step.Workspace.ID)
	if err != nil {
		return DestroyResult{}, err
	}
	if workspace.State != "destroyed" {
		return DestroyResult{}, &domain.Error{Code: "E_STATE_INTENT", Message: "destroy tombstone was not committed"}
	}
	return DestroyResult{Workspace: workspace}, nil
}
func (r DestroyResult) withWarning(warning string) DestroyResult { r.Warning = warning; return r }
