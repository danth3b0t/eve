package git

import (
	"context"
	"strings"
)

// LocalBranches lists local branch references for completion only. The call
// never fetches and returns no history/content.
func (c *Client) LocalBranches(ctx context.Context, root string) ([]string, error) {
	data, err := c.read(ctx, root, "for-each-ref", "--count=200", "--format=%(refname:short)", "refs/heads")
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

// CompletionRef is a bounded local ref value and its accepted insertion form.
type CompletionRef struct {
	Kind, Display, Insert string
}

// CompletionRefs lists local heads, tags and locally cached remote-tracking
// refs. It never fetches. Ambiguous head/tag short names are emitted fully
// qualified so a candidate cannot select a different identity.
func (c *Client) CompletionRefs(ctx context.Context, root, prefix string) ([]CompletionRef, error) {
	data, err := c.read(ctx, root, "for-each-ref", "--count=400", "--format=%(refname)%00%(refname:short)%00%(objecttype)", "refs/heads", "refs/tags", "refs/remotes")
	if err != nil {
		return nil, err
	}
	shortKinds := map[string][]string{}
	var refs []CompletionRef
	appendRef := func(kind, qualified, display string) {
		if display == "" || !safeArgument(display) {
			return
		}
		if prefix != "" && !strings.HasPrefix(display, prefix) && !strings.HasPrefix(qualified, prefix) {
			return
		}
		shortKinds[display] = append(shortKinds[display], kind)
		refs = append(refs, CompletionRef{Kind: kind, Display: display, Insert: display})
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 {
			continue
		}
		qualified, objectType := fields[0], fields[2]
		if strings.HasPrefix(qualified, "refs/heads/") {
			appendRef("head", qualified, strings.TrimPrefix(qualified, "refs/heads/"))
		} else if strings.HasPrefix(qualified, "refs/tags/") {
			appendRef("tag", qualified, strings.TrimPrefix(qualified, "refs/tags/"))
		} else if strings.HasPrefix(qualified, "refs/remotes/") {
			appendRef("remote", qualified, strings.TrimPrefix(qualified, "refs/remotes/"))
		} else if objectType == "commit" {
			appendRef("ref", qualified, qualified)
		}
	}
	for index, ref := range refs {
		if len(shortKinds[ref.Display]) > 1 && (ref.Kind == "head" || ref.Kind == "tag") {
			base := map[string]string{"head": "refs/heads/", "tag": "refs/tags/"}[ref.Kind]
			refs[index].Insert = base + ref.Display
		}
	}
	if prefix == "" || strings.HasPrefix("HEAD", prefix) {
		result, headErr := c.run(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD")
		if headErr == nil && len(result.Output) > 0 {
			refs = append([]CompletionRef{{Kind: "HEAD", Display: "HEAD", Insert: "HEAD"}}, refs...)
		}
	}
	return refs, nil
}

// WorktreeBranches returns branches currently attached to checked-out working
// trees and their canonical paths. The bounded porcelain read performs no
// pruning, fetch, hook or maintenance work.
func (c *Client) WorktreeBranches(ctx context.Context, root string) (map[string]string, error) {
	data, err := c.read(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	worktreePath := ""
	for _, record := range strings.Split(string(data), "\x00") {
		if record == "" {
			continue
		}
		if strings.HasPrefix(record, "worktree ") {
			worktreePath = strings.TrimPrefix(record, "worktree ")
			continue
		}
		if strings.HasPrefix(record, "branch refs/heads/") && worktreePath != "" {
			branch := strings.TrimPrefix(record, "branch refs/heads/")
			if safeArgument(branch) {
				out[branch] = worktreePath
			}
		}
	}
	return out, nil
}
