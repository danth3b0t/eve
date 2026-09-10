package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"eve/internal/domain"
	"golang.org/x/term"
)

func readTeamToken(ctx context.Context, useStdin bool) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if useStdin {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<10+1))
		if err != nil {
			return "", &domain.Error{Code: "E_CREDENTIAL_PROFILE", Message: "credential input could not be read safely"}
		}
		return normalizeToken(string(data))
	}
	terminal := int(os.Stdin.Fd())
	if !term.IsTerminal(terminal) {
		return "", &domain.Error{Code: "E_PROVIDER_AUTH", Message: "run on an interactive terminal or pass exactly one token through --token-stdin"}
	}
	type result struct {
		value string
		err   error
	}
	answered := make(chan result, 1)
	go func() { data, err := term.ReadPassword(terminal); answered <- result{string(data), err} }()
	_, _ = os.Stderr.WriteString("Development team token: ")
	select {
	case <-ctx.Done():
		_ = os.Stdin.Close()
		return "", ctx.Err()
	case result := <-answered:
		_, _ = os.Stderr.WriteString("\n")
		if result.err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return "", ctx.Err()
			}
			if errors.Is(result.err, io.EOF) {
				return "", &domain.Error{Code: "E_PROVIDER_AUTH", Message: "team token input is missing"}
			}
			return "", &domain.Error{Code: "E_CREDENTIAL_PROFILE", Message: "token could not be read safely"}
		}
		return normalizeToken(result.value)
	}
}
func normalizeToken(value string) (string, error) {
	token := strings.TrimSpace(value)
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", &domain.Error{Code: "E_PROVIDER_AUTH", Message: "team token input is missing or invalid"}
	}
	if len(token) > 1<<20 {
		return "", &domain.Error{Code: "E_PROVIDER_AUTH", Message: "token input is too large"}
	}
	return token, nil
}
