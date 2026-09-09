package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExistingRegistryOpensReadOnly(t *testing.T) {
	s, r := fixture(t)
	w, _ := begin(t, s, r, "read-only")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	root := s.root
	before := map[string]os.FileInfo{}
	for _, name := range []string{"state.sqlite", "state.sqlite-wal", "state.sqlite-shm"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		before[name] = info
	}
	read, err := OpenReadOnly(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	workspace, err := read.Workspace(t.Context(), w.id)
	if err != nil || workspace.ID != w.id {
		t.Fatal("read-only workspace lookup failed")
	}
	if _, err := read.WorkspaceByBranch(t.Context(), r.ID, "read-only"); err != nil {
		t.Fatal(err)
	}
	_, err = read.LockWorkspace(w.id)
	code(t, err, "E_STATE_READ_ONLY")
	_, _, err = read.HMACKey(t.Context())
	code(t, err, "E_STATE_READ_ONLY")
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	for name, beforeInfo := range before {
		after, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !os.SameFile(after, beforeInfo) || after.Size() != beforeInfo.Size() || after.ModTime() != beforeInfo.ModTime() {
			t.Fatalf("read-only open changed %s", name)
		}
	}
}
func TestReadOnlyRefusesMissingOrNewerRegistry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	_, err := OpenReadOnly(t.Context(), root)
	code(t, err, "E_STATE_PATH")
	if _, ei := os.Lstat(root); !os.IsNotExist(ei) {
		t.Fatal("read-only open created state")
	}
}
