//go:build linux || darwin

package m0

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/config"
	"eve/internal/git"
	"eve/internal/lifecycle"
	"eve/internal/ports"
)

// Uses production planning/allocation/Git/staging/publication, followed by the
// existing native frontend launcher. Installation/supervision belong to this
// harness, not EVE. This local-only test does not provision a Convex backend.
func TestPublishedLifecycleNativeFrontends(t *testing.T) {
	if os.Getenv("EVE_M0_NATIVE") != "1" {
		t.Skip("set EVE_M0_NATIVE=1; requires pinned native fixture dependencies")
	}
	f := newFixture(t)
	t.Setenv("HOME", strings.TrimPrefix(f.env[1], "HOME="))
	t.Setenv("XDG_CONFIG_HOME", strings.TrimPrefix(f.env[2], "XDG_CONFIG_HOME="))
	manifest := "version = 1\n"
	for _, app := range []string{"web", "admin"} {
		manifest += fmt.Sprintf("[services.%s]\npath = 'apps/%s'\nenv_file = '.env.local'\nport = 'PORT'\n[services.%s.env]\nVITE_CONVEX_URL = 'https://${workspace.slug}.convex.cloud'\nVITE_CONVEX_SITE_URL = 'https://${workspace.slug}.convex.site'\n", app, app, app)
	}
	f.write(t, "eve.toml", manifest)
	f.run(t, "git", "add", "eve.toml")
	f.run(t, "git", "commit", "-m", "Declare existing native frontend bindings")
	g, err := git.New()
	must(t, err)
	s, err := lifecycle.OpenForGit(t.Context(), g, f.root, filepath.Join(filepath.Dir(f.root), "eve-state"))
	must(t, err)
	defer s.Close()
	_, err = lifecycle.RegisterSource(t.Context(), s, g, f.root)
	must(t, err)
	user, err := config.ParseUser([]byte("version = 1\n"))
	must(t, err)
	var worktrees []fixture
	var values []nativeValues
	var processes []*running
	for _, branch := range []string{"published-a", "published-b"} {
		plan, err := lifecycle.PlanGit(t.Context(), s, g, f.root, branch, "")
		must(t, err)
		lock, err := s.LockWorkspace(plan.WorkspaceID)
		must(t, err)
		func() {
			defer lock.Close()
			_, err = lock.BeginCreate(t.Context(), plan.Intent(user))
			must(t, err)
			allocation, e := ports.Reserve(t.Context(), lock, nil)
			must(t, e)
			_, err = lifecycle.PrepareGit(t.Context(), s, g, lock)
			must(t, err)
			_, err = lifecycle.StageFiles(t.Context(), s, g, lock, plan.Files)
			must(t, err)
			prepared, e := lifecycle.PublishFiles(t.Context(), s, g, lock)
			must(t, e)
			if prepared.State != "prepared" || prepared.Generation != 1 {
				t.Fatal("native launch preceded generation completion")
			}
			v := nativeValues{url: "https://" + filepath.Base(plan.Path) + ".convex.cloud", siteURL: "https://" + filepath.Base(plan.Path) + ".convex.site"}
			for _, ep := range allocation.Endpoints {
				if ep.Service == "web" {
					v.ports[0] = ep.Port
				}
				if ep.Service == "admin" {
					v.ports[1] = ep.Port
				}
			}
			values = append(values, v)
		}()
		wt := fixture{root: plan.Path, env: f.env}
		wt.install(t)
		wt.unchanged(t)
		process := wt.start(t, "run", "dev", "--filter=@eve-m0/web", "--filter=@eve-m0/admin")
		assertFrontends(t, process, values[len(values)-1])
		worktrees = append(worktrees, wt)
		processes = append(processes, process)
	}
	if values[0].ports == values[1].ports || values[0].url == values[1].url {
		t.Fatal("published workspaces were not isolated")
	}
	processes[0].stop()
	assertFrontends(t, processes[1], values[1])
	processes[1].stop()
	for _, wt := range worktrees {
		wt.unchanged(t)
	}
	f.unchanged(t)
}
