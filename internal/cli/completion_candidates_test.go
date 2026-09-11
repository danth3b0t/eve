package cli

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eve/internal/platform"
	"eve/internal/state"
	_ "modernc.org/sqlite"
)

type completionFixture struct {
	base, root string
}

func completionWorkspaceFixture(t *testing.T) (completionFixture, map[string]any) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(base, "config", "eve")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("version = 1\n[ports]\nmin = 39000\nmax = 39100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "source")
	if err := os.MkdirAll(filepath.Join(root, "env"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitignore": "env/generated.env\n",
		"eve.toml":   "version = 1\n[workspace]\nport_block_size = 4\n[services.web]\npath = \"env\"\nenv_file = \"generated.env\"\nport = \"PORT\"\n[services.web.env]\nPUBLIC_NAME = \"completion-test\"\n",
		"env/.keep":  "\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGitForCompletion(t, root, "init", "-q", "-b", "main")
	runGitForCompletion(t, root, "config", "user.name", "Completion")
	runGitForCompletion(t, root, "config", "user.email", "completion@example.invalid")
	runGitForCompletion(t, root, "add", ".")
	runGitForCompletion(t, root, "commit", "-qm", "baseline")
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg"))
	t.Setenv("EVE_STATE_DIR", filepath.Join(base, "state"))
	t.Setenv("EVE_ACTIVE_HELP", "0")
	t.Chdir(root)
	workspace := completionFixture{base: base, root: root}
	return workspace, nil
}

func runGitForCompletion(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func setCompletionOperationState(t *testing.T, stateDir, workspaceID, stateName, phase, command, operationState string) {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(stateDir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(t.Context(), `UPDATE workspaces SET state=?,phase=? WHERE id=?`, stateName, phase, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE operations SET command=?,state=? WHERE workspace_id=?`, command, operationState, workspaceID); err != nil {
		t.Fatal(err)
	}
}

func TestDynamicCompletionPoliciesUseRegisteredTree(t *testing.T) {
	fixture, _ := completionWorkspaceFixture(t)
	code, stdout, stderr := runCLIForTest(t, "create", "--yes", "--json", "payments")
	if code != 0 {
		t.Fatalf("create: %d %s %s", code, stdout, stderr)
	}
	var created struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil || created.Workspace.ID == "" {
		t.Fatalf("created workspace identity: %v %s", err, stdout)
	}
	id := created.Workspace.ID
	runGitForCompletion(t, fixture.root, "checkout", "-q", "-b", "publish")
	runGitForCompletion(t, fixture.root, "checkout", "-q", "main")
	runGitForCompletion(t, fixture.root, "tag", "publish")
	runGitForCompletion(t, fixture.root, "update-ref", "refs/remotes/origin/publish", "main")
	if err := os.MkdirAll(filepath.Join(fixture.root, "packages", "backend"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "packages", "backend", "convex.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "packages", "backend", "package.json"), []byte(`{"dependencies":{"convex":"latest"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	runGitForCompletion(t, fixture.root, "add", "packages/backend")
	runGitForCompletion(t, fixture.root, "commit", "-qm", "backend evidence")

	cases := []struct {
		name     string
		args     []string
		want     []string
		rejected []string
	}{
		{"selector", []string{"__complete", "status", ""}, []string{"payments\t", "prepared configuration"}, []string{"state.sqlite"}},
		{"selector no description same insertion", []string{"__completeNoDesc", "status", "pay"}, []string{"payments\n", ":4\n"}, []string{"payments\t"}},
		{"sync continues prepared apply", []string{"__complete", "sync", ""}, []string{"payments\t", "apply configuration"}, []string{}},
		{"resume omits prepared", []string{"__complete", "resume", ""}, []string{":4\n"}, []string{"payments"}},
		{"destroy includes record", []string{"__complete", "destroy", ""}, []string{"payments\t", ":4\n"}, []string{}},
		{"create distinguishes managed target", []string{"__complete", "create", ""}, []string{"payments\t", "prepared configuration"}, []string{"\nmain"}},
		{"from refs include local choices", []string{"__complete", "create", "--from", ""}, []string{"main\t", "HEAD\t"}, []string{}},
		{"ambiguous head tag and cached remote", []string{"__complete", "create", "--from", "pu"}, []string{"refs/heads/publish", "refs/tags/publish"}, []string{"refs/heads/publish\tlocal tag", "origin/publish"}},
		{"cached remote ref", []string{"__complete", "create", "--from", "origin"}, []string{"origin/publish"}, []string{"refs/heads/publish"}},
		{"equals ref", []string{"__complete", "create", "--from="}, []string{"main", "HEAD"}, []string{}},
		{"unrelated from has no ref shortcut", []string{"__complete", "list", "--from", ""}, []string{}, []string{"main\tlocal branch"}},
		{"boolean value only in value form", []string{"__complete", "status", "--json="}, []string{"true", "false"}, []string{"payments"}},
		{"bounded backend path", []string{"__complete", "init", "--backend-path", "pack"}, []string{"packages/backend"}, []string{"node_modules"}},
		{"declared site URL service", []string{"__complete", "init", "--site-url-service", "w"}, []string{"web\t"}, []string{"backend"}},
		{"keys operands", []string{"__complete", "keys", ""}, []string{"--json"}, []string{"workspace.id"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLIForTest(t, tc.args...)
			if code != 0 {
				t.Fatalf("%v: %d %s %s", tc.args, code, stdout, stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("%v missing %q:\n%s stderr=%s", tc.args, want, stdout, stderr)
				}
			}
			for _, rejected := range tc.rejected {
				if strings.Contains(stdout, rejected) {
					t.Fatalf("%v contained rejected %q:\n%s", tc.args, rejected, stdout)
				}
			}
			if strings.Contains(stdout, "schema_version") || strings.Contains(stdout, "eve "+fixture.root) {
				t.Fatalf("protocol polluted: %s", stdout)
			}
		})
	}

	unicodeBranch := "功能/payments"
	code, stdout, stderr = runCLIForTest(t, "create", "--yes", unicodeBranch)
	if code != 0 {
		t.Fatalf("unicode create: %d %s %s", code, stdout, stderr)
	}
	code, stdout, _ = runCLIForTest(t, "__complete", "status", "功能")
	if code != 0 || !strings.HasPrefix(stdout, unicodeBranch+"\t") {
		t.Fatalf("unicode insertion identity was altered: %d %s", code, stdout)
	}
	code, stdout, stderr = runCLIForTest(t, "status", unicodeBranch)
	if code != 0 || !strings.Contains(stdout, unicodeBranch) {
		t.Fatalf("candidate insertion did not resolve: %d %s %s", code, stdout, stderr)
	}

	setCompletionOperationState(t, filepath.Join(fixture.base, "state"), id, "destroying", "git", "destroy", "pending")
	code, stdout, _ = runCLIForTest(t, "__complete", "resume", "pay")
	if code != 0 || !strings.Contains(stdout, "payments\t") || !strings.Contains(stdout, "continue deletion") {
		t.Fatalf("resume deletion continuation: %d %s", code, stdout)
	}
	code, stdout, _ = runCLIForTest(t, "__complete", "sync", "pay")
	if code != 0 || strings.Contains(stdout, "payments\t") {
		t.Fatalf("sync suggested destruction state: %d %s", code, stdout)
	}
	setCompletionOperationState(t, filepath.Join(fixture.base, "state"), id, "syncing", "git", "sync", "pending")
	code, stdout, _ = runCLIForTest(t, "__complete", "sync", "pay")
	if code != 0 || !strings.Contains(stdout, "payments\t") || !strings.Contains(stdout, "continue sync") {
		t.Fatalf("sync continuation: %d %s", code, stdout)
	}

	t.Chdir(fixture.base)
	for attempt := 0; attempt < 5; attempt++ {
		code, stdout, stderr = runCLIForTest(t, "__complete", "gc", "--workspace", id[:8])
		if code == 0 && strings.Contains(stdout, id+"\t") && strings.Contains(stdout, ":4\n") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code != 0 || !strings.Contains(stdout, id+"\t") || !strings.Contains(stdout, ":4\n") {
		t.Fatalf("global GC ID completion: %d %s %s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "payments\t") {
		t.Fatalf("GC emitted branch for --workspace: %s", stdout)
	}
}

func TestProfileCompletionReadsNoTokenObject(t *testing.T) {
	fixture, _ := completionWorkspaceFixture(t)
	paths, err := platform.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(t.Context(), paths.State)
	if err != nil {
		t.Fatal(err)
	}
	secret := "profile-completion-secret"
	if _, err := store.StoreManagementProfile(t.Context(), "convex", "work", "init-devs", 42, secret, time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.base, "outside"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(fixture.base, "outside"))
	for _, args := range [][]string{
		{"__complete", "auth", "convex", "status", "--profile", "wo"},
		{"__complete", "auth", "convex", "status", "--profile=wo"},
		{"__complete", "auth", "convex", "login", "--profile", "wo"},
	} {
		code, stdout, _ := runCLIForTest(t, args...)
		if code != 0 || !strings.Contains(stdout, "work\t") {
			t.Fatalf("%v: %d %s", args, code, stdout)
		}
	}
}
