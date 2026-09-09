package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type envelope struct {
	OK        bool
	Workspace struct {
		ID, Branch, Path, State, Phase string
		Generation                     int
	}
	Error    struct{ Code, Message string }
	Services map[string]struct {
		Port int
		URL  string
	}
}

func fixture(t *testing.T) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "source")
	if err := os.MkdirAll(filepath.Join(root, "env"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitignore": "env/generated.env\n",
		"eve.toml":   "version = 1\n[workspace]\nport_block_size = 4\n[services.web]\npath = \"env\"\nenv_file = \"generated.env\"\nport = \"PORT\"\n[services.web.env]\nPUBLIC_NAME = \"cli-native\"\n",
		"env/.keep":  "\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.name", "CLI")
	runGit(t, root, "config", "user.email", "cli@example.invalid")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "baseline")
	return base, root
}
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(data))
}
func command(t *testing.T, binary, root, base string, args ...string) (int, []byte, []byte, envelope) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = root
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(base, "home"), "XDG_CONFIG_HOME=" + filepath.Join(base, "config"), "XDG_STATE_HOME=" + filepath.Join(base, "xdg"), "EVE_STATE_DIR=" + filepath.Join(base, "state"), "GIT_CONFIG_NOSYSTEM=1"}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var parsed envelope
	if len(stdout.Bytes()) > 0 && stdout.Bytes()[0] == '{' {
		if json.Unmarshal(stdout.Bytes(), &parsed) != nil {
			t.Fatalf("invalid JSON stdout:\n%s", stdout.String())
		}
	}
	return code, stdout.Bytes(), stderr.Bytes(), parsed
}
func TestCreatePathStatusDestroyLifecycle(t *testing.T) {
	base, root := fixture(t)
	binary := filepath.Join(base, "eve")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, data)
	}
	code, _, stderr, _ := command(t, binary, root, base, "create", "payments")
	if code != 3 || !strings.Contains(string(stderr), "E_APPROVAL_REQUIRED") {
		t.Fatalf("unapproved create code=%d stderr=%s", code, stderr)
	}
	entries, err := os.ReadDir(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == ".eve-worktrees" {
			t.Fatal("unapproved create made worktree parent")
		}
	}
	code, stdout, _, created := command(t, binary, root, base, "create", "--yes", "--json", "payments")
	if code != 0 || !created.OK || created.Workspace.State != "prepared" || created.Workspace.Generation != 1 {
		t.Fatalf("create: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(string(stdout), `"workspace":{"id"`) || !strings.Contains(string(stdout), `"generation":1`) {
		t.Fatalf("JSON contract drift: %s", stdout)
	}
	path := created.Workspace.Path
	data, err := os.ReadFile(filepath.Join(path, "env", "generated.env"))
	if err != nil || !strings.Contains(string(data), "PORT=") || !strings.Contains(string(data), "PUBLIC_NAME=cli-native") {
		t.Fatal("native image not published")
	}
	if info, err := os.Stat(filepath.Join(path, "env", "generated.env")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("CLI published broad mode")
	}
	if runGit(t, root, "status", "--porcelain", "--untracked-files=all") != "" {
		t.Fatal("source checkout changed")
	}
	code, stdout, _, _ = command(t, binary, root, base, "path", "payments")
	if code != 0 || strings.TrimSpace(string(stdout)) != path {
		t.Fatalf("path: %d %s", code, stdout)
	}
	code, stdout, _, status := command(t, binary, root, base, "status", "--json", "payments")
	if code != 0 || !status.OK || status.Services["web"].Port == 0 || status.Workspace.State != "prepared" {
		t.Fatalf("status: %d %s", code, stdout)
	}
	code, _, stderr, _ = command(t, binary, root, base, "destroy", path)
	if code != 3 || !strings.Contains(string(stderr), "E_APPROVAL_REQUIRED") {
		t.Fatalf("unapproved destroy: %d %s", code, stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("unapproved destroy removed worktree")
	}
	if err := os.WriteFile(filepath.Join(path, "user-file"), []byte("user work"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr, _ = command(t, binary, root, base, "destroy", "--yes", path)
	if code != 3 || !strings.Contains(string(stderr), "E_WORKTREE_DIRTY") {
		t.Fatalf("dirty destroy: %d %s", code, stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("dirty destroy removed worktree")
	}
	code, stdout, _, destroyed := command(t, binary, root, base, "destroy", "--yes", "--discard-changes", "--json", path)
	if code != 0 || !destroyed.OK || destroyed.Workspace.State != "destroyed" {
		t.Fatalf("destroy: %d %s", code, stdout)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("destroyed worktree remains")
	}
	if runGit(t, root, "branch", "--list", "payments") == "" {
		t.Fatal("destroy removed the Git branch")
	}
	code, _, _, _ = command(t, binary, root, base, "path", "payments")
	if code != 3 {
		t.Fatalf("destroyed path code=%d", code)
	}
	if out := runGit(t, root, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("source changed after lifecycle: %s", out)
	}
}
