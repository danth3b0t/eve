package git

import (
	"context"
	"path"
	"strings"
)

const maxBackendCompletionCandidates = 20

// CompletionBackendPaths returns repo-relative package paths containing a
// committed convex.json. A sibling package is sufficient bounded evidence
// for tab completion; full dependency/consumer validation intentionally stays
// with init, never during a shell request.
func (c *Client) CompletionBackendPaths(ctx context.Context, root, oid string) ([]string, error) {
	if oid != "HEAD" && !validOID(oid) {
		return nil, problem("E_GIT_REF", "a pinned commit ID is required", "")
	}
	data, err := c.read(ctx, root, "ls-tree", "-r", "-z", "--name-only", oid)
	if err != nil {
		return nil, err
	}
	var backends []string
	for _, name := range strings.Split(string(data), "\x00") {
		if !strings.HasSuffix(name, "/convex.json") && name != "convex.json" || strings.Contains(name, "/node_modules/") || len(backends) >= maxBackendCompletionCandidates {
			continue
		}
		backends = append(backends, path.Dir(name))
	}
	return backends, nil
}
