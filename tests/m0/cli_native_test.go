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
	Services map[string]struct {
		Port int    `json:"port"`
		URL  string `json:"url"`
	} `json:"services"`
	Resources map[string]struct {
		Provider  string `json:"provider"`
		Name      string `json:"name"`
		URL       string `json:"url"`
		SiteURL   string `json:"site_url"`
		ExpiresAt string `json:"expires_at"`
	} `json:"resources"`
	Doctor struct {
		Checks []struct {
			ID, Status, Evidence string
		} `json:"checks"`
	} `json:"doctor"`
	Error struct{ Code, Message string } `json:"error,omitempty"`
}

func runEVE(t *testing.T, f fixture, binary string, args ...string) eveResult {
	return runEVEWithEnv(t, f, binary, nil, args...)
}
func runEVEWithEnv(t *testing.T, f fixture, binary string, extra []string, args ...string) eveResult {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = f.root
	env := append(append([]string{}, f.env...), "EVE_STATE_DIR="+filepath.Join(filepath.Dir(f.root), "eve-state"))
	cmd.Env = append(env, extra...)
	data, err := cmd.CombinedOutput()
	if strings.Contains(string(data), "CONVEX_DEPLOY_KEY") {
		t.Fatal("CLI output exposed a reserved selector")
	}
	if err != nil {
		t.Fatalf("eve %v: %v\n%s", args, err, data)
	}
	var result eveResult
	if json.Unmarshal(data, &result) != nil || result.SchemaVersion != 1 || !result.OK {
		t.Fatalf("invalid EVE result")
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
		if os.Getenv("EVE_M0_BROWSER") == "1" {
			for index, app := range []string{"web", "admin"} {
				session := created.Workspace.Branch + "-" + app
				url := fmt.Sprintf("http://127.0.0.1:%d", v.ports[index])
				deadline := time.Now().Add(30 * time.Second)
				for {
					got, ok := browserConfigJSON(t, session, url)
					if ok && got["url"] == v.url && got["siteUrl"] == v.siteURL {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("CLI browser %s saw %v", session, got)
					}
					time.Sleep(250 * time.Millisecond)
				}
			}
		}
		worktrees = append(worktrees, wt)
		values = append(values, v)
		processes = append(processes, p)
	}
	processes[0].stop()
	waitPortsReleased(t, values[0].ports)
	destroyed := runEVE(t, f, binary, "destroy", "--yes", "--json", "cli-a")
	if destroyed.Workspace.State != "destroyed" {
		t.Fatal("CLI destroy did not finish")
	}
	if _, err := os.Lstat(worktrees[0].root); !os.IsNotExist(err) {
		t.Fatal("destroyed worktree remains")
	}
	assertFrontends(t, processes[1], values[1])
	processes[1].stop()
	if command := f.run(t, "git", "branch", "--list", "cli-a"); command != "" {
		t.Fatal("destroy retained a still-identical EVE-created branch")
	}
	f.unchanged(t)
	worktrees[1].unchanged(t)
}

func waitPortsReleased(t *testing.T, values [2]int) {
	t.Helper()
	for _, port := range values {
		deadline := time.Now().Add(75 * time.Second)
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
}
