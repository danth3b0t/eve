package git

import (
	"context"
	"eve/internal/domain"
)

// CheckPublication checks native Git locks as refusal signals, not liveness
// evidence. It neither unlocks nor changes the index/worktree.
func (c *Client) CheckPublication(ctx context.Context, id domain.GitIdentity, branch, head string) error {
	checkout, err := c.Verify(ctx, id)
	if err != nil {
		return err
	}
	if id.AdminDir == id.CommonDir || checkout.Branch != branch || checkout.HeadOID != head {
		return problem("E_GIT_IDENTITY", "publication requires the recorded linked checkout and revision", id.Path)
	}
	if err := c.Compatible(ctx, checkout); err != nil {
		return err
	}
	entry, err := c.registered(ctx, checkout)
	if err != nil {
		return err
	}
	if entry.Locked {
		return problem("E_GIT_LOCKED", "review the native Git lock before publication", id.Path)
	}
	return checkIndexUnlocked(id.AdminDir)
}
