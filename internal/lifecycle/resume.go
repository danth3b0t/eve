package lifecycle

import (
	"context"

	"eve/internal/domain"
	"eve/internal/files"
	"eve/internal/git"
	"eve/internal/ports"
	"eve/internal/state"
)

// ResumeOptions intentionally contains provider retry inputs only. Resume is an
// explicit user operation, not an automatic background cleanup task.
type ResumeOptions struct{ ProviderFactory convexFactory }

func (o ResumeOptions) provider() convexFactory {
	if o.ProviderFactory != nil {
		return o.ProviderFactory
	}
	return defaultConvexFactory
}

// ResumeWorkspace continues an operation using the immutable workspace/target
// manifest already journaled in state. It never plans a replacement workspace,
// deployment, branch or project from the current source checkout.
func ResumeWorkspace(ctx context.Context, s *state.Store, g *git.Client, workspaceID string, options ResumeOptions) (state.Workspace, error) {
	lock, err := s.LockWorkspace(workspaceID)
	if err != nil {
		return state.Workspace{}, err
	}
	defer lock.Close()
	workspace, err := s.Workspace(ctx, workspaceID)
	if err != nil {
		return state.Workspace{}, err
	}
	switch workspace.State {
	case "prepared":
		return PublishFiles(ctx, s, g, lock)
	case "destroying", "cleanup_pending":
		result, err := DestroyLocal(ctx, s, g, lock, DestroyOptions{Approved: true, ProviderFactory: options.provider()})
		return result.Workspace, err
	case "creating":
		return resumeCreate(ctx, s, g, lock, workspace, options)
	default:
		return state.Workspace{}, &domain.Error{Code: "E_CREATE_RESUME", Message: "this workspace is not in an active create/destroy operation"}
	}
}
func resumeCreate(ctx context.Context, s *state.Store, g *git.Client, lock *state.LockedWorkspace, workspace state.Workspace, options ResumeOptions) (state.Workspace, error) {
	if workspace.Phase == "reserve" {
		if _, err := ports.Reserve(ctx, lock, nil); err != nil {
			return state.Workspace{}, err
		}
	}
	if _, err := PrepareGit(ctx, s, g, lock); err != nil {
		return state.Workspace{}, err
	}
	// Recreate a source snapshot against the pinned blob only after Git has proved
	// the original target identity; a changed current manifest cannot redirect it.
	repo, err := s.Repository(ctx, workspace.RepositoryID)
	if err != nil {
		return state.Workspace{}, err
	}
	source, err := registeredSource(ctx, s, g, repo)
	if err != nil {
		return state.Workspace{}, err
	}
	manifest, err := g.Manifest(ctx, repo.SourcePath, workspace.HeadOID)
	if err != nil {
		return state.Workspace{}, err
	}
	plan, err := files.Snapshot(ctx, g, source.Identity, git.Target{Branch: workspace.Branch, HeadOID: workspace.HeadOID, Manifest: manifest})
	if err != nil {
		return state.Workspace{}, err
	}
	resources, err := lock.Resources(ctx)
	if err != nil {
		return state.Workspace{}, err
	}
	if len(resources) == 0 {
		if _, err = StageFiles(ctx, s, g, lock, plan); err != nil {
			return state.Workspace{}, err
		}
		return PublishFiles(ctx, s, g, lock)
	}
	bindings, err := provisionResources(ctx, s, lock, workspace, options.provider())
	if err != nil {
		return state.Workspace{}, err
	}
	if _, err = StageFilesWithBindings(ctx, s, g, lock, plan, &bindings); err != nil {
		return state.Workspace{}, err
	}
	return PublishFiles(ctx, s, g, lock)
}
