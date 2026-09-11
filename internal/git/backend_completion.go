package git

import (
	"context"
	"encoding/json"
	"path"
	"strings"
)

const maxBackendCompletionCandidates = 20

// CompletionBackendPaths finds committed convex.json packages and verifies the
// sibling package metadata without discovering services, reading arbitrary
// paths, or scanning env/config values. It performs no full onboarding pass.
func (c *Client) CompletionBackendPaths(ctx context.Context, root, oid string) ([]string, error) {
	if !validOID(oid) {
		return nil, problem("E_GIT_REF", "a pinned commit ID is required", "")
	}
	data, err := c.read(ctx, root, "ls-tree", "-r", "-z", "--name-only", oid)
	if err != nil {
		return nil, err
	}
	names := strings.Split(string(data), "\x00")
	packages := map[string]bool{}
	for _, name := range names {
		if strings.Contains(name, "/node_modules/") {
			delete(packages, path.Dir(name))
			continue
		}
		if path.Base(name) == "package.json" {
			packages[name] = true
		}
	}
	var backends []string
	for _, name := range names {
		if !strings.HasSuffix(name, "/convex.json") && name != "convex.json" || strings.Contains(name, "/node_modules/") || len(backends) >= maxBackendCompletionCandidates {
			continue
		}
		base := path.Dir(name)
		packagePath := path.Join(base, "package.json")
		if !packages[packagePath] || !safeArgument(oid+":"+packagePath) {
			continue
		}
		data, showErr := c.read(ctx, root, "show", oid+":"+packagePath)
		if showErr != nil {
			return nil, showErr
		}
		var metadata struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &metadata) != nil || (metadata.Dependencies["convex"] == "" && metadata.DevDependencies["convex"] == "") {
			continue
		}
		backends = append(backends, base)
	}
	return backends, nil
}
