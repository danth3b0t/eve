package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eve/internal/provider/convex"
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

func runConcurrentLockProbe(t *testing.T, started <-chan struct{}, release chan struct{}, acquire func(), operation func() error) {
	t.Helper()
	done := make(chan error, 1)
	acquired := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		done <- operation()
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider model did not start")
	}
	go func() {
		acquire()
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("repository mutation lock was reacquired while provider work was in flight")
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("repository mutation lock was lost after provider work finished")
	}
	wg.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProvisioningAndSyncHoldRepositoryLockAcrossProviderModel(t *testing.T) {
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
	runConcurrentLockProbe(t, started, release, func() {
		l, err := r.store.LockRepository(workspace.RepositoryID)
		if err == nil {
			l.Close()
		}
	}, func() error {
		var err error
		bindings, err = provisionResources(t.Context(), r.store, lock, workspace, factory)
		return err
	})

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

	// A no-change sync still reviews provider state/resolution before deciding not
	// to start a generation; that provider review must hold the same repository lock.
	started = make(chan struct{}, 8)
	release = make(chan struct{})
	calls.Store(0)
	syncFactory := func(token string) (convexAdapter, error) {
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
	runConcurrentLockProbe(t, started, release, func() {
		l, err := r.store.LockRepository(workspace.RepositoryID)
		if err == nil {
			l.Close()
		}
	}, func() error {
		_, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{ProviderFactory: syncFactory})
		return err
	})
}
