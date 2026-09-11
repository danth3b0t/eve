package git

import (
	"context"
	"strings"
)

// LocalBranches lists local branch references for completion only. The call
// never fetches and returns no history/content.
func (c *Client) LocalBranches(ctx context.Context, root string) ([]string, error) {
	data, err := c.read(ctx, root, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && safeArgument(line) {
			branches = append(branches, line)
		}
	}
	return branches, nil
}
