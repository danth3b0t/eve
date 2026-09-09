//go:build linux || darwin

package m0

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"eve/internal/ports"
)

type eveResult struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
	Workspace     struct {
		ID, Branch, Path, State, Phase string
		Generation                     int
	} `json:"workspace"`
	Error struct{ Code, Message string } `json:"error,omitempty"`
}

func runEVE(t *testing.T, f fixture, binary string, args ...string) eveResult {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = f.root
	cmd.Env = append(append([]string{}, f.env...), "EVE_STATE_DIR="+filepath.Join(filepath.Dir(f.root), "eve-state"))
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("eve %v: %v\n%s", args, err, data)
	}
	var result eveResult
	if json.Unmarshal(data, &result) != nil || result.SchemaVersion != 1 || !result.OK {
		t.Fatalf("invalid EVE result: %s", data)
	}
	if strings.Contains(string(data), "CONVEX_DEPLOY_KEY") {
		t.Fatal("CLI output exposed a reserved selector")
	}
	return result
}
func TestCLILocalLifecycleNativeFrontends(t *testing.T) {
	if os.Getenv("EVE_M0_NATIVE") != "1" {
		t.Skip("set EVE_M0_NATIVE=1; requires pinned native fixture and Go for a one-time CLI build")
	}
	f := newFixture(t)
	binary := filepath.Join(filepath.Dir(f.root), "eve")
	build := exec.Command("go", "build", "-o", binary, "./cmd/eve")
	build.Dir = filepath.Join("..", "..")
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, data)
	}
	manifest := "version = 1\n"
	for _, app := range []string{"web", "admin"} {
		manifest += fmt.Sprintf("[services.%s]\npath = 'apps/%s'\nenv_file = '.env.local'\nport = 'PORT'\n[services.%s.env]\nVITE_CONVEX_URL = 'https://${workspace.slug}.convex.cloud'\nVITE_CONVEX_SITE_URL = 'https://${workspace.slug}.convex.site'\n", app, app, app)
	}
	f.write(t, "eve.toml", manifest)
	f.run(t, "git", "add", "eve.toml")
	f.run(t, "git", "commit", "-m", "Declare native frontend bindings")
	var worktrees []fixture
	var values []nativeValues
	var processes []*running
	for _, branch := range []string{"cli-a", "cli-b"} {
		created := runEVE(t, f, binary, "create", "--yes", "--json", branch)
		if created.Workspace.State != "prepared" || created.Workspace.Generation != 1 {
			t.Fatal("CLI did not prepare generation 1")
		}
		wt := fixture{root: created.Workspace.Path, env: f.env}
		wt.install(t)
		wt.unchanged(t)
		v := nativeValues{url: "https://" + filepath.Base(created.Workspace.Path) + ".convex.cloud", siteURL: "https://" + filepath.Base(created.Workspace.Path) + ".convex.site"}
		for _, app := range []string{"web", "admin"} {
			data, err := os.ReadFile(filepath.Join(wt.root, "apps", app, ".env.local"))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(data), "\n") {
				prefix := "PORT="
				if strings.HasPrefix(line, prefix) {
					port, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
					if err != nil {
						t.Fatal(err)
					}
					if app == "web" {
						v.ports[0] = port
					} else {
						v.ports[1] = port
					}
				}
			}
		}
		p := wt.start(t, "run", "dev", "--filter=@eve-m0/web", "--filter=@eve-m0/admin")
		assertFrontends(t, p, v)
		worktrees = append(worktrees, wt)
		values = append(values, v)
		processes = append(processes, p)
	}
	processes[0].stop()
	for _, port := range values[0].ports {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if err := ports.ProbeTCP(t.Context(), port); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("stopped process left endpoint %d listening or unavailable", port)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	destroyed := runEVE(t, f, binary, "destroy", "--yes", "--json", "cli-a")
	if destroyed.Workspace.State != "destroyed" {
		t.Fatal("CLI destroy did not finish")
	}
	if _, err := os.Lstat(worktrees[0].root); !os.IsNotExist(err) {
		t.Fatal("destroyed worktree remains")
	}
	assertFrontends(t, processes[1], values[1])
	processes[1].stop()
	if command := f.run(t, "git", "branch", "--list", "cli-a"); command == "" {
		t.Fatal("destroy removed its branch")
	}
	f.unchanged(t)
	worktrees[1].unchanged(t)
}
