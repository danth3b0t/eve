package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"eve/internal/platform"
	"eve/internal/state"
)

func TestAuthStatusShowsProfileMetadataWithoutToken(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", base+"/config")
	paths, err := platform.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(t.Context(), paths.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreManagementProfile(t.Context(), "convex", "audit", "team-name", 42, "status-probe-token", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := authStatus(t.Context(), &commandOptions{Profile: "audit", JSON: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Auth["profile"] != "audit" || result.Auth["team_slug"] != "team-name" || result.Auth["team_id"] != "42" {
		t.Fatalf("auth status metadata: %#v", result.Auth)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", result, result), "status-probe-token") {
		t.Fatal("auth status exposed a credential")
	}
}
