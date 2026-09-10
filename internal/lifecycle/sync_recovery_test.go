package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cloudSyncSetup struct {
	repository repository
	plan       GitPlan
	managed    chan map[string]string
	factory    convexFactory
}

func cloudSyncFixture(t *testing.T) cloudSyncSetup {
	t.Helper()
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	managed := make(chan map[string]string, 4)
	factory := fakeConvexFactory(t, managed, false, nil, nil)
	bindings, err := provisionResources(t.Context(), r.store, lock, current, factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StageFilesWithBindings(t.Context(), r.store, r.client, lock, plan.Files, &bindings); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	<-managed
	return cloudSyncSetup{repository: r, plan: plan, managed: managed, factory: factory}
}

func cloudManifest(value string) string {
	return `version = 1
[workspace]
port_block_size = 4
[resources.backend]
provider = "convex"
path = "packages/backend"
project = "dev-team:m0"
[resources.backend.env]
SITE_URL = "${services.web.url}"
CUSTOM_SYNC = "` + value + `"
[services.web]
path = "apps/web"
env_file = ".env.local"
port = "PORT"
[services.web.env]
VITE_CONVEX_URL = "${resources.backend.url}"
VITE_CONVEX_SITE_URL = "${resources.backend.site_url}"
`
}

func TestRemoteOnlySyncAdvancesAppliedGeneration(t *testing.T) {
	setup := cloudSyncFixture(t)
	plan := setup.plan
	for _, change := range []struct {
		generation int
		value      string
	}{{2, "remote-two"}, {3, "remote-three"}} {
		upgradeManifest(plan, cloudManifest(change.value), t)
		result, err := SyncWorkspace(t.Context(), setup.repository.store, setup.repository.client, plan.WorkspaceID, SyncOptions{ProviderFactory: setup.factory})
		if err != nil || result.Workspace.Generation != change.generation || !result.RestartRequired {
			t.Fatalf("remote-only generation %d failed: %+v %v", change.generation, result.Workspace, err)
		}
		update := <-setup.managed
		if update["CUSTOM_SYNC"] != change.value {
			t.Fatalf("wrong cloud-only update: %v", update)
		}
	}
	repeat, err := SyncWorkspace(t.Context(), setup.repository.store, setup.repository.client, plan.WorkspaceID, SyncOptions{ProviderFactory: setup.factory})
	if err != nil || repeat.Workspace.Generation != 3 || repeat.RestartRequired {
		t.Fatalf("remote no-op created a generation: %+v %v", repeat.Workspace, err)
	}
	if len(setup.managed) != 0 {
		t.Fatalf("remote value was rewritten during no-op: %v", <-setup.managed)
	}
	workspace, err := setup.repository.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || workspace.AppliedManifest.Resources["backend"].Env["CUSTOM_SYNC"] != "remote-three" {
		t.Fatalf("applied generation did not advance: %+v %v", workspace.AppliedManifest, err)
	}
}

func TestResumeDispatchesInterruptedSync(t *testing.T) {
	r, plan := syncLocalFixture(t)
	upgradeManifest(plan, "version = 1\n[services.api]\npath = \".\"\nenv_file = \".env.local\"\n[services.api.env]\nPUBLIC_NAME = \"old\"\nRESUMED_KEY = \"resume\"\n", t)
	lock, err := r.store.LockWorkspace(plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := lock.SyncStep(t.Context())
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	interrupted, err := prepareSyncPlan(t.Context(), r.store, r.client, lock, step, SyncOptions{})
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err := stageSyncPlan(t.Context(), r.store, lock, plan.WorkspaceID, interrupted); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || current.State != "syncing" {
		t.Fatalf("fixture is not interrupted: %+v %v", current, err)
	}
	resumed, err := ResumeWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, ResumeOptions{})
	if err != nil || resumed.State != "prepared" || resumed.Generation != 2 {
		t.Fatalf("sync did not resume exactly: %+v %v", resumed, err)
	}
	data, err := os.ReadFile(filepath.Join(plan.Path, ".env.local"))
	if err != nil || !strings.Contains(string(data), "RESUMED_KEY=resume\n") {
		t.Fatalf("resumed image not published: %v", err)
	}
}

func TestSyncIgnoresCreationCopyOnlyDestination(t *testing.T) {
	r := repositoryFixture(t)
	manifest := `version = 1
[workspace]
copy = ["config.txt"]
[services.api]
path = "."
env_file = ".env.local"
[services.api.env]
LOCAL_VALUE = "one"
`
	for name, data := range map[string]string{".gitignore": "/.env.local\n/config.txt\n", "eve.toml": manifest, "config.txt": "opaque copy\n"} {
		if err := os.WriteFile(filepath.Join(r.root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command(t, r.root, "add", ".gitignore", "eve.toml")
	command(t, r.root, "commit", "-qm", "copy fixture")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "copy-sync", "")
	if err != nil {
		t.Fatal(err)
	}
	lock := approved(t, r, plan)
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if _, err := StageFiles(t.Context(), r.store, r.client, lock, plan.Files); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	upgradeManifest(plan, strings.Replace(manifest, `LOCAL_VALUE = "one"`, `LOCAL_VALUE = "two"`, 1), t)
	result, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{})
	if err != nil || result.Workspace.Generation != 2 {
		t.Fatalf("copy-only destination broke sync: %+v %v", result.Workspace, err)
	}
	data, err := os.ReadFile(filepath.Join(plan.Path, "config.txt"))
	if err != nil || string(data) != "opaque copy\n" {
		t.Fatal("copy-only destination was changed")
	}
}
