package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResumeUsesFrozenIdentityAndCompletesLocalCreate(t *testing.T) {
	r := repositoryFixture(t)
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte("/.env.local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "ignore native local configuration")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "resume-local", "")
	if err != nil {
		t.Fatal(err)
	}
	lock := approved(t, r, plan)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// First-stage interruption permits a new source snapshot; the commit/manifest identity remains frozen.
	if err := os.WriteFile(filepath.Join(r.root, ".env.local"), []byte("SOURCE_EDIT=ignored-by-frozen-intent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, ResumeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != plan.WorkspaceID || resumed.Path != plan.Path || resumed.State != "prepared" || resumed.Generation != 1 {
		t.Fatal("resume did not complete original create")
	}
	again, err := ResumeWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, ResumeOptions{})
	if err != nil || again.ID != resumed.ID || again.Path != resumed.Path || again.Generation != 1 {
		t.Fatal("completed resume was not idempotent")
	}
	content, err := os.ReadFile(filepath.Join(plan.Path, ".env.local"))
	if err != nil || !strings.Contains(string(content), "SOURCE_EDIT=ignored-by-frozen-intent") {
		t.Fatal("resume did not preserve the source's unmanaged local input")
	}
}
func TestResumeReconcilesUnknownConvexDeploymentAndCompletesCreate(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	managed := make(chan map[string]string, 1)
	posts := 0
	factory := fakeConvexFactory(t, managed, true, &posts)
	_, err = provisionResources(t.Context(), r.store, lock, current, factory)
	errorCode(t, err, "E_PROVIDER_AMBIGUOUS")
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, ResumeOptions{ProviderFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != "prepared" || resumed.Generation != 1 || posts != 1 {
		t.Fatal("resume recreated or did not finish the exact deployment")
	}
	resources, err := r.store.Resources(t.Context(), plan.WorkspaceID)
	if err != nil || resources[0].State != "configured" {
		t.Fatal("resource not configured")
	}
	backend, err := os.ReadFile(filepath.Join(plan.Path, "packages/backend", ".env.local"))
	if err != nil || !strings.Contains(string(backend), "CONVEX_DEPLOY_KEY=dev:calm-cow-456|convex-key") {
		t.Fatal("resume did not stage exact native binding")
	}
	resumedAgain, err := ResumeWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, ResumeOptions{ProviderFactory: factory})
	if err != nil || resumedAgain.ID != resumed.ID || resumedAgain.Generation != 1 {
		t.Fatal("prepared resume was not idempotent")
	}
}
