//go:build linux || darwin

package m0

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Deliberately opt out by default: this creates one plan-only cloud dev
// deployment. It verifies expiry preservation without relying on it as deletion.
func TestLiveCLIExpiryPreservesWorktreeAndCleanup(t *testing.T) {
	expiryTTL := os.Getenv("EVE_M0_EXPIRY_TTL")
	if os.Getenv("EVE_M0_LIVE") != "1" || expiryTTL == "" {
		t.Skip("set EVE_M0_LIVE=1 and a short production-safe EVE_M0_EXPIRY_TTL; creates one disposable deployment")
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
[resources.backend]
provider = 'convex'
path = 'packages/backend'
project = '%s'
ttl = '%s'
`, projectBinding, expiryTTL)
	f.write(t, "eve.toml", manifest)
	f.run(t, "git", "add", "eve.toml")
	f.run(t, "git", "commit", "-m", "Declare finite-expiry backend")
	result := runEVEWithEnv(t, f, binary, []string{"EVE_CONVEX_TOKEN=" + token}, "create", "--yes", "--json", "short-lived")
	backend := result.Resources["backend"]
	if result.Workspace.State != "prepared" || backend.Name == "" || backend.ExpiresAt == "" {
		t.Fatal("short-lived workspace was not fully prepared")
	}
	expiry, err := time.Parse(time.RFC3339, backend.ExpiresAt)
	if err != nil {
		t.Fatal("missing provider expiry")
	}
 if time.Until(expiry) <= 0 || time.Until(expiry) > 2*time.Hour {
  t.Fatalf("unexpected expiring deployment evidence: %s", backend.ExpiresAt)
 }
	var exact deployment
	if _, err = api.management("GET", "/deployments/"+backend.Name, nil, &exact); err != nil {
		t.Fatal("recorded deployment does not verify by name")
	}
	team, slug, projectOK := strings.Cut(projectBinding, ":")
	if !projectOK {
		t.Fatal("invalid project binding")
	}
	var projectRef struct {
		ID int64 `json:"id"`
	}
	if _, err := api.management("GET", "/teams/"+team+"/projects/"+slug, nil, &projectRef); err != nil || projectRef.ID != before.ID {
		t.Fatal("project moved before expiry")
	}
	if exact.ProjectID != before.ID || exact.Type != "dev" || exact.Default == nil || *exact.Default {
		t.Fatal("expiring resource is not an exact nondefault dev deployment")
	}
	deadline := expiry.Add(2 * time.Minute)
	for {
		var found deployment
		status, err := api.management("GET", "/deployments/"+backend.Name, nil, &found)
		if status == http.StatusNotFound {
			break
		}
		if err != nil {
			t.Fatalf("provider identity could not be verified during expiry: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("finite deployment was still present after expiry budget; no automatic local action is legitimate")
		}
		time.Sleep(10 * time.Second)
	}
	if _, err := os.Stat(result.Workspace.Path); err != nil {
		t.Fatal("provider expiry destroyed local worktree")
	}
	doctorOutput := runEVEWithEnv(t, f, binary, []string{"EVE_CONVEX_TOKEN=" + token}, "doctor", "--remote", "--json", "short-lived")
	missing := false
	for _, check := range doctorOutput.Doctor.Checks {
		if check.ID == "provider_remote:backend" && check.Status == "warning" {
			missing = true
		}
	}
	if !missing {
		t.Fatal("expired deployment was not reported missing after TTL")
	}
	destroyed := runEVEWithEnv(t, f, binary, []string{"EVE_CONVEX_TOKEN=" + token}, "destroy", "--yes", "--json", "short-lived")
	if destroyed.Workspace.State != "destroyed" {
		t.Fatal("local preparation for expired backend could not be destroyed")
	}
	verifyDefaults(t, api, before, defaults)
}
