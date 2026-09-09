package private

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestVerifiedRemovalRetainsChangedObjects(t *testing.T) {
	objects, dir := objectsFixture(t)
	ref := uuid.NewString()
	data := []byte("private-snapshot")
	if err := objects.Create(t.Context(), ref, data); err != nil {
		t.Fatal(err)
	}
	if err := objects.RemoveVerified(t.Context(), ref, int64(len(data)), func([]byte) bool { return false }); err == nil {
		t.Fatal("unverified object removed")
	}
	if _, err := os.Stat(filepath.Join(dir, ref)); err != nil {
		t.Fatal("rejected removal lost object")
	}
	if err := objects.RemoveVerified(t.Context(), ref, int64(len(data)), func(got []byte) bool { return bytes.Equal(got, data) }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ref)); !os.IsNotExist(err) {
		t.Fatal("verified object was not removed")
	}
	if err := objects.RemoveVerified(t.Context(), ref, int64(len(data)), func([]byte) bool { return true }); err != nil {
		t.Fatal("lost removal response could not reconcile")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ref)); err != nil {
		t.Fatal(err)
	}
	if err := objects.RemoveVerified(t.Context(), ref, int64(len(data)), func([]byte) bool { return true }); err == nil {
		t.Fatal("linked object removed")
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("outside target changed")
	}
}
