package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"eve/internal/domain"
	"eve/internal/platform"
	"github.com/google/uuid"
)

type Worktree struct {
	Path, HeadOID, Branch string
	Locked                bool
	LockReason            string
	Prunable              bool
}

func (c *Client) Worktrees(ctx context.Context, root string) ([]Worktree, error) {
	data, err := c.read(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(data)
}

func parseWorktrees(data []byte) ([]Worktree, error) {
	var all []Worktree
	var current *Worktree
	for _, raw := range bytes.Split(data, []byte{0}) {
		if len(raw) == 0 {
			if current != nil {
				all = append(all, *current)
				current = nil
			}
			continue
		}
		key, value, _ := strings.Cut(string(raw), " ")
		if key == "worktree" {
			if current != nil || !filepath.IsAbs(value) {
				return nil, problem("E_GIT_FORMAT", "invalid worktree inventory", "")
			}
			current = &Worktree{Path: filepath.Clean(value)}
			continue
		}
		if current == nil {
			return nil, problem("E_GIT_FORMAT", "invalid worktree inventory", "")
		}
		switch key {
		case "HEAD":
			if !validOID(value) {
				return nil, problem("E_GIT_FORMAT", "invalid worktree commit ID", "")
			}
			current.HeadOID = value
		case "branch":
			if !strings.HasPrefix(value, "refs/heads/") {
				return nil, problem("E_GIT_FORMAT", "invalid worktree branch metadata", "")
			}
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "locked":
			current.Locked, current.LockReason = true, value
		case "prunable":
			current.Prunable = true
		case "bare", "detached":
			// Inspect rejects bare checkouts; detached is not a branch label.
		default:
			// Irrelevant additive porcelain fields do not change ownership.
		}
	}
	if current != nil || len(all) == 0 {
		return nil, problem("E_GIT_FORMAT", "truncated or empty worktree inventory", "")
	}
	return all, nil
}

// CreationReference is a temporary native Git lock reason, not a startup marker
// or a substitute for the external OS-held operation lock. Keep it until the
// actual Git identity is durably recorded. This makes a completed-but-unrecorded
// worktree distinguishable from an unrelated directory during reconciliation.
func CreationReference(operationID string) (string, error) {
	id, err := uuid.Parse(operationID)
	if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != operationID {
		return "", problem("E_ID_INVALID", "creation requires a full operation UUIDv4", "")
	}
	return "eve-create:" + operationID, nil
}

func validReference(value string) bool {
	ref, err := CreationReference(strings.TrimPrefix(value, "eve-create:"))
	return err == nil && ref == value
}

type AddRequest struct {
	Source    domain.GitIdentity
	Target    Target
	Path      string
	Reference string
	Scratch   string // protected directory outside repositories, supplied by state
}

// Add is a first attempt only. The caller must hold workspace/repository locks
// and have persisted an inflight intent before invoking it. It never adopts an
// existing path or resets a branch. Failures leave observable Git state intact.
func (c *Client) Add(ctx context.Context, in AddRequest) (Checkout, error) {
	if !validReference(in.Reference) || !validOID(in.Target.HeadOID) {
		return Checkout{}, problem("E_GIT_INTENT", "pinned commit and recorded creation reference are required", in.Path)
	}
	source, err := c.Verify(ctx, in.Source)
	if err != nil {
		return Checkout{}, err
	}
	if err := c.Compatible(ctx, source); err != nil {
		return Checkout{}, err
	}
	if err := c.validateBranch(ctx, in.Source.Path, in.Target.Branch); err != nil {
		return Checkout{}, err
	}
	if err := c.targetLayout(ctx, in.Source.Path, in.Target.HeadOID); err != nil {
		return Checkout{}, err
	}
	exists, err := c.branchExists(ctx, in.Source.Path, in.Target.Branch)
	if err != nil {
		return Checkout{}, err
	}
	if in.Target.NewBranch == exists {
		return Checkout{}, problem("E_GIT_REF_CHANGED", "branch existence changed since planning; no reset or adoption is allowed", in.Path)
	}
	if exists {
		head, err := c.commit(ctx, in.Source.Path, "refs/heads/"+in.Target.Branch)
		if err != nil {
			return Checkout{}, err
		}
		if head != in.Target.HeadOID {
			return Checkout{}, problem("E_GIT_REF_CHANGED", "existing branch moved since planning", in.Path)
		}
	}
	if err := c.unclaimed(ctx, in.Source.Path, in.Path, in.Target.Branch); err != nil {
		return Checkout{}, err
	}
	if inside(in.Source.Path, in.Path) || inside(in.Source.CommonDir, in.Path) {
		return Checkout{}, problem("E_PATH_ESCAPE", "managed worktrees must be outside the source checkout and Git metadata", in.Path)
	}
	if err := newPath(in.Path, false); err != nil {
		return Checkout{}, err
	}
	if err := newPath(in.Path, true); err != nil {
		return Checkout{}, err
	}
	if err := ctx.Err(); err != nil {
		return Checkout{}, err
	}
	// Git otherwise accepts an existing empty directory. Claim it exclusively
	// ourselves so a path appearing after preflight is not silently adopted.
	if err := os.Mkdir(in.Path, 0700); err != nil {
		return Checkout{}, problem("E_WORKTREE_EXISTS", "cannot exclusively create the planned worktree directory", in.Path)
	}
	_, rootIdentity, err := platform.DirectoryIdentity(in.Path)
	if err != nil {
		return Checkout{}, err
	}
	args := []string{"worktree", "add", "--no-guess-remote", "--lock", "--reason", in.Reference}
	ref := in.Target.Branch
	if in.Target.NewBranch {
		args = append(args, "-b", in.Target.Branch)
		ref = in.Target.HeadOID
	}
	args = append(args, "--", in.Path, ref)
	if err := c.mutate(ctx, in.Source.Path, in.Scratch, args...); err != nil {
		return Checkout{}, err
	}
	created, err := c.ObserveCreation(ctx, in)
	if err != nil {
		return Checkout{}, err
	}
	if created.Identity.PathIdentity != rootIdentity {
		return Checkout{}, problem("E_GIT_IDENTITY", "worktree root changed during Git creation", in.Path)
	}
	return created, nil
}

func (c *Client) unclaimed(ctx context.Context, root, path, branch string) error {
	entries, err := c.Worktrees(ctx, root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Path == path || entry.Branch == branch {
			return problem("E_GIT_WORKTREE_CONFLICT", "path or branch is already registered with Git; do not force or prune it", entry.Path)
		}
	}
	return nil
}

// ObserveCreation is read-only. Only an outstanding recorded inflight intent may
// call it to adopt its own receipt. A merely matching path/branch/commit without
// the exact temporary creation reference is not ownership evidence.
func (c *Client) ObserveCreation(ctx context.Context, in AddRequest) (Checkout, error) {
	if !validReference(in.Reference) {
		return Checkout{}, problem("E_GIT_INTENT", "recorded creation reference is required", in.Path)
	}
	if _, err := c.Verify(ctx, in.Source); err != nil {
		return Checkout{}, err
	}
	if err := checkAncestors(in.Path, false); err != nil {
		return Checkout{}, err
	}
	observed, err := c.Inspect(ctx, in.Path)
	if err != nil {
		return Checkout{}, err
	}
	id := observed.Identity
	if id.Path != in.Path || id.CommonDir != in.Source.CommonDir || id.CommonIdentity != in.Source.CommonIdentity || id.AdminDir == id.CommonDir {
		return Checkout{}, problem("E_GIT_IDENTITY", "creation did not produce the intended linked worktree", in.Path)
	}
	entry, err := c.registered(ctx, observed)
	if err != nil {
		return Checkout{}, err
	}
	if !entry.Locked || entry.LockReason != in.Reference || entry.Prunable {
		return Checkout{}, problem("E_GIT_OWNERSHIP", "no exact creation receipt; retain the directory for review", in.Path)
	}
	if observed.HeadOID != in.Target.HeadOID || observed.Branch != in.Target.Branch {
		return Checkout{}, problem("E_GIT_REF_CHANGED", "created worktree no longer matches the frozen revision; do not reset it", in.Path)
	}
	// Git writes --reason before checkout has completed. A matching lock reason
	// alone can therefore also describe a live/orphaned/incomplete checkout.
	index := filepath.Join(id.AdminDir, "index")
	before, err := os.Lstat(index)
	if err != nil || !before.Mode().IsRegular() {
		return Checkout{}, problem("E_GIT_INCOMPLETE", "creation index is missing or unsafe; retain the inflight intent", in.Path)
	}
	if err := checkIndexUnlocked(id.AdminDir); err != nil {
		return Checkout{}, err
	}
	changes, err := c.Changes(ctx, in.Path)
	if err != nil {
		return Checkout{}, err
	}
	if len(changes) != 0 {
		return Checkout{}, problem("E_GIT_INCOMPLETE", "creation is incomplete or was edited; do not reset or adopt it automatically", in.Path)
	}
	if err := checkIndexUnlocked(id.AdminDir); err != nil {
		return Checkout{}, err
	}
	after, err := os.Lstat(index)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return Checkout{}, problem("E_GIT_INCOMPLETE", "creation index changed during observation", in.Path)
	}
	return observed, nil
}

