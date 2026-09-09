package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPaths(t *testing.T) {
	for _, tc := range []struct {
		os, state, config, override, wantState, wantConfig string
	}{
		{"linux", "", "", "", "/home/user/.local/state/eve", "/home/user/.config/eve/config.toml"},
		{"linux", "/state", "/config", "", "/state/eve", "/config/eve/config.toml"},
		{"darwin", "/ignored", "/ignored", "", "/home/user/Library/Application Support/eve", "/home/user/Library/Application Support/eve/config.toml"},
		{"linux", "", "", "/isolated", "/isolated", "/home/user/.config/eve/config.toml"},
		{"darwin", "", "", "/isolated", "/isolated", "/home/user/Library/Application Support/eve/config.toml"},
	} {
		p, err := pathsFor(tc.os, "/home/user", tc.state, tc.config, tc.override)
		if err != nil || p.State != tc.wantState || p.Config != tc.wantConfig || p.Isolated != (tc.override != "") {
			t.Fatalf("%+v: %+v %v", tc, p, err)
		}
	}
	for _, args := range [][5]string{{"windows", "/home", "", "", ""}, {"linux", "relative", "", "", ""}, {"linux", "/home", "relative", "", ""}, {"linux", "/home", "", "relative", ""}, {"linux", "/home", "", "", "relative"}} {
		if _, err := pathsFor(args[0], args[1], args[2], args[3], args[4]); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestPrivateObjectsAndLocks(t *testing.T) {
	root := t.TempDir()
	private, err := PrivateDir(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(private, "workspace.lock")
	first, err := TryLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := TryLock(lockPath); !errors.Is(err, ErrLocked) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("concurrent lock: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatal("closing a lock must not unlink it", err)
	}
	second, err := TryLock(lockPath)
	if err != nil {
		t.Fatal("stale lock-file existence was mistaken for a held lock", err)
	}
	second.Close()

	secret := filepath.Join(private, "secret")
	if err := os.WriteFile(secret, []byte("private-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"symlink", "hardlink", "fifo", "broad"} {
		t.Run(kind, func(t *testing.T) {
			p := filepath.Join(private, kind)
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(secret, p)
			case "hardlink":
				err = os.Link(secret, p)
			case "fifo":
				err = unix.Mkfifo(p, 0600)
			case "broad":
				err = os.WriteFile(p, nil, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CreatePrivateFile(p); err == nil {
				t.Fatal("accepted unsafe state file")
			}
			if l, err := TryLock(p); err == nil {
				l.Close()
				t.Fatal("accepted unsafe lock")
			}
			os.Remove(p)
		})
	}
	data, err := os.ReadFile(secret)
	if err != nil || string(data) != "private-sentinel" {
		t.Fatal("validation changed the linked source")
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, link + "/."} {
		if _, err := PrivateDir(path); err == nil {
			t.Fatal("accepted a symlink state root")
		}
	}
	if err := os.Chmod(private, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateDir(private); err == nil {
		t.Fatal("silently accepted/repaired broad state permissions")
	}
}
