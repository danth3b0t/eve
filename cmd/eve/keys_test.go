package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeysReportsCommittedInterpolationInventoryWithoutState(t *testing.T) {
	base, root := fixture(t)
	binary := filepath.Join(base, "eve")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, data)
	}
	code, stdout, _, _ := command(t, binary, root, base, "keys", "--json")
	if code != 0 || !strings.Contains(string(stdout), `${workspace.id}`) || !strings.Contains(string(stdout), `${services.web.port}`) || !strings.Contains(string(stdout), `${services.web.url}`) || strings.Contains(string(stdout), "cli-native") {
		t.Fatalf("keys inventory: %d %s", code, stdout)
	}
	if _, err := filepath.EvalSymlinks(filepath.Join(base, "state")); !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("keys created EVE state: %v", err)
	}
}
