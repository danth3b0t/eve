package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func syncLocalFixture(t *testing.T) (repository, GitPlan) {
	t.Helper()
	r := repositoryFixture(t)
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte("/.env.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "ignore local env")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "sync-values", "")
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
	return r, plan
}
func upgradeManifest(plan GitPlan, text string, t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(plan.Path, "eve.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, plan.Path, "add", "eve.toml")
	command(t, plan.Path, "commit", "-qm", "sync values")
}
func TestSyncAdditiveValuesPreservesUnmanagedContentAndAdvancesGeneration(t *testing.T) {
	r, plan := syncLocalFixture(t)
	envPath := filepath.Join(plan.Path, ".env.local")
	if err := os.WriteFile(envPath, []byte("# unmanaged comment\nUSER_VALUE=kept\nPUBLIC_NAME=old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	upgradeManifest(plan, "version = 1\n[services.api]\npath = \".\"\nenv_file = \".env.local\"\n[services.api.env]\nPUBLIC_NAME = \"new\"\nADDITIVE_KEY = \"${workspace.id}\"\n", t)
	result, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RestartRequired || result.Workspace.Generation != 2 {
		t.Fatalf("sync did not complete generation 2: %+v", result.Workspace)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"# unmanaged comment\nUSER_VALUE=kept\nPUBLIC_NAME=new\nADDITIVE_KEY=" + plan.WorkspaceID + "\n"} {
		if !strings.Contains(string(data), required) {
			t.Fatalf("synced content lost boundary:\n%s", data)
		}
	}
	third, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if third.RestartRequired || third.Workspace.Generation != 2 {
		t.Fatal("no-change sync created another generation")
	}
}

func TestSyncAddsEndpointWithoutReassigningExistingSlots(t *testing.T) {
	r := repositoryFixture(t)
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte("/.env.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	text := "version = 1\n[workspace]\nport_block_size = 4\n[services.api]\npath = \".\"\nenv_file = \".env.local\"\nport = \"PORT\"\n[services.api.env]\nPUBLIC_NAME = \"old\"\n"
	if err := os.WriteFile(filepath.Join(r.root, "eve.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "endpoint sync baseline")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "sync-endpoint", "")
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
	initial, err := r.store.Allocation(t.Context(), plan.WorkspaceID)
	if err != nil || len(initial.Endpoints) != 1 {
		t.Fatal("initial allocation missing")
	}
	upgraded := "version = 1\n[workspace]\nport_block_size = 4\n[services.api]\npath = \".\"\nenv_file = \".env.local\"\nport = \"PORT\"\n[services.api.env]\nPUBLIC_NAME = \"new\"\nHMR_PORT = \"${services.api.ports.hmr.port}\"\n[services.api.ports.hmr]\nenv = \"HMR_PORT\"\n"
	upgradeManifest(plan, upgraded, t)
	result, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{})
	if err != nil || result.Workspace.Generation != 2 {
		t.Fatalf("endpoint sync failed: %v", err)
	}
	allocation, err := r.store.Allocation(t.Context(), plan.WorkspaceID)
	if err != nil || len(allocation.Endpoints) != 2 {
		t.Fatal("endpoint not appended")
	}
	if allocation.Endpoints[1].Port != initial.Base+1 || allocation.Endpoints[0].Port != initial.Base {
		t.Fatal("existing endpoint slot changed")
	}
	data, err := os.ReadFile(filepath.Join(plan.Path, ".env.local"))
	if err != nil || !strings.Contains(string(data), "HMR_PORT="+strconv.Itoa(initial.Base+1)+"\n") {
		t.Fatal("new endpoint not published")
	}
	again, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{})
	if err != nil || again.RestartRequired || again.Workspace.Generation != 2 {
		t.Fatal("second endpoint sync did not converge")
	}
}

func TestSyncManagedDriftRequiresExplicitOverwrite(t *testing.T) {
	r, plan := syncLocalFixture(t)
	envPath := filepath.Join(plan.Path, ".env.local")
	if err := os.WriteFile(envPath, []byte("PUBLIC_NAME=user-edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	upgradeManifest(plan, "version = 1\n[services.api]\npath = \".\"\nenv_file = \".env.local\"\n[services.api.env]\nPUBLIC_NAME = \"new\"\n", t)
	_, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{})
	errorCode(t, err, "E_MANAGED_VALUE_CHANGED")
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || current.Generation != 1 {
		t.Fatal("drift started generation")
	}
	result, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{OverwriteManaged: true})
	if err != nil || result.Workspace.Generation != 2 {
		t.Fatalf("explicit overwrite failed: %v", err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil || !strings.Contains(string(data), "PUBLIC_NAME=new\n") {
		t.Fatal("managed overwrite did not publish")
	}
}

func TestSyncUpdatesDeclaredRemoteValuesWhilePreservingResourceIdentity(t *testing.T) {
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
	managed := make(chan map[string]string, 2)
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
	initial := <-managed
	if initial["SITE_URL"] == "" {
		t.Fatal("creation remote update unexpected")
	}
	upgraded := `version = 1
[workspace]
port_block_size = 4
[resources.backend]
provider = "convex"
path = "packages/backend"
project = "dev-team:m0"
[resources.backend.env]
SITE_URL = "${services.web.url}"
CUSTOM_SYNC = "${workspace.id}"
[services.web]
path = "apps/web"
env_file = ".env.local"
port = "PORT"
[services.web.env]
VITE_CONVEX_URL = "${resources.backend.url}"
VITE_CONVEX_SITE_URL = "${resources.backend.site_url}"
EXTRA_LOCAL = "synced"
`
	upgradeManifest(plan, upgraded, t)
	result, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{ProviderFactory: factory})
	if err != nil || result.Workspace.Generation != 2 || !result.RestartRequired {
		t.Fatalf("declared sync failed: %v", err)
	}
	update := <-managed
	if update["CUSTOM_SYNC"] != plan.WorkspaceID || update["SITE_URL"] == "" {
		t.Fatalf("unexpected remote sync update: %v", update)
	}
	resources, err := r.store.Resources(t.Context(), plan.WorkspaceID)
	if err != nil || resources[0].RemoteName != "calm-cow-456" {
		t.Fatal("sync retargeted resource")
	}
	front, err := os.ReadFile(filepath.Join(plan.Path, "apps", "web", ".env.local"))
	if err != nil || !strings.Contains(string(front), "EXTRA_LOCAL=synced\n") {
		t.Fatal("additive local value missing")
	}
}
