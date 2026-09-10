package lifecycle

import (
	"testing"
	"time"

	"eve/internal/provider/convex"
)

func TestSlowSetupUsesResourceAttemptWindow(t *testing.T) {
	r, plan := resourceFixture(t)
	t.Setenv("EVE_CONVEX_TOKEN", "auth-holder-sentinel")
	lock := approved(t, r, plan)
	resources, err := lock.Resources(t.Context())
	if err != nil || len(resources) != 1 {
		t.Fatal(err)
	}
	managed := make(chan map[string]string, 1)
	factory := fakeConvexFactory(t, managed, false, nil, nil)
	api, err := factory("auth-holder-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	oldWorkspaceStart := time.Now().Add(-3 * time.Minute).UnixMilli()
	deployment, err := provisionedDeployment(t.Context(), api, lock, resources[0], convex.Intent{ProjectID: 42, Reference: resources[0].RemoteReference, Region: resources[0].Spec.Region, StartMS: oldWorkspaceStart, ExpiresMS: resources[0].IntendedExpiresAtMS})
	if err != nil {
		t.Fatal(err)
	}
	after, err := lock.Resources(t.Context())
	if err != nil || len(after) != 1 || after[0].AttemptStartedAtMS <= oldWorkspaceStart || after[0].AttemptStartedAtMS > deployment.CreatedAt+2*time.Minute.Milliseconds() {
		t.Fatalf("old workspace window used instead of attempt window: %+v deployment=%+v", after[0], deployment)
	}
}