// Git's index lock is only a conservative refusal signal, not proof of a live
// process. Never remove it automatically or infer that it is safe from a PID.
func checkIndexUnlocked(admin string) error {
	if _, err := os.Lstat(filepath.Join(admin, "index.lock")); !errors.Is(err, os.ErrNotExist) {
		return problem("E_GIT_INCOMPLETE", "Git index may be changing; review its lock before retry", admin)
	}
	return nil
}

func (c *Client) registered(ctx context.Context, checkout Checkout) (Worktree, error) {
	entries, err := c.Worktrees(ctx, checkout.Identity.Path)
	if err != nil {
		return Worktree{}, err
	}
	for _, entry := range entries {
		if entry.Path == checkout.Identity.Path && entry.HeadOID == checkout.HeadOID && entry.Branch == checkout.Branch && !entry.Prunable {
			return entry, nil
		}
	}
	return Worktree{}, problem("E_GIT_IDENTITY", "Git registration does not match the checkout", checkout.Identity.Path)
}

// FinishCreation removes ONLY our temporary creation lock, AFTER the identity is
// durably recorded. An unrelated user lock must never be unlocked automatically.
func (c *Client) FinishCreation(ctx context.Context, id domain.GitIdentity, reference, scratch string) error {
	if !validReference(reference) {
		return problem("E_GIT_INTENT", "recorded creation reference is required", id.Path)
	}
	checkout, err := c.Verify(ctx, id)
	if err != nil {
		return err
	}
	entry, err := c.registered(ctx, checkout)
	if err != nil {
		return err
	}
	if !entry.Locked {
		return nil // reconciles a successful unlock whose response was lost
	}
	if entry.LockReason != reference {
		return problem("E_GIT_LOCKED", "worktree has a user/unknown Git lock; review it manually", id.Path)
	}
	return c.mutate(ctx, id.Path, scratch, "worktree", "unlock", "--", id.Path)
}

