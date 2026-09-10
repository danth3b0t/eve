package lifecycle

import (
	"context"
	"testing"

	"eve/internal/provider/convex"
)

type envReadCountAdapter struct {
	convexAdapter
	reads int
}

func (a *envReadCountAdapter) Env(ctx context.Context, deployment convex.Deployment, key string) (map[string]string, error) {
	a.reads++
	return map[string]string{}, nil
}

func TestDefaultsOnlySkipsEnvironmentRead(t *testing.T) {
	adapter := &envReadCountAdapter{}
	if err := configureResourceEnv(t.Context(), adapter, convex.Deployment{}, "dev:exact|key", nil); err != nil {
		t.Fatal(err)
	}
	if adapter.reads != 0 {
		t.Fatalf("defaults-only provisioning read remote env %d times", adapter.reads)
	}
}
