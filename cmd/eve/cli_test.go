package main

import (
	"bytes"
	"encoding/json"
	"eve/internal/config"
	"eve/internal/git"
	"eve/internal/lifecycle"
	"eve/internal/ports"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type envelope struct {
	OK        bool
	Workspace struct {
		ID, Branch, Path, State, Phase string
		Generation                     int
	}
	Existing        bool
	RestartRequired bool `json:"restart_required"`
	Error           struct{ Code, Message string }
	Services        map[string]struct {
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
	configDir := filepath.Join(base, "config", "eve")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("version = 1\n[ports]\nmin = 39400\nmax = 39500\n"), 0600); err != nil {
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
	planCode, planOut, _, _ := command(t, binary, root, base, "plan", "--json", "payments")
	if planCode != 0 || !strings.Contains(string(planOut), `"branch":"payments"`) || !strings.Contains(string(planOut), `"head_oid"`) || !strings.Contains(string(planOut), `"files":{"files"`) {
		t.Fatalf("plan output: %d %s", planCode, planOut)
	}
	if info, err := os.Lstat(filepath.Join(base, "state", "state.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("plan created state: %v", info)
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
	repeatCode, repeatOut, _, repeat := command(t, binary, root, base, "create", "--json", "payments")
	if repeatCode != 0 || !repeat.OK || !repeat.Existing || repeat.Workspace.ID != created.Workspace.ID || repeat.Workspace.Path != path {
		t.Fatalf("repeat create was not exact: code=%d out=%s", repeatCode, repeatOut)
	}
	conflictCode, conflictOut, _, conflict := command(t, binary, root, base, "create", "--json", "--from", "main", "payments")
	if conflictCode != 3 || conflict.Error.Code != "E_CREATE_EXISTS" {
		t.Fatalf("retargeting repeat unexpectedly allowed: %d %s", conflictCode, conflictOut)
	}
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
	listCode, listOut, _, _ := command(t, binary, root, base, "list", "--json")
	if listCode != 0 || !strings.Contains(string(listOut), `"repositories"`) || !strings.Contains(string(listOut), created.Workspace.ID) || !strings.Contains(string(listOut), `"port":`+strconv.Itoa(created.Services["web"].Port)) {
		t.Fatalf("list output incomplete: code=%d out=%s", listCode, listOut)
	}
	allCode, allOut, _, _ := command(t, binary, root, base, "list", "--json", "--all")
	if allCode != 0 || !strings.Contains(string(allOut), `"label":"source"`) || !strings.Contains(string(allOut), path) {
		t.Fatalf("list --all output incomplete: %d %s", allCode, allOut)
	}
	syncManifest := "version = 1\n[workspace]\nport_block_size = 4\n[services.web]\npath = \"env\"\nenv_file = \"generated.env\"\nport = \"PORT\"\n[services.web.env]\nPUBLIC_NAME = \"synced-native\"\nSYNC_ADD = \"${workspace.id}\"\n"
	if err := os.WriteFile(filepath.Join(path, "eve.toml"), []byte(syncManifest), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "add", "eve.toml")
	runGit(t, path, "commit", "-qm", "sync declaration")
	syncCode, syncOut, _, synced := command(t, binary, root, base, "sync", "--json", "payments")
	if syncCode != 0 || !synced.OK || synced.Workspace.Generation != 2 || !synced.RestartRequired {
		t.Fatalf("sync: %d %s", syncCode, syncOut)
	}
	syncedData, err := os.ReadFile(filepath.Join(path, "env", "generated.env"))
	if err != nil || !strings.Contains(string(syncedData), "PUBLIC_NAME=synced-native\n") || !strings.Contains(string(syncedData), "SYNC_ADD="+created.Workspace.ID+"\n") {
		t.Fatal("synced image missing")
	}
	repeatSyncCode, repeatSyncOut, _, repeatSync := command(t, binary, root, base, "sync", "--json", "payments")
	if repeatSyncCode != 0 || repeatSync.RestartRequired || repeatSync.Workspace.Generation != 2 {
		t.Fatalf("no-change sync was not idempotent: %d %s", repeatSyncCode, repeatSyncOut)
	}
	code, stdout, _, _ = command(t, binary, root, base, "path", "payments")
	if code != 0 || strings.TrimSpace(string(stdout)) != path {
		t.Fatalf("path: %d %s", code, stdout)
	}
	code, stdout, _, status := command(t, binary, root, base, "status", "--json", "payments")
	if code != 0 || !status.OK || status.Services["web"].Port == 0 || status.Workspace.State != "prepared" {
		t.Fatalf("status: %d %s", code, stdout)
	}
	doctorCode, doctorOut, _, doctor := command(t, binary, root, base, "doctor", "--json", "payments")
	if doctorCode != 0 || !doctor.OK || !strings.Contains(string(doctorOut), `"status":"pass"`) || !strings.Contains(string(doctorOut), `"status":"not_checked"`) {
		t.Fatalf("doctor: %d %s", doctorCode, doctorOut)
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
	doctorCode, doctorOut, _, doctor = command(t, binary, root, base, "doctor", "--json", "payments")
	if doctorCode != 0 || !strings.Contains(string(doctorOut), `"git","status":"warning"`) {
		t.Fatalf("doctor did not report user change: %d %s", doctorCode, doctorOut)
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
	emptyCode, emptyOut, _, _ := command(t, binary, root, base, "list", "--json")
	if emptyCode != 0 || strings.Contains(string(emptyOut), path) || !strings.Contains(string(emptyOut), `"workspaces":[]`) {
		t.Fatalf("destroyed workspace remained in list: %d %s", emptyCode, emptyOut)
	}
	if out := runGit(t, root, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("source changed after lifecycle: %s", out)
	}
}

func TestResumeCLICompletesInterruptedCreate(t *testing.T) {
	base, root := fixture(t)
	binary := filepath.Join(base, "eve")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, data)
	}
	g, err := git.New()
	if err != nil {
		t.Fatal(err)
	}
	s, err := lifecycle.OpenForGit(t.Context(), g, root, filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	if _, err := lifecycle.RegisterSource(t.Context(), s, g, root); err != nil {
		t.Fatal(err)
	}
	plan, err := lifecycle.PlanGit(t.Context(), s, g, root, "resume-cli", "")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.LockWorkspace(plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	user, err := config.ParseUser([]byte("version = 1\n[ports]\nmin = 39400\nmax = 39500\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.BeginCreate(t.Context(), plan.Intent(user)); err != nil {
		t.Fatal(err)
	}
	if _, err := ports.Reserve(t.Context(), lock, nil); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	blockedCode, blockedOut, _, blocked := command(t, binary, root, base, "create", "--json", "resume-cli")
	if blockedCode != 3 || blocked.Error.Code != "E_RESUME_REQUIRED" {
		t.Fatalf("incomplete create selected replacement: %d %s", blockedCode, blockedOut)
	}
	code, stdout, _, resumed := command(t, binary, root, base, "resume", "--json", "resume-cli")
	if code != 0 || !resumed.OK || resumed.Workspace.State != "prepared" || resumed.Workspace.Generation != 1 {
		t.Fatalf("resume failed: code=%d stdout=%s", code, stdout)
	}
	if resumed.Workspace.ID != plan.WorkspaceID || resumed.Workspace.Path != plan.Path {
		t.Fatal("resume selected a replacement workspace")
	}
	if _, err := os.Stat(filepath.Join(plan.Path, "env", "generated.env")); err != nil {
		t.Fatal("resume did not publish native image")
	}
	code, stdout, _, again := command(t, binary, root, base, "resume", "--json", "resume-cli")
	if code != 0 || !again.OK || again.Workspace.State != "prepared" {
		t.Fatalf("idempotent resumed failed: %d %s", code, stdout)
	}
}

func TestGCCLIReportsBeforeExactApply(t *testing.T) {
	base, root := fixture(t)
	binary := filepath.Join(base, "eve")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, data)
	}
	code, stdout, _, created := command(t, binary, root, base, "create", "--yes", "--json", "orphan")
	if code != 0 || !created.OK {
		t.Fatalf("create: %d %s", code, stdout)
	}
	path := created.Workspace.Path
	runGit(t, root, "worktree", "remove", "--force", "--", path)
	runGit(t, root, "worktree", "prune")
	code, stdout, _, _ = command(t, binary, root, base, "gc", "--json")
	if code != 0 || !strings.Contains(string(stdout), `"kind":"orphaned_worktree"`) || !strings.Contains(string(stdout), `"eligible":true`) || strings.Contains(string(stdout), `"applied":true`) {
		t.Fatalf("gc report: %d %s", code, stdout)
	}
	code, stdout, _, _ = command(t, binary, root, base, "gc", "--apply", "--json")
	if code != 0 || !strings.Contains(string(stdout), `"applied":true`) || !strings.Contains(string(stdout), `"state":"destroyed"`) {
		t.Fatalf("gc apply: %d %s", code, stdout)
	}
	code, stdout, _, _ = command(t, binary, root, base, "gc", "--json")
	if code != 0 || strings.Contains(string(stdout), path) || strings.Contains(string(stdout), `gc_candidates`) {
		t.Fatalf("gc retained candidate: %d %s", code, stdout)
	}
}