func (c *Client) mutate(ctx context.Context, root, scratch string, args ...string) error {
	if _, err := platform.CheckPrivate(scratch, true); err != nil {
		return err
	}
	canonical, err := c.CheckStateLocation(ctx, root, scratch)
	if err != nil {
		return err
	}
	scratch = canonical
	hooks, err := os.MkdirTemp(scratch, "git-hooks-")
	if err != nil {
		return problem("E_STATE_IO", "cannot create temporary empty Git hooks directory", scratch)
	}
	defer os.Remove(hooks) // Never recursively delete an unexpectedly populated directory.
	result, err := c.run(ctx, root, append([]string{"-c", "core.hooksPath=" + hooks}, args...)...)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return problem("E_GIT_MUTATION", "Git mutation failed; retain intent and inspect observable state before retry", root)
	}
	return nil
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Paths are absolute canonical plans. Existing components may not be symlinks;
// only absence (not permission/I/O errors) is accepted during planning.
func checkAncestors(path string, missing bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return problem("E_PATH_ESCAPE", "an absolute normalized worktree path is required", path)
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && missing {
			return nil
		}
		if err != nil {
			return problem("E_WORKTREE_PATH", "worktree path is missing or inaccessible", current)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return problem("E_SYMLINK", "worktree paths must not traverse symlinks", current)
		}
		if !info.IsDir() {
			return problem("E_WORKTREE_PATH", "worktree parent is not a directory", current)
		}
	}
	return nil
}

func newPath(path string, createParents bool) error {
	if err := checkAncestors(path, true); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return problem("E_WORKTREE_EXISTS", "destination must not exist; never adopt or overwrite it", path)
	}
	if createParents {
		if _, err := platform.PrivateDir(filepath.Dir(path)); err != nil {
			return err
		}
		return newPath(path, false)
	}
	return nil
}
