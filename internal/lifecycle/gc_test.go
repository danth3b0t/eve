package lifecycle

import (
	"os"
	"testing"
)

func TestGCCompletesOnlyExactLocalOrphan(t *testing.T) {
	r, p, w := preparedFixture(t)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "worktree", "remove", "--force", "--", p.Path)
	command(t, r.root, "worktree", "prune")
	result, err := ApplyGC(t.Context(), r.store, r.client, p.WorkspaceID, GCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Workspace.State != "destroyed" {
		t.Fatal("GC did not complete exact orphan cleanup")
	}
	_, err = ApplyGC(t.Context(), r.store, r.client, p.WorkspaceID, GCOptions{})
	errorCode(t, err, "E_GC_NOT_GARBAGE")
}
func TestGCRefusesExistingOrRecreatedPath(t *testing.T) {
	r, p, w := preparedFixture(t)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "worktree", "remove", "--force", "--", p.Path)
	command(t, r.root, "worktree", "prune")
	if err := os.Mkdir(p.Path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyGC(t.Context(), r.store, r.client, p.WorkspaceID, GCOptions{})
	errorCode(t, err, "E_GC_NOT_GARBAGE")
	workspace, err := r.store.Workspace(t.Context(), p.WorkspaceID)
	if err != nil || workspace.State != "prepared" {
		t.Fatal("recreated path was finalized")
	}
}
