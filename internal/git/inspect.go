package git

import (
	"bytes"
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"

	"eve/internal/domain"
	"eve/internal/platform"
)

type Checkout struct {
	Identity domain.GitIdentity
	HeadOID  string
	Branch   string // empty means detached HEAD
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func (c *Client) value(ctx context.Context, dir string, args ...string) (string, error) {
	data, err := c.read(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return scalar(data)
}

// Inspect accepts an invoking subdirectory, but always returns the actual
// canonical checkout root. Caller-supplied Git environment cannot retarget it.
func (c *Client) Inspect(ctx context.Context, dir string) (Checkout, error) {
	var result Checkout
	invoking, _, err := platform.DirectoryIdentity(dir)
	if err != nil {
		return result, problem("E_GIT_CHECKOUT", "checkout directory is missing or inaccessible", dir)
	}
	bare, err := c.value(ctx, invoking, "rev-parse", "--is-bare-repository")
	if err != nil {
		return result, err
	}
	inside, err := c.value(ctx, invoking, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return result, err
	}
	if bare != "false" || inside != "true" {
		return result, problem("E_GIT_LAYOUT", "a normal non-bare checkout is required", invoking)
	}
	id := &result.Identity
	for _, entry := range []struct {
		args           []string
		path, identity *string
	}{
		{[]string{"rev-parse", "--show-toplevel"}, &id.Path, &id.PathIdentity},
		{[]string{"rev-parse", "--absolute-git-dir"}, &id.AdminDir, &id.AdminIdentity},
		{[]string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, &id.CommonDir, &id.CommonIdentity},
	} {
		path, err := c.value(ctx, invoking, entry.args...)
		if err != nil {
			return Checkout{}, err
		}
		if !filepath.IsAbs(path) {
			return Checkout{}, problem("E_GIT_FORMAT", "Git must report absolute administrative paths", invoking)
		}
		*entry.path, *entry.identity, err = platform.DirectoryIdentity(path)
		if err != nil {
			return Checkout{}, err
		}
	}
	rel, err := filepath.Rel(id.Path, invoking)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Checkout{}, problem("E_GIT_IDENTITY", "Git redirected the checkout outside the invoking directory", invoking)
	}
	if id.AdminDir != id.CommonDir && filepath.Dir(id.AdminDir) != filepath.Join(id.CommonDir, "worktrees") {
		return Checkout{}, problem("E_GIT_LAYOUT", "unexpected linked-worktree administrative location", id.AdminDir)
	}
	result.HeadOID, err = c.commit(ctx, id.Path, "HEAD")
	if err != nil {
		if ctx.Err() != nil {
			return Checkout{}, ctx.Err()
		}
		return Checkout{}, problem("E_GIT_HEAD", "checkout requires an initialized commit HEAD", id.Path)
	}
	r, err := c.run(ctx, id.Path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return Checkout{}, err
	}
	switch r.ExitCode {
	case 0:
		ref, err := scalar(r.Output)
		if err != nil || !strings.HasPrefix(ref, "refs/heads/") {
			return Checkout{}, problem("E_GIT_FORMAT", "HEAD is not a local branch", id.Path)
		}
		result.Branch = strings.TrimPrefix(ref, "refs/heads/")
	case 1: // documented detached-HEAD result
	default:
		return Checkout{}, problem("E_GIT_READ", "cannot inspect HEAD", id.Path)
	}
	if _, err := c.registered(ctx, result); err != nil {
		return Checkout{}, err
	}
	return result, nil
}

// InspectForCompletion reads only the canonical repository/platform identity and
// one HEAD revision in one bounded Git invocation. Lifecycle operations must continue
// calling the full safety-oriented Inspect; candidates are evidence, not authority.
func (c *Client) InspectForCompletion(ctx context.Context, dir string) (Checkout, error) {
	var result Checkout
	invoking, _, err := platform.DirectoryIdentity(dir)
	if err != nil {
		return result, problem("E_GIT_CHECKOUT", "checkout directory is missing or inaccessible", dir)
	}
	output, err := c.value(ctx, invoking, "rev-parse", "--is-bare-repository", "--is-inside-work-tree", "--show-toplevel", "--absolute-git-dir", "--path-format=absolute", "--git-common-dir", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return result, err
	}
	lines := strings.Split(output, "\n")
	if len(lines) != 6 || lines[0] != "false" || lines[1] != "true" {
		return result, problem("E_GIT_LAYOUT", "a normal non-bare checkout is required", invoking)
	}
	id := &result.Identity
	for index, entry := range []struct {
		path, identity *string
	}{
		{&id.Path, &id.PathIdentity},
		{&id.AdminDir, &id.AdminIdentity},
		{&id.CommonDir, &id.CommonIdentity},
	} {
		pathValue := lines[index+2]
		if !filepath.IsAbs(pathValue) {
			return Checkout{}, problem("E_GIT_FORMAT", "Git must report absolute administrative paths", invoking)
		}
		*entry.path, *entry.identity, err = platform.DirectoryIdentity(pathValue)
		if err != nil {
			return Checkout{}, err
		}
	}
	rel, err := filepath.Rel(id.Path, invoking)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Checkout{}, problem("E_GIT_IDENTITY", "Git redirected the checkout outside the invoking directory", invoking)
	}
	if id.AdminDir != id.CommonDir && filepath.Dir(id.AdminDir) != filepath.Join(id.CommonDir, "worktrees") {
		return Checkout{}, problem("E_GIT_LAYOUT", "unexpected linked-worktree administrative location", id.AdminDir)
	}
	result.HeadOID = lines[5]
	if !validOID(result.HeadOID) {
		return Checkout{}, problem("E_GIT_FORMAT", "Git did not return a full commit ID", id.Path)
	}
	return result, nil
}

