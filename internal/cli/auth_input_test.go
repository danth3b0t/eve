package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"eve/internal/domain"
)

func TestNormalizeTokenBounds(t *testing.T) {
	token, err := normalizeToken("  valid-token_1  \n")
	if err != nil || token != "valid-token_1" {
		t.Fatalf("normal token: %v", err)
	}
	for _, bad := range []string{"", "   ", "bad\ntoken", "bad\rtoken", "bad\x00token", strings.Repeat("x", 1<<20+1)} {
		if _, err := normalizeToken(bad); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}
func TestTokenInputNeedsTTYOrExplicitStdin(t *testing.T) {
	old := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	defer func() { os.Stdin = old; reader.Close(); writer.Close() }()
	_, err = readTeamToken(t.Context(), false)
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != "E_PROVIDER_AUTH" {
		t.Fatalf("expected non-TTY refusal, got %v", err)
	}
}

func TestAuthLoginDispatchesOptions(t *testing.T) {
	old := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	defer func() { os.Stdin = old; reader.Close() }()

	// Empty --token-stdin reaches token validation. The pre-fix dispatcher rejected
	// these same valid options as a malformed auth invocation.
	_, err = auth(t.Context(), []string{"convex", "login", "--project", "team:project", "--token-stdin"})
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != "E_PROVIDER_AUTH" {
		t.Fatalf("expected token validation error, got %v", err)
	}
}
func TestCancelledInputWins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := readTeamToken(ctx, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}
