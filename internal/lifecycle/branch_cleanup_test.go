package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func branchFixture(t *testing.T, branch, from string) (repository, GitPlan) {
	t.Helper()
	r := repositoryFixture(t)
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte("/.env.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "ignore local values")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, branch, from)
	if err != nil {
		t.Fatal(err)
	}
	lock := approved(t, r, plan)
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if _, err := StageFiles(t.Context(), r.store, r.client, lock, plan.Files); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	return r, plan
}

func branchExists(t *testing.T, r repository, branch string) bool {
	t.Helper()
	_, exists, err := r.client.BranchReference(t.Context(), r.root, branch)
	if err != nil {
		t.Fatal(err)
	}
	return exists
}

func destroyByID(t *testing.T, r repository, workspace string) (DestroyResult, error) {
	t.Helper()
	lock, err := r.store.LockWorkspace(workspace)
	if err != nil {
		return DestroyResult{}, err
	}
	defer lock.Close()
	return DestroyLocal(t.Context(), r.store, r.client, lock, DestroyOptions{Approved: true})
}

func TestDestroyRemovesExactEveCreatedBranch(t *testing.T) {
	r, plan := branchFixture(t, "eve-created-branch", "")
	if _, err := destroyByID(t, r, plan.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if branchExists(t, r, plan.Target.Branch) {
		t.Fatal("EVE-created branch metadata remained")
	}
	workspace, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || workspace.State != "destroyed" || workspace.Branch != plan.Target.Branch || workspace.Path != plan.Path {
		t.Fatal("tombstone identity was lost")
	}
}

func TestDestroyPreservesAdvancedEveCreatedBranch(t *testing.T) {
	r, plan := branchFixture(t, "advanced-created-branch", "")
	probeLock, err := r.store.LockWorkspace(plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	probeStep, err := probeLock.DestroyStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := probeLock.Close(); err != nil {
		t.Fatal(err)
	}
	if !probeStep.NewBranch {
		t.Fatal("durable create intent lost its EVE-created branch marker")
	}
	if err := os.WriteFile(filepath.Join(plan.Path, "advanced.txt"), []byte("advanced\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, plan.Path, "add", "advanced.txt")
	command(t, plan.Path, "commit", "-qm", "user target update")
	if _, err := destroyByID(t, r, plan.WorkspaceID); err != nil {
		t.Fatal("advanced worktree destruction should preserve the divergent branch", err)
	}
	if !branchExists(t, r, plan.Target.Branch) {
		t.Fatal("changed EVE-created branch was removed")
	}
	workspace, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || workspace.State != "destroyed" || workspace.Branch != plan.Target.Branch {
		t.Fatalf("changed branch destroyed audit: %+v %v", workspace, err)
	}
}

func TestDestroyPreservesExactPreexistingBranch(t *testing.T) {
	r := repositoryFixture(t)
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte("/.env.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "ignore local values")
	command(t, r.root, "checkout", "-qb", "owned-source-branch")
	command(t, r.root, "checkout", "main")
	original := command(t, r.root, "rev-parse", "refs/heads/owned-source-branch")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "owned-source-branch", "")
	if err != nil {
		t.Fatal(err)
	}
	lock := approved(t, r, plan)
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if _, err := StageFiles(t.Context(), r.store, r.client, lock, plan.Files); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := destroyByID(t, r, plan.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	current, exists, err := r.client.BranchReference(t.Context(), r.root, plan.Target.Branch)
	if err != nil || !exists || current != original {
		t.Fatal("pre-existing source branch was removed or retargeted")
	}
}

func TestGCPreservesAdvancedBranchAfterManualWorktreeLoss(t *testing.T) {
	r, plan := branchFixture(t, "lost-worktree-branch", "")
	command(t, r.root, "worktree", "remove", "--force", "--", plan.Path)
	command(t, r.root, "worktree", "prune")

	command(t, r.root, "checkout", "-qb", "tip-probe")
	probePath := filepath.Join(r.root, "probe.txt")
	if err := os.WriteFile(probePath, []byte("advanced\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", "probe.txt")
	command(t, r.root, "commit", "-qm", "advanced tip")
	advanced := command(t, r.root, "rev-parse", "tip-probe")
	command(t, r.root, "checkout", "main")
	command(t, r.root, "update-ref", "refs/heads/"+plan.Target.Branch, advanced)
	command(t, r.root, "branch", "-D", "tip-probe")

	if _, err := ApplyGC(t.Context(), r.store, r.client, plan.WorkspaceID, GCOptions{}); err != nil {
		t.Fatal("advanced branch preserve cleanup failed", err)
	}
	if !branchExists(t, r, plan.Target.Branch) {
		t.Fatal("advanced branch was removed")
	}
	workspace, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || workspace.State != "destroyed" || workspace.Branch != plan.Target.Branch || workspace.Path != plan.Path {
		t.Fatal("branch cleanup discarded tombstone evidence")
	}
}

func TestGCReconcilesRawDirectoryLossWithRetainedGitMetadata(t *testing.T) {
	r, plan := branchFixture(t, "raw-worktree-loss", "")
	if err := os.RemoveAll(plan.Path); err != nil {
		t.Fatal(err)
	}
	workspace, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := r.store.LockWorkspace(plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	destroyStep, err := lock.DestroyStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	admin := destroyStep.Identity.AdminDir
	if _, err := os.Lstat(admin); err != nil {
		t.Fatal("raw directory loss also removed Git metadata; test setup invalid")

	}
	if _, err := ApplyGC(t.Context(), r.store, r.client, plan.WorkspaceID, GCOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(admin); !os.IsNotExist(err) {
		t.Fatal("retained Git metadata was not exactly removed")
	}
	if branchExists(t, r, plan.Target.Branch) {
		t.Fatal("raw-loss GC retained EVE-created branch metadata")
	}
	workspace, err = r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || workspace.State != "destroyed" {
		t.Fatalf("raw-loss GC did not finish: %+v %v", workspace, err)
	}
}