// Compatible rejects layouts requiring orchestration that EVE does not perform.
// It reads index metadata only, never executable configuration or submodule code.
func (c *Client) Compatible(ctx context.Context, checkout Checkout) error {
	root := checkout.Identity.Path
	r, err := c.run(ctx, root, "config", "--type=bool", "--get", "core.sparseCheckout")
	if err != nil {
		return err
	}
	if r.ExitCode != 1 {
		if r.ExitCode != 0 || !bytes.Equal(r.Output, []byte("false\n")) {
			return problem("E_GIT_LAYOUT", "sparse checkout is not supported", root)
		}
	}
	data, err := c.read(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for _, row := range bytes.Split(data, []byte{0}) {
		if bytes.HasPrefix(row, []byte("160000 ")) {
			return problem("E_GIT_LAYOUT", "submodule orchestration is not supported", root)
		}
	}
	// These flags can hide user modifications from Git's usual dirty check.
	data, err = c.read(ctx, root, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, row := range bytes.Split(data, []byte{0}) {
		if len(row) != 0 && (row[0] == 'S' || (row[0] >= 'a' && row[0] <= 'z')) {
			return problem("E_GIT_LAYOUT", "skip-worktree or assume-unchanged flags require explicit repair", root)
		}
	}
	return nil
}

func (c *Client) commit(ctx context.Context, root, ref string) (string, error) {
	if !safeArgument(ref) {
		return "", problem("E_GIT_REF", "invalid revision argument", "")
	}
	value, err := c.value(ctx, root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if !validOID(value) {
		return "", problem("E_GIT_FORMAT", "Git did not return a full commit ID", root)
	}
	return value, nil
}

// Verify matches recorded filesystem identities, not names or HEAD alone.
// Commits/dirty files may change during ordinary work; they are checked separately.
func (c *Client) Verify(ctx context.Context, expected domain.GitIdentity) (Checkout, error) {
	if err := checkAncestors(expected.Path, false); err != nil {
		return Checkout{}, err
	}
	observed, err := c.Inspect(ctx, expected.Path)
	if err != nil {
		return Checkout{}, err
	}
	if observed.Identity != expected {
		return Checkout{}, problem("E_GIT_IDENTITY", "recorded checkout/common/admin identity no longer matches", expected.Path)
	}
	return observed, nil
}
