package cli

import (
	"errors"
	"os"
	"testing"

	"eve/internal/domain"
)

func TestInteractiveApprovalNeedsTTY(t *testing.T) {
	old := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	defer func() { os.Stdin = old; reader.Close(); writer.Close() }()
	approved, err := interactiveYes(t.Context(), "Create?")
	if approved {
		t.Fatal("approval without a terminal succeeded")
	}
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != "E_APPROVAL_REQUIRED" {
		t.Fatalf("expected explicit approval error: %v", err)
	}
}
