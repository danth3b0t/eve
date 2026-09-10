package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func doctorStatuses(result DoctorResult) map[string]string {
	out := map[string]string{}
	for _, check := range result.Checks {
		out[check.ID] = check.Status
	}
	return out
}
func TestDoctorReportsConfigurationAndWarningsWithoutLaunching(t *testing.T) {
	r, plan := syncLocalFixture(t)
	result, err := DoctorWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	statuses := doctorStatuses(result)
	for _, id := range []string{"registry", "git", "files", "endpoints", "resources"} {
		if statuses[id] != "pass" {
			t.Fatalf("unexpected initial status: %v", statuses)
		}
	}
	if statuses["runtime"] != "not_checked" || statuses["loader"] != "not_checked" {
		t.Fatal("doctor claimed launch evidence")
	}
	// Managed drift is a warning, not a rewritten file or process action.
	envPath := filepath.Join(plan.Path, ".env.local")
	if err := os.WriteFile(envPath, []byte("PUBLIC_NAME=edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	drifted, err := DoctorWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if doctorStatuses(drifted)["files"] != "warning" {
		t.Fatal("did not report published-image drift")
	}
	if err := os.WriteFile(filepath.Join(plan.Path, "user-new-file"), []byte("review me"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = DoctorWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if doctorStatuses(result)["git"] != "warning" {
		t.Fatal("visible Git change did not warn")
	}
}

func TestDoctorRemoteUsesOnlyIdentityChecks(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	factory := fakeConvexFactory(t, make(chan map[string]string, 1), false, nil)
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
	_, err = DoctorWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	remoteLock, err := r.store.LockWorkspace(plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := DoctorRemote(t.Context(), r.store, remoteLock, current, factory)
	closeErr := remoteLock.Close()
	if closeErr != nil && err == nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Status != "pass" || checks[0].ID != "provider_remote:backend" {
		t.Fatalf("unexpected remote checks: %v", checks)
	}
}
