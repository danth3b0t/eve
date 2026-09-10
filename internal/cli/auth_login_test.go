package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"eve/internal/provider/convex"
)

func TestAuthLoginStoresNamedProfileThroughInjectedValidation(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", base+"/config")
	old := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("login-probe-token\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	defer func() { os.Stdin = old; reader.Close() }()

	called := 0
	result, err := authLogin(t.Context(), []string{"--project", "team-name:app", "--profile", "kairo", "--token-stdin"}, func(ctx context.Context, token, project string) (convex.Project, error) {
		called++
		if token != "login-probe-token" || project != "team-name:app" {
			t.Fatal("login forwarded unexpected profile inputs")
		}
		return convex.Project{ID: 42, TeamID: 7, Slug: "app", TeamSlug: "team-name", Dev: "default-dev"}, nil
	})
	if err != nil || !result.OK || called != 1 {
		t.Fatalf("login failed: %v calls=%d", err, called)
	}
	if result.Auth["profile"] != "kairo" || result.Auth["team_slug"] != "team-name" || result.Auth["team_id"] != "7" {
		t.Fatalf("auth metadata lost: %#v", result.Auth)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", result, result), "login-probe-token") {
		t.Fatal("login response exposed a credential")
	}

	status, err := auth(t.Context(), []string{"convex", "status", "--profile", "kairo", "--json"})
	if err != nil || status.Auth["profile"] != "kairo" {
		t.Fatalf("named profile status failed: %v %#v", err, status.Auth)
	}
	removed, err := auth(t.Context(), []string{"convex", "logout", "--profile", "kairo", "--json"})
	if err != nil || !removed.OK || removed.Auth["profile"] != "kairo" {
		t.Fatalf("named profile logout failed: %v %#v", err, removed.Auth)
	}
}
