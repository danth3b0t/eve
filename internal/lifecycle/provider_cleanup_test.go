package lifecycle

import (
	"os"
	"testing"
)

func TestRemoteDeleteDeniedRetainsWorktreeThenRecoversExactly(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	failDelete := true
	factory := fakeConvexFactory(t, make(chan map[string]string, 2), false, nil, &failDelete)
	bindings, err := provisionResources(t.Context(), r.store, lock, current, factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StageFilesWithBindings(t.Context(), r.store, r.client, lock, plan.Files, &bindings); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	_, err = DestroyLocal(t.Context(), r.store, r.client, lock, DestroyOptions{Approved: true, ProviderFactory: factory})
	errorCode(t, err, "E_CLEANUP_PENDING")
	if _, statErr := os.Stat(plan.Path); statErr != nil {
		t.Fatal("failed remote deletion removed local worktree")
	}
	resources, resErr := lock.Resources(t.Context())
	if resErr != nil || resources[0].State != "cleanup_pending" {
		t.Fatal("remote cleanup identity was not retained")
	}
	failDelete = false
	result, err := DestroyLocal(t.Context(), r.store, r.client, lock, DestroyOptions{Approved: true, ProviderFactory: factory})
	if err != nil || result.Workspace.State != "destroyed" {
		t.Fatalf("exact cleanup recovery failed: %v", err)
	}
	resources, resErr = lock.Resources(t.Context())
	if resErr != nil || resources[0].State != "deleted" {
		t.Fatal("recoverable failed delete did not complete")
	}
	if _, statErr := os.Lstat(plan.Path); !os.IsNotExist(statErr) {
		t.Fatal("cleanup kept local worktree")
	}
}
