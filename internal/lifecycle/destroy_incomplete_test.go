package lifecycle

import (
	"os"
	"testing"
	"time"

	"eve/internal/git"
)

func TestDoctorInspectsWorkspaceBeforeGitReceipt(t *testing.T) {
	r, plan := syncLocalFixtureless(t)
	lock := approved(t, r, plan)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := DoctorWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, check := range result.Checks {
		statuses[check.ID] = check.Status
	}
	if statuses["git"] != "warning" || statuses["files"] != "not_checked" {
		t.Fatalf("doctor hardened mutation eligibility into failure: %v", result.Checks)
	}
}

func syncLocalFixtureless(t *testing.T) (repository, GitPlan) {
	t.Helper()
	r := repositoryFixture(t)
	if err := os.WriteFile(r.root+"/.gitignore", []byte("/.env.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "fixture baseline")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "pre-git-doctor", "")
	if err != nil {
		t.Fatal(err)
	}
	return r, plan
}

func TestDestroySupersedesCreateBeforeGitReceipt(t *testing.T) {
	r, plan := syncLocalFixtureless(t)
	lock := approved(t, r, plan)
	if err := lock.StartGit(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := git.CreationReference(current.OperationID)
	source, err := r.client.Inspect(t.Context(), r.root)
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := r.store.ScratchDir()
	if err != nil {
		t.Fatal(err)
	}
	added, err := r.client.Add(t.Context(), git.AddRequest{Source: source.Identity, Target: plan.Target, Path: plan.Path, Reference: reference, Scratch: scratch})
	if err != nil {
		t.Fatal(err)
	}
	current, err = r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || current.State != "creating" {
		t.Fatal(err)
	}
	result, err := DestroyLocal(t.Context(), r.store, r.client, lock, DestroyOptions{Approved: true})
	if err != nil || result.Workspace.State != "destroyed" {
		t.Fatalf("pre-receipt workspace was not abandoned exactly: %+v %v", result.Workspace, err)
	}
	if _, err := os.Lstat(added.Identity.Path); !os.IsNotExist(err) || !pathMissing(added.Identity.AdminDir) {
		t.Fatalf("pre-receipt worktree/admin evidence remains: %v", err)
	}
	if allocation, err := r.store.Allocation(t.Context(), plan.WorkspaceID); err != nil || len(allocation.Endpoints) != 0 {
		t.Fatalf("destroyed pre-receipt allocation remains live: %+v %v", allocation, err)
	}
}

func TestDestroySupersedesCreateAfterCloudFailBeforePublication(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	identity, err := PrepareGit(t.Context(), r.store, r.client, lock)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	managed := make(chan map[string]string, 1)
	factory := fakeConvexFactory(t, managed, false, nil, nil)
	if _, err := provisionResources(t.Context(), r.store, lock, current, factory); err != nil {
		t.Fatal(err)
	}
	<-managed
	current, err = r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || current.State != "creating" {
		t.Fatalf("fixture unexpectedly published: %+v %v", current, err)
	}
	result, err := DestroyLocal(t.Context(), r.store, r.client, lock, DestroyOptions{Approved: true, ProviderFactory: factory})
	if err != nil || result.Workspace.State != "destroyed" {
		t.Fatalf("interrupted cloud create could not be destroyed: %+v %v", result.Workspace, err)
	}
	resources, err := r.store.Resources(t.Context(), plan.WorkspaceID)
	if err != nil || len(resources) != 1 || resources[0].State != "deleted" {
		t.Fatalf("resource escape: %+v %v", resources, err)
	}
	if _, err := os.Lstat(identity.Path); !os.IsNotExist(err) {
		t.Fatal("worktree retained")
	}
	if expires := time.Until(time.UnixMilli(resources[0].ExpiresAtMS)); expires <= 0 {
		t.Fatal("remote expiry was not captured before deletion")
	}
}
func TestDestroySupersedesInterruptedSyncJournal(t *testing.T) {
	r, plan := syncLocalFixture(t)
	upgradeManifest(plan, "version = 1\n[services.api]\npath = \".\"\nenv_file = \".env.local\"\n[services.api.env]\nPUBLIC_NAME = \"old\"\nSUPERSEDED = \"yes\"\n", t)
	lock, err := r.store.LockWorkspace(plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := lock.SyncStep(t.Context())
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	interrupted, err := prepareSyncPlan(t.Context(), r.store, r.client, lock, step, SyncOptions{})
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err := stageSyncPlan(t.Context(), r.store, lock, plan.WorkspaceID, interrupted); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	result, err := DestroyLocal(t.Context(), r.store, r.client, lock, DestroyOptions{Approved: true})
	if err != nil || result.Workspace.State != "destroyed" {
		t.Fatalf("interrupted sync could not be abandoned: %+v %v", result.Workspace, err)
	}
	workspace, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || workspace.State != "destroyed" || workspace.Generation != 1 {
		t.Fatalf("destroy rewrote the abandoned generation: %+v %v", workspace, err)
	}
}

func pathMissing(path string) bool {
	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}
