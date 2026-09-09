package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"eve/internal/domain"
)

type Change struct {
	Index, Worktree byte
	Path            string
}

func (c *Client) Changes(ctx context.Context, root string) ([]Change, error) {
	data, err := c.read(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none", "--no-renames")
	if err != nil {
		return nil, err
	}
	return parseChanges(data)
}

func parseChanges(data []byte) ([]Change, error) {
	var changes []Change
	if len(data) != 0 && data[len(data)-1] != 0 {
		return nil, problem("E_GIT_FORMAT", "truncated Git change inventory", "")
	}
	for _, row := range bytes.Split(data, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		if len(row) < 4 || row[2] != ' ' || !filepath.IsLocal(string(row[3:])) {
			return nil, problem("E_GIT_FORMAT", "invalid Git change inventory", "")
		}
		changes = append(changes, Change{row[0], row[1], string(row[3:])})
	}
	return changes, nil
}

type RemovalOptions struct {
	DiscardChanges bool
	// Optional future materializer hook: compare a regular tracked file and its
	// mode against the exact recorded EVE-published HMAC. It must not approve
	// arbitrary user changes. A nil callback treats all visible edits as user work.
	OwnedEdit func(path string) (bool, error)
}

type RemovalCheck struct {
	NeedsForce bool
	Warning    string
}

// CheckRemoval is read-only and must run BEFORE deleting remote resources. This
// Git-only check does not replace consent, registry ownership, endpoint probes,
// or the requirement to stop the ordinary launcher before destruction.
func (c *Client) CheckRemoval(ctx context.Context, id domain.GitIdentity, branch, reference string, options RemovalOptions) (RemovalCheck, error) {
	result := RemovalCheck{Warning: "Ignored local files and caches also disappear; stop the ordinary project launcher first."}
	if id.AdminDir == id.CommonDir || branch == "" {
		return result, problem("E_GIT_OWNERSHIP", "the source/main checkout must never be removed", id.Path)
	}
	checkout, err := c.Verify(ctx, id)
	if err != nil {
		return result, err
	}
	if checkout.Branch != branch {
		return result, problem("E_GIT_IDENTITY", "worktree branch changed; review it before deletion", id.Path)
	}
	if err := c.Compatible(ctx, checkout); err != nil {
		return result, err
	}
	entry, err := c.registered(ctx, checkout)
	if err != nil {
		return result, err
	}
	if entry.Locked && (!validReference(reference) || entry.LockReason != reference) {
		return result, problem("E_GIT_LOCKED", "worktree has an unrelated Git lock; --discard-changes does not bypass it", id.Path)
	}
	if err := checkIndexUnlocked(id.AdminDir); err != nil {
		return result, err
	}
	changes, err := c.Changes(ctx, id.Path)
	if err != nil {
		return result, err
	}
	for _, change := range changes {
		approved := options.DiscardChanges
		if !approved && change.Index == ' ' && change.Worktree == 'M' && options.OwnedEdit != nil {
			approved, err = options.OwnedEdit(change.Path)
			if err != nil {
				return result, err
			}
		}
		if !approved {
			return result, problem("E_WORKTREE_DIRTY", "user changes block deletion; review or explicitly discard them", change.Path)
		}
		result.NeedsForce = true
	}
	return result, nil
}

// Remove must be called only for a recorded owned identity after destruction
// intent/consent and any remote cleanup. Safety is checked again immediately
// before Git removal. No branch is deleted; no worktree is forcibly unlocked.
func (c *Client) Remove(ctx context.Context, id domain.GitIdentity, branch, reference, scratch string, options RemovalOptions) error {
	check, err := c.CheckRemoval(ctx, id, branch, reference, options)
	if err != nil {
		return err
	}
	if err := c.FinishCreation(ctx, id, reference, scratch); err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if check.NeedsForce {
		args = append(args, "--force")
	}
	args = append(args, "--", id.Path)
	// Run from the common admin directory: the directory being removed must not
	// be the subprocess's cwd. No global/common Git configuration is changed.
	if err := c.mutate(ctx, id.CommonDir, scratch, args...); err != nil {
		return err
	}
	for _, path := range []string{id.Path, id.AdminDir} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return problem("E_CLEANUP_PENDING", "owned worktree/admin removal could not be confirmed", path)
		}
	}
	return nil
}
