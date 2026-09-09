//go:build linux || darwin

package m0

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertNativeBackend(t *testing.T, p *running, v nativeValues) {
	t.Helper()
	waitFor(t, p, func() bool {
		var result struct {
			Status string `json:"status"`
			Value  struct {
				Site string `json:"siteUrl"`
			} `json:"value"`
		}
		_, err := newCloudAPI("").request(context.Background(), "POST", v.url+"/api/query", "", map[string]any{"path": "probe:connection", "args": map[string]any{}, "format": "json"}, &result)
		return err == nil && result.Status == "success" && result.Value.Site == fmt.Sprintf("http://localhost:%d", v.ports[0])
	})
}
func verifiedProject(t *testing.T, api cloudAPI, binding string) (project, []deployment) {
	team, slug, ok := strings.Cut(binding, ":")
	if !ok || !providerName.MatchString(team) || !providerName.MatchString(slug) {
		t.Fatal("invalid project binding")
	}
	var project project
	_, err := api.management("GET", "/teams/"+team+"/projects/"+slug, nil, &project)
	must(t, err)
	var token struct {
		Type   string `json:"type"`
		TeamID int64  `json:"teamId"`
	}
	_, err = api.management("GET", "/token_details", nil, &token)
	must(t, err)
	if project.ID <= 0 || project.TeamSlug != team || project.Slug != slug || token.Type != "teamToken" || token.TeamID != project.TeamID || project.Dev == "" {
		t.Fatal("wrong project/token identity")
	}
	names := map[string]string{"dev": project.Dev}
	if project.Prod != "" {
		names["prod"] = project.Prod
	}
	var defaults []deployment
	for kind, name := range names {
		var d deployment
		_, err = api.management("GET", "/deployments/"+name, nil, &d)
		must(t, err)
		if d.ID <= 0 || d.Name != name || d.ProjectID != project.ID || d.Kind != "cloud" || d.Type != kind || d.Default == nil || !*d.Default {
			t.Fatal("default identity mismatch")
		}
		defaults = append(defaults, d)
	}
	return project, defaults
}
func verifyDefaults(t *testing.T, api cloudAPI, want project, defaults []deployment) {
	got, _ := verifiedProject(t, api, want.TeamSlug+":"+want.Slug)
	if got != want || len(defaults) == 0 {
		t.Fatal("project/default identity changed")
	}
	for _, expected := range defaults {
		var d deployment
		_, err := api.management("GET", "/deployments/"+expected.Name, nil, &d)
		must(t, err)
		if d.ID != expected.ID || d.Name != expected.Name || d.ProjectID != expected.ProjectID || d.Type != expected.Type || d.Default == nil || !*d.Default {
			t.Fatal("default deployment identity changed")
		}
	}
}
func TestLiveCLICConvexLifecycle(t *testing.T) {
	if os.Getenv("EVE_M0_LIVE") != "1" {
		t.Skip("set EVE_M0_LIVE=1; creates TWO disposable cloud dev deployments and deletes exactly those deployments")
	}
	token := os.Getenv("EVE_CONVEX_TOKEN")
	projectBinding := os.Getenv("EVE_M0_PROJECT")
	if token == "" || projectBinding == "" {
		t.Fatal("dedicated development token and project binding required")
	}
	api := newCloudAPI(token)
	before, defaults := verifiedProject(t, api, projectBinding)
	f := newFixture(t)
	binary := filepath.Join(filepath.Dir(f.root), "eve")
	build := exec.Command("go", "build", "-o", binary, "./cmd/eve")
	build.Dir = filepath.Join("..", "..")
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, data)
	}
	manifest := fmt.Sprintf(`version = 1
[workspace]
port_block_size = 4
[resources.backend]
provider = 'convex'
path = 'packages/backend'
project = '%s'
[resources.backend.env]
SITE_URL = '${services.web.url}'
[services.web]
path = 'apps/web'
env_file = '.env.local'
port = 'PORT'
[services.web.env]
VITE_CONVEX_URL = '${resources.backend.url}'
VITE_CONVEX_SITE_URL = '${resources.backend.site_url}'
[services.admin]
path = 'apps/admin'
env_file = '.env.local'
port = 'PORT'
[services.admin.env]
VITE_CONVEX_URL = '${resources.backend.url}'
VITE_CONVEX_SITE_URL = '${resources.backend.site_url}'
`, projectBinding)
	f.write(t, "eve.toml", manifest)
	f.run(t, "git", "add", "eve.toml")
	f.run(t, "git", "commit", "-m", "Declare production cloud contracts")
	extra := []string{"EVE_CONVEX_TOKEN=" + token}
	names := []string{"cloud-live-a", "cloud-live-b"}
	created := make(map[string]eveResult)
	processes := map[string]*running{}
	defer func() {
		for _, branch := range names {
			if _, known := created[branch]; known {
				cmd := exec.Command(binary, "destroy", "--yes", "--json", branch)
				cmd.Dir = f.root
				cmd.Env = append(append([]string{}, f.env...), "EVE_STATE_DIR="+filepath.Join(filepath.Dir(f.root), "eve-state"), extra[0])
				data, err := cmd.CombinedOutput()
				if err != nil {
					t.Errorf("exact CLI destroy %s failed: %v", branch, err)
					continue
				}
				var result eveResult
				if json.Unmarshal(data, &result) != nil || !result.OK || result.Workspace.State != "destroyed" {
					t.Errorf("destroy %s did not complete: %s", branch, data)
				}
			}
		}
	}()
	for _, branch := range names {
		result := runEVEWithEnv(t, f, binary, extra, "create", "--yes", "--json", branch)
		created[branch] = result
		backend, exists := result.Resources["backend"]
		if !exists || backend.Name == "" || !convexOrigin.MatchString(backend.URL) || backend.ExpiresAt == "" {
			t.Fatalf("bad resource output: %s", result.Resources)
		}
		expires, err := time.Parse(time.RFC3339, backend.ExpiresAt)
		if err != nil || expires.Before(time.Now().Add(23*time.Hour)) {
			t.Fatal("finite expiry missing")
		}
		wt := fixture{root: result.Workspace.Path, env: f.env}
		wt.install(t)
		wt.unchanged(t)
		v := nativeValues{ports: [2]int{result.Services["web"].Port, result.Services["admin"].Port}, url: backend.URL, siteURL: backend.SiteURL}
		process := wt.start(t, "run", "dev")
		assertFrontends(t, process, v)
		assertNativeBackend(t, process, v)
		processes[branch] = process
	}
	if created[names[0]].Resources["backend"].Name == created[names[1]].Resources["backend"].Name {
		t.Fatal("providers did not isolate deployments")
	}
	a := created[names[0]]
	processes[names[0]].stop()
	waitPortsReleased(t, [2]int{a.Services["web"].Port, a.Services["admin"].Port})
	if _, down := os.Stat(a.Workspace.Path); down != nil {
		t.Fatal("stopped worktree moved early")
	}
	destroyed := runEVEWithEnv(t, f, binary, extra, "destroy", "--yes", "--json", names[0])
	if destroyed.Workspace.State != "destroyed" {
		t.Fatal("remote/local destroy incomplete")
	}
	delete(created, names[0])
	if _, err := os.Lstat(a.Workspace.Path); !os.IsNotExist(err) {
		t.Fatal("destroyed worktree still exists")
	}
	b := created[names[1]]
	bv := nativeValues{ports: [2]int{b.Services["web"].Port, b.Services["admin"].Port}, url: b.Resources["backend"].URL, siteURL: b.Resources["backend"].SiteURL}
	assertFrontends(t, processes[names[1]], bv)
	assertNativeBackend(t, processes[names[1]], bv)
	processes[names[1]].stop()
	waitPortsReleased(t, bv.ports)
	destroyedB := runEVEWithEnv(t, f, binary, extra, "destroy", "--yes", "--json", names[1])
	if destroyedB.Workspace.State != "destroyed" {
		t.Fatal("second destroy incomplete")
	}
	delete(created, names[1])
	verifyDefaults(t, api, before, defaults)
}
