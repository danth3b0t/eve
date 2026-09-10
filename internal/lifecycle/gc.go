package lifecycle

import (
	"context"

	"eve/internal/domain"
	"eve/internal/git"
	"eve/internal/state"
)

// GCOptions mirrors destroy's provider selection. There is never an automatic
// periodic cleanup worker or approval by resource/reference prefix alone.
type GCOptions struct{ ProviderFactory convexFactory }

// ApplyGC completes only exact garbage reported with checked ownership. A
// missing registered worktree is completed under a NEW durable destroy intent;
// incomplete/expired/failed state is reported separately by the caller.
func ApplyGC(ctx context.Context, s *state.Store, g *git.Client, workspaceID string, options GCOptions) (DestroyResult, error) {
	lock, err := s.LockWorkspace(workspaceID)
	if err != nil {
		return DestroyResult{}, err
	}
	defer lock.Close()
	workspace, err := s.Workspace(ctx, workspaceID)
	if err != nil {
		return DestroyResult{}, err
	}
	factory := options.ProviderFactory
	if factory == nil {
		factory = defaultConvexFactory
	}
	if workspace.State == "destroying" || workspace.State == "cleanup_pending" {
		return DestroyLocal(ctx, s, g, lock, DestroyOptions{Approved: true, ProviderFactory: factory})
	}
	if workspace.State != "prepared" {
		return DestroyResult{}, &domain.Error{Code: "E_GC_NOT_GARBAGE", Message: "workspace does not have complete destruction identity", Path: workspace.Path}
	}
	step, err := lock.DestroyStep(ctx)
	if err != nil {
		return DestroyResult{}, err
	}
	absent, err := destroyedAbsence(step.Identity)
	if err != nil || !absent {
		if err == nil {
			err = &domain.Error{Code: "E_GC_NOT_GARBAGE", Message: "a workspace or admin path is still present; GC does not remove existing local code", Path: workspace.Path}
		}
		return DestroyResult{}, err
	}
	if err := lock.StartDestroy(ctx); err != nil {
		return DestroyResult{}, err
	}
	step, err = lock.DestroyStep(ctx)
	if err != nil {
		return DestroyResult{}, err
	}
	if err := remoteDestructionForWorkspace(ctx, s, lock, step.Workspace, DestroyOptions{Approved: true, ProviderFactory: factory}); err != nil {
		return DestroyResult{}, err
	}
	return finishDestroy(ctx, s, g, lock, step)
}
