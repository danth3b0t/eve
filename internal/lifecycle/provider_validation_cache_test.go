package lifecycle

import (
	"context"
	"testing"

	"eve/internal/provider/convex"
)

type validationCounterAdapter struct {
	convexAdapter
	projects   *int
	inspectors *int
}

func (a *validationCounterAdapter) ValidateProject(ctx context.Context, binding string) (convex.Project, error) {
	*a.projects++
	return a.convexAdapter.ValidateProject(ctx, binding)
}

func (a *validationCounterAdapter) InspectDefault(ctx context.Context, name, kind string, projectID int64) (convex.Deployment, error) {
	*a.inspectors++
	return a.convexAdapter.InspectDefault(ctx, name, kind, projectID)
}

func TestSameProjectValidationIsReusedWithinProvisioning(t *testing.T) {
	r, plan := multiFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	if _, err := PrepareGit(t.Context(), r.store, r.client, lock); err != nil {
		t.Fatal(err)
	}
	current, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	projectChecks, defaultChecks := 0, 0
	baseFactory := multiConvexFactory(t, make(chan map[string]string, 2))
	factory := func(token string) (convexAdapter, error) {
		api, err := baseFactory(token)
		if err != nil {
			return nil, err
		}
		return &validationCounterAdapter{convexAdapter: api, projects: &projectChecks, inspectors: &defaultChecks}, nil
	}
	bindings, err := provisionResources(t.Context(), r.store, lock, current, factory)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings.outputs) != 2 {
		t.Fatal("multi-resource outputs incomplete")
	}
	if projectChecks != 1 || defaultChecks != 1 {
		t.Fatalf("same-invocation validation repeated: project=%d defaults=%d", projectChecks, defaultChecks)
	}
}
