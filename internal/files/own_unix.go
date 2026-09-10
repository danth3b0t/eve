//go:build linux || darwin

package files

import (
	"context"
	"os"

	"eve/internal/domain"
)

// CompareManagedContent rereads a current destination through the root/nofollow
// policy and applies the caller's recorded-content verifier. Content never
// escapes to logs. A nil current file cannot satisfy a published tracked file.
func CompareManagedContent(ctx context.Context, id domain.GitIdentity, name string, mode os.FileMode, verify func([]byte) bool) error {
	if verify == nil {
		return failure("E_FILE_INTENT", "recorded file fingerprint is required", name)
	}
	r, err := openRoot(id.Path, id.PathIdentity)
	if err != nil {
		return err
	}
	defer r.fs.Close()
	current, err := r.read(ctx, name, MaxCopyBytes, true)
	if err != nil {
		return err
	}
	if current.info == nil || current.info.Mode() != mode || !verify(current.data) {
		return failure("E_FILE_CHANGED", "current file differs from EVE's recorded published image", name)
	}
	return r.check()
}

// ReadDestination snapshots one declared target with the no-link/no-follow
// policy. A nil identity means clean absence, not an existing user file.
func ReadDestination(ctx context.Context, id domain.GitIdentity, name string) ([]byte, *domain.FileIdentity, error) {
	r, err := openRoot(id.Path, id.PathIdentity)
	if err != nil {
		return nil, nil, err
	}
	defer r.fs.Close()
	current, err := r.read(ctx, name, MaxCopyBytes, true)
	if err != nil {
		return nil, nil, err
	}
	if err := r.check(); err != nil {
		return nil, nil, err
	}
	return current.data, identity(current.info), nil
}
