package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
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

func TestGCRemovesBranchAfterManualWorktreeLossAndRecoversTipMismatch(t *testing.T) {
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
	if strings.TrimSpace(command(t, r.root, "update-ref", "refs/heads/"+plan.Target.Branch, advanced)) == "" {
		// update-ref is intentionally quiet.
	}
	command(t, r.root, "branch", "-D", "tip-probe")

	if _, err := ApplyGC(t.Context(), r.store, r.client, plan.WorkspaceID, GCOptions{}); err == nil || !strings.Contains(err.Error(), "E_GIT_REFERENCE_CHANGED") {
		t.Fatalf("changed branch accepted for metadata cleanup: %v", err)
	}
	if !branchExists(t, r, plan.Target.Branch) {
		t.Fatal("changed branch was removed")
	}
	workspace, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || workspace.State != "destroying" {
		t.Fatal("denied cleanup lost the durable operation")
	}

	command(t, r.root, "update-ref", "refs/heads/"+plan.Target.Branch, plan.Target.HeadOID)
	if _, err := ApplyGC(t.Context(), r.store, r.client, plan.WorkspaceID, GCOptions{}); err != nil {
		t.Fatal(err)
	}
	if branchExists(t, r, plan.Target.Branch) {
		t.Fatal("stale EVE-created branch was not pruned")
	}
	workspace, err = r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || workspace.State != "destroyed" || workspace.Branch != plan.Target.Branch || workspace.Path != plan.Path {
		t.Fatal("branch cleanup discarded tombstone evidence")
	}
}
