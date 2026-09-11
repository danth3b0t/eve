package lifecycle

import (
	"context"
	"time"

	"eve/internal/config"
	"eve/internal/git"
	"eve/internal/ports"
	"eve/internal/state"
)

// CreateTimings contains monotonic phase durations without provider bodies,
// credentials, URLs or request contents.
type CreateTimings struct {
	IntentMS      int64 `json:"intent_ms"`
	ReservationMS int64 `json:"reservation_ms"`
	GitMS         int64 `json:"git_ms"`
	ProviderMS    int64 `json:"provider_ms"`
	FileStagingMS int64 `json:"file_staging_ms"`
	PublicationMS int64 `json:"publication_ms"`
	CreateTotalMS int64 `json:"create_total_ms"`
}

type CreateResult struct {
	Workspace  state.Workspace
	Allocation state.Allocation
	Timings    CreateTimings
}

// CreateProgress receives safe only phase labels. It never receives provider
// bodies, selectors, tokens or image data.
type CreateProgress func(string)

// CreateLocal performs the complete LOCAL-ONLY ordered boundary for an already
// reviewed plan. It never installs dependencies, launches scripts or claims
// runtime health. The returned state is configuration preparation only.
func CreateLocal(ctx context.Context, s *state.Store, g *git.Client, p GitPlan, user config.UserConfig) (CreateResult, error) {
	return CreateLocalProgress(ctx, s, g, p, user, nil)
}

func CreateLocalProgress(ctx context.Context, s *state.Store, g *git.Client, p GitPlan, user config.UserConfig, progress CreateProgress) (CreateResult, error) {
	totalStart := time.Now()
	var timings CreateTimings
	phaseStart := totalStart
	mark := func(target *int64) {
		now := time.Now()
		*target += now.Sub(phaseStart).Milliseconds()
		phaseStart = now
	}
	phase := func(name string) {
		if progress != nil {
			progress(name)
		}
	}
	phase("recording creation intent before the first write")
	lock, err := s.LockWorkspace(p.WorkspaceID)
	if err != nil {
		return CreateResult{}, err
	}
	defer lock.Close()
	if _, err := lock.BeginCreate(ctx, p.Intent(user)); err != nil {
		return CreateResult{}, err
	}
	mark(&timings.IntentMS)

	phase("reserving immutable local allocations")
	allocation, err := ports.Reserve(ctx, lock, nil)
	if err != nil {
		return CreateResult{}, err
	}
	mark(&timings.ReservationMS)

	phase("creating the exact Git worktree")
	if _, err := PrepareGit(ctx, s, g, lock); err != nil {
		return CreateResult{}, err
	}
	mark(&timings.GitMS)

	workspaceBeforeProvision, err := s.Workspace(ctx, p.WorkspaceID)
	if err != nil {
		return CreateResult{}, err
	}
	filesStart := time.Now()
	if len(workspaceBeforeProvision.Manifest.Resources) != 0 {
		providerStart := time.Now()
		phase("creating and verifying provider development resources")
		bindings, err := ProvisionResources(ctx, s, lock, workspaceBeforeProvision)
		timings.ProviderMS += time.Since(providerStart).Milliseconds()
		if err != nil {
			return CreateResult{}, err
		}
		filesStart = time.Now()
		phase("staging reviewed native file images")
		if _, err := StageFilesWithBindings(ctx, s, g, lock, p.Files, &bindings); err != nil {
			return CreateResult{}, err
		}
		phase("staging reviewed native file images")
	} else if _, err := StageFiles(ctx, s, g, lock, p.Files); err != nil {
		return CreateResult{}, err
	}
	// After Git, provider and file staging remain separately attributable.
	timings.FileStagingMS += time.Since(filesStart).Milliseconds()
	phaseStart = filesStart
	mark(&timings.PublicationMS)
	phase("publishing declared configuration only")
	prepared, err := PublishFiles(ctx, s, g, lock)
	if err != nil {
		return CreateResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return CreateResult{}, err
	}
	timings.CreateTotalMS = time.Since(totalStart).Milliseconds()
	return CreateResult{Workspace: prepared, Allocation: allocation, Timings: timings}, nil
}
