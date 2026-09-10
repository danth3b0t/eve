package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func selectorFixture(t *testing.T, destination, line string) (repository, GitPlan) {
	t.Helper()
	r, plan := resourceFixture(t)
	path := filepath.Join(r.root, destination)
	if err := os.WriteFile(path, []byte("CONVEX_DEPLOYMENT=dev:old-selector\n"+line), 0600); err != nil {
		t.Fatal(err)
	}
	lock := approved(t, r, plan)
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := lock.Resources(t.Context())
	if err != nil || len(resources) != 1 {
		t.Fatal(err)
	}
	factory := fakeConvexFactory(t, make(chan map[string]string, 1), false, nil, nil)
	err = func() error {
		_, err := provisionResources(t.Context(), r.store, lock, current, factory)
		return err
	}()
	if err == nil {
		t.Fatal("selector conflict was provisioned")
	}
	_ = lock.Close()
	return r, plan
}

func TestSourceSelectorConflictBlocksProviderWrites(t *testing.T) {
	r, _ := selectorFixture(t, filepath.Join("packages", "backend", ".env.local"), "CONVEX_SELF_HOSTED_URL=https://elsewhere.invalid\n")
	_ = r
}

func TestServiceSelectorConflictBlocksProviderWrites(t *testing.T) {
	_, _ = selectorFixture(t, filepath.Join("apps", "web", ".env.local"), "CONVEX_DEPLOYMENT_TOKEN=blocked\n")
}

func TestAmbientSelectorBlocksProviderWrites(t *testing.T) {
	t.Setenv("CONVEX_DEPLOYMENT", "dev:stale")
	_, _ = selectorFixture(t, filepath.Join("packages", "backend", ".env.local"), "EXISTING_UNMANAGED=kept\n")
}

func TestSelectorErrorNeverLeaksValue(t *testing.T) {
	got := func() error {
		violation, err := selectorViolations([]byte("CONVEX_DEPLOYMENT_TOKEN=very-sensitive-value\n"), true, "packages/backend/.env.local")
		return errOrSelectorError(err, "source", "packages/backend/.env.local", violation)
	}()
	if got == nil || !strings.Contains(got.Error(), "CONVEX_DEPLOYMENT_TOKEN") || strings.Contains(got.Error(), "very-sensitive-value") {
		t.Fatalf("diagnostic unsafe or opaque: %v", got)
	}
}
