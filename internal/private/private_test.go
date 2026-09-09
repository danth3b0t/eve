package private

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/platform"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func objectsFixture(t *testing.T) (*Objects, string) {
	t.Helper()
	dir, err := platform.PrivateDir(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { objects.Close() })
	return objects, dir
}
func TestImmutablePrivateObjects(t *testing.T) {
	objects, dir := objectsFixture(t)
	ref := uuid.NewString()
	content := []byte("private-image-canary")
	if err := objects.Create(t.Context(), ref, content); err != nil {
		t.Fatal(err)
	}
	data, err := objects.Read(t.Context(), ref, int64(len(content)))
	if err != nil || !bytes.Equal(data, content) {
		t.Fatalf("round trip failed: %v", err)
	}
	if err := objects.Create(t.Context(), ref, []byte("replacement")); err == nil {
		t.Fatal("overwrote immutable image")
	}
	if _, err := objects.Read(t.Context(), ref, 1); err == nil {
		t.Fatal("ignored read bound")
	}
	info, err := os.Stat(filepath.Join(dir, ref))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private mode not enforced")
	}
	for _, bad := range []string{"../escape", "not-a-reference", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", uuid.NewString() + "/child"} {
		if err := objects.Create(t.Context(), bad, content); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	unused := uuid.NewString()
	if err := objects.Create(ctx, unused, content); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, unused)); !os.IsNotExist(err) {
		t.Fatal("cancelled write created object")
	}
	if _, err := objects.Read(ctx, ref, MaxBytes); err != context.Canceled {
		t.Fatalf("cancelled read: %v", err)
	}
}
func TestUnsafeObjectsAndReplacedStore(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "directory", "broad-mode"} {
		t.Run(kind, func(t *testing.T) {
			objects, dir := objectsFixture(t)
			ref := uuid.NewString()
			name := filepath.Join(dir, ref)
			target := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(target, []byte("outside-canary"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, name)
			case "hardlink":
				err = os.Link(target, name)
			case "fifo":
				err = unix.Mkfifo(name, 0600)
			case "directory":
				err = os.Mkdir(name, 0700)
			case "broad-mode":
				err = os.WriteFile(name, []byte("inside"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := objects.Read(t.Context(), ref, MaxBytes); err == nil {
				t.Fatal("unsafe object read")
			}
			if err := objects.Create(t.Context(), ref, []byte("new")); err == nil {
				t.Fatal("unsafe object replaced")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "outside-canary" {
				t.Fatal("outside file changed")
			}
		})
	}
	objects, dir := objectsFixture(t)
	if err := os.Rename(dir, dir+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := objects.Create(t.Context(), uuid.NewString(), nil); err == nil {
		t.Fatal("replaced store accepted")
	}
	entries, err := os.ReadDir(dir + "-moved")
	if err != nil || len(entries) != 0 {
		t.Fatal("wrote through detached root")
	}
}
func TestHMACDomainsAndPrivateFormatting(t *testing.T) {
	key, err := LoadKey(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	// Independent fixed vector: Python hmac/sha256 with big-endian uint64 length
	// prefixes over [eve-hmac-v1,file,workspace,env,"",PORT=1234\n].
	want := "0c76f2f655aefad9d24774e38ba00b955cc884b4d94396c24191a8c447a81229"
	got := key.File("workspace", "env", []byte("PORT=1234\n"))
	if got != want {
		t.Fatalf("fingerprint vector: %s", got)
	}
	for _, different := range []string{key.File("other", "env", []byte("PORT=1234\n")), key.File("workspace", "other", []byte("PORT=1234\n")), key.File("workspace", "env", []byte("PORT=1235\n")), key.Value("workspace", "env", "PORT", "1234")} {
		if Equal(got, different) {
			t.Fatal("fingerprint domain/content collision")
		}
	}
	if key.Value("w", "ab", "c", "d") == key.Value("w", "a", "bc", "d") {
		t.Fatal("ambiguous domain framing")
	}
	for _, bad := range []string{"", "xx", got[:62]} {
		if Equal(bad, bad) {
			t.Fatal("invalid MAC accepted")
		}
	}
	data, _ := json.Marshal(key)
	if string(data) != "{}" || strings.Contains(fmt.Sprintf("%v %#v", key, key), strings.Repeat("42", 32)) {
		t.Fatal("key leaked via formatting")
	}
}
