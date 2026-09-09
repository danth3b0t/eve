package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"eve/internal/platform"
)

// CheckStateLocation is read-only and runs BEFORE the mutating state opener.
// Existing ancestors are canonicalized (including macOS /var); the state object
// itself may not be a link. Every known checkout/common/admin directory is
// excluded. Filesystem identities also catch case aliases/bind-mounted parents.
func (c *Client) CheckStateLocation(ctx context.Context, root, statePath string) (string, error) {
	if !filepath.IsAbs(statePath) {
		return "", problem("E_STATE_PATH", "state directory must be absolute", statePath)
	}
	statePath = filepath.Clean(statePath)
	if info, err := os.Lstat(statePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", problem("E_STATE_SYMLINK", "state directory must not be a symlink", statePath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", problem("E_STATE_IO", "cannot inspect proposed state directory", statePath)
	}
	ancestor := statePath
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", problem("E_STATE_IO", "cannot inspect proposed state ancestors", ancestor)
		}
		ancestor = filepath.Dir(ancestor)
	}
	real, _, err := platform.DirectoryIdentity(ancestor)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(ancestor, statePath)
	if err != nil {
		return "", problem("E_STATE_PATH", "cannot resolve proposed state directory", statePath)
	}
	proposed := filepath.Join(real, rel)
	entries, err := c.Worktrees(ctx, root)
	if err != nil {
		return "", err
	}
	var protected []string
	for _, entry := range entries {
		protected = append(protected, entry.Path)
	}
	for _, flag := range []string{"--git-common-dir", "--absolute-git-dir"} {
		path, err := c.value(ctx, root, "rev-parse", "--path-format=absolute", flag)
		if err != nil {
			return "", err
		}
		protected = append(protected, path)
	}
	identities := make(map[string]bool)
	for _, path := range protected {
		if inside(path, proposed) {
			return "", problem("E_STATE_PATH", "state/scratch must be outside repository and Git administrative directories", statePath)
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue // stale Git entries still constrain the lexical path above
		}
		_, id, err := platform.DirectoryIdentity(path)
		if err != nil {
			return "", err
		}
		identities[id] = true
	}
	for p := real; ; p = filepath.Dir(p) {
		_, id, err := platform.DirectoryIdentity(p)
		if err != nil {
			return "", err
		}
		if identities[id] {
			return "", problem("E_STATE_PATH", "state/scratch aliases a repository or Git administrative directory", statePath)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return proposed, nil
}
