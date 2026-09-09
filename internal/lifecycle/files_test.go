package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilePreflightRejectsDuplicateOwnedKeyBeforeCreate(t *testing.T) {
	r := repositoryFixture(t)
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, ".env.local"), []byte("PUBLIC_NAME=first\nPUBLIC_NAME=second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := PlanGit(t.Context(), r.store, r.client, r.root, "feature", "")
	errorCode(t, err, "E_ENV_DUPLICATE")
	if p.WorkspaceID != "" || p.Files != nil {
		t.Fatal("failed file preflight returned a creation plan")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.root), ".eve-worktrees")); !os.IsNotExist(err) {
		t.Fatal("file preflight created worktree directories")
	}
}
