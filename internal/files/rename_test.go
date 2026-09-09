//go:build linux || darwin

package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveRenameNeverReplacesConcurrentDestination(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{"temporary": "eve image", "destination": "concurrent user file"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := renameExclusive(root, "temporary", "destination"); err == nil {
		t.Fatal("exclusive rename replaced destination")
	}
	data, err := os.ReadFile(filepath.Join(dir, "destination"))
	if err != nil || string(data) != "concurrent user file" {
		t.Fatal("concurrent destination changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "temporary")); err != nil {
		t.Fatal("failed rename discarded temporary")
	}
}
