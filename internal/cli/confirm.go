package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"

	"eve/internal/domain"
	"golang.org/x/term"
)

func interactiveYes(ctx context.Context, prompt string) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "interactive approval unavailable; use the explicit --yes flag"}
	}
	_, _ = os.Stderr.WriteString(prompt + " [yes/no]: ")
	type result struct {
		text string
		err  error
	}
	answer := make(chan result, 1)
	go func() { text, err := bufio.NewReader(os.Stdin).ReadString('\n'); answer <- result{text, err} }()
	select {
	case <-ctx.Done():
		_ = os.Stdin.Close()
		return false, ctx.Err()
	case result := <-answer:
		if result.err != nil && result.err != io.EOF {
			return false, &domain.Error{Code: "E_STATE_IO", Message: "approval could not be read safely"}
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return strings.TrimSpace(result.text) == "yes", nil
	}
}
