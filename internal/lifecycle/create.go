package lifecycle

import (
	"context"

	"eve/internal/config"
	"eve/internal/git"
	"eve/internal/ports"
	"eve/internal/state"
)

// CreateLocal performs the complete LOCAL-ONLY ordered boundary for an already
// reviewed plan. It never installs dependencies, launches scripts or claims
// runtime health. The returned state is configuration preparation only.
func CreateLocal(ctx context.Context, s *state.Store, g *git.Client, p GitPlan, user config.UserConfig) (state.Workspace, state.Allocation, error) {
	lock, err := s.LockWorkspace(p.WorkspaceID)
	if err != nil {
		return state.Workspace{}, state.Allocation{}, err
	}
	defer lock.Close()
	if _, err := lock.BeginCreate(ctx, p.Intent(user)); err != nil {
		return state.Workspace{}, state.Allocation{}, err
	}
	allocation, err := ports.Reserve(ctx, lock, nil)
	if err != nil {
		return state.Workspace{}, state.Allocation{}, err
	}
	if _, err := PrepareGit(ctx, s, g, lock); err != nil {
		return state.Workspace{}, state.Allocation{}, err
	}
	workspaceBeforeProvision, err := s.Workspace(ctx, p.WorkspaceID)
	if err != nil {
		return state.Workspace{}, state.Allocation{}, err
	}
	if len(workspaceBeforeProvision.Manifest.Resources) != 0 {
		bindings, err := ProvisionResources(ctx, s, lock, workspaceBeforeProvision)
		if err != nil {
			return state.Workspace{}, state.Allocation{}, err
		}
		if _, err := StageFilesWithBindings(ctx, s, g, lock, p.Files, &bindings); err != nil {
			return state.Workspace{}, state.Allocation{}, err
		}
	} else if _, err := StageFiles(ctx, s, g, lock, p.Files); err != nil {
		return state.Workspace{}, state.Allocation{}, err
	}
	prepared, err := PublishFiles(ctx, s, g, lock)
	if err != nil {
		return state.Workspace{}, state.Allocation{}, err
	}
	if err := ctx.Err(); err != nil {
		return state.Workspace{}, state.Allocation{}, err
	}
	return prepared, allocation, nil
}
