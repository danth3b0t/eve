package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eve/internal/provider/convex"
	"eve/internal/state"
)

type blockingEnvAdapter struct {
	convexAdapter
	call func() error
}

func (a blockingEnvAdapter) Env(ctx context.Context, deployment convex.Deployment, key string) (map[string]string, error) {
	if err := a.call(); err != nil {
		return nil, err
	}
	return a.convexAdapter.Env(ctx, deployment, key)
}

func assertRepositoryLockAvailable(t *testing.T, s *state.Store, repositoryID string) {
	t.Helper()
	lock, err := s.LockRepository(t.Context(), repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func runScopedRepositoryLockProbe(t *testing.T, started <-chan struct{}, release chan struct{}, repositoryID func() string, s *state.Store, operation func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- operation() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider model did not start")
	}
	assertRepositoryLockAvailable(t, s, repositoryID())
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider work did not recover after the bounded model probe")
	}
}

func TestProviderAndSyncDoNotHoldRepositoryLock(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	workspace, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var calls atomic.Int64
	baseFactory := fakeConvexFactory(t, make(chan map[string]string, 4), false, nil, nil)
	factory := func(token string) (convexAdapter, error) {
		api, err := baseFactory(token)
		if err != nil {
			return nil, err
		}
		return blockingEnvAdapter{convexAdapter: api, call: func() error {
			if calls.Add(1) == 1 {
				started <- struct{}{}
				<-release
			}
			return nil
		}}, nil
	}

	var bindings bindingResult
	operation := func() error {
		var err error
		bindings, err = provisionResources(t.Context(), r.store, lock, workspace, factory)
		return err
	}
	runScopedRepositoryLockProbe(t, started, release, func() string { return workspace.RepositoryID }, r.store, operation)

	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if _, err := StageFilesWithBindings(t.Context(), r.store, r.client, lock, plan.Files, &bindings); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	started = make(chan struct{}, 8)
	release = make(chan struct{})
	calls.Store(0)
	syncOperation := func() error {
		_, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{ProviderFactory: factory})
		return err
	}
	runScopedRepositoryLockProbe(t, started, release, func() string { return workspace.RepositoryID }, r.store, syncOperation)
}

func nativePlan(t *testing.T, r repository, branch string) GitPlan {
	t.Helper()
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func completeNativeCreate(t *testing.T, r repository, plan GitPlan, lock *state.LockedWorkspace) error {
	t.Helper()
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		return err
	}
	if _, err := StageFiles(t.Context(), r.store, r.client, lock, plan.Files); err != nil {
		return err
	}
	_, err := PublishFiles(t.Context(), r.store, r.client, lock)
	return err
}

func TestTwoIndependentNativeCreatesCompleteConcurrently(t *testing.T) {
	r := repositoryFixture(t)
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte("/.env.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "ignore local values")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plans := []GitPlan{nativePlan(t, r, "concurrent-one"), nativePlan(t, r, "concurrent-two")}
	locks := []*state.LockedWorkspace{approved(t, r, plans[0]), approved(t, r, plans[1])}

	var wg sync.WaitGroup
	errs := make(chan error, len(plans))
	for i, plan := range plans {
		wg.Add(1)
		go func(i int, plan GitPlan) {
			defer wg.Done()
			errs <- completeNativeCreate(t, r, plan, locks[i])
		}(i, plan)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, plan := range plans {
		workspace, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
		if err != nil || workspace.State != "prepared" || workspace.Generation != 1 {
			t.Fatalf("independent concurrent create did not complete: %+v %v", workspace, err)
		}
	}
}
