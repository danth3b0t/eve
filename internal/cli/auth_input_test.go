package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"eve/internal/domain"
	"eve/internal/provider/convex"
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

func TestAuthLoginDispatchesTypedOptions(t *testing.T) {
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

	opts := &commandOptions{Project: "team:project", Profile: "default", TokenStdin: true}
	_, err = authLogin(t.Context(), opts, nil, func(context.Context, string, string) (convex.Project, error) {
		return convex.Project{}, errors.New("validator should not receive input")
	})
	if err == nil {
		t.Fatal("empty token input accepted")
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
