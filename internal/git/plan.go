package git

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"eve/internal/config"
	"github.com/google/uuid"
)

type Target struct {
	Branch, HeadOID string
	NewBranch       bool
	Manifest        []byte `json:"-"`
}

func (c *Client) validateBranch(ctx context.Context, root, branch string) error {
	if !safeArgument(branch) {
		return problem("E_GIT_BRANCH", "invalid branch name", "")
	}
	r, err := c.run(ctx, root, "check-ref-format", "--branch", branch)
	if err != nil {
		return err
	}
	// --branch expands @{-n}; that shorthand is not a literal new branch name.
	if r.ExitCode != 0 || string(r.Output) != branch+"\n" {
		return problem("E_GIT_BRANCH", "a literal valid Git branch name is required", "")
	}
	return nil
}

func (c *Client) branchExists(ctx context.Context, root, branch string) (bool, error) {
	r, err := c.run(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	switch r.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, problem("E_GIT_READ", "branch lookup failed; absence is not established", root)
	}
}

// Plan uses only the registered source's HEAD (or an explicit ref) for a new
// branch. Existing branches always use their own commit and cannot be reset by
// --from. No fetch, worktree, branch, allocation, or application file is created.
func (c *Client) Plan(ctx context.Context, source Checkout, branch, from string) (Target, error) {
	if _, err := c.Verify(ctx, source.Identity); err != nil {
		return Target{}, err
	}
	if err := c.Compatible(ctx, source); err != nil {
		return Target{}, err
	}
	root := source.Identity.Path
	if err := c.validateBranch(ctx, root, branch); err != nil {
		return Target{}, err
	}
	exists, err := c.branchExists(ctx, root, branch)
	if err != nil {
		return Target{}, err
	}
	if err := c.unclaimed(ctx, root, "", branch); err != nil {
		return Target{}, err
	}
	if exists && from != "" {
		return Target{}, problem("E_GIT_REF", "--from applies only to a new branch; existing branches are never reset", "")
	}
	ref := "refs/heads/" + branch
	if !exists {
		ref = from
		if ref == "" {
			ref = "HEAD"
		}
	}
	oid, err := c.commit(ctx, root, ref)
	if err != nil {
		return Target{}, err
	}
	if err := c.targetLayout(ctx, root, oid); err != nil {
		return Target{}, err
	}
	manifest, err := c.Manifest(ctx, root, oid)
	if err != nil {
		return Target{}, err
	}
	if _, err := config.Parse(manifest); err != nil {
		return Target{}, err
	}
	return Target{Branch: branch, HeadOID: oid, NewBranch: !exists, Manifest: manifest}, nil
}

func (c *Client) targetLayout(ctx context.Context, root, oid string) error {
	data, err := c.read(ctx, root, "ls-tree", "-r", "-z", "--full-tree", oid)
	if err != nil {
		return err
	}
	for _, row := range bytes.Split(data, []byte{0}) {
		if bytes.HasPrefix(row, []byte("160000 ")) {
			return problem("E_GIT_LAYOUT", "target commit contains submodules; no checkout was created", root)
		}
	}
	return nil
}

// Manifest reads the committed blob, without checkout filters or working-copy
// overrides. A symlink named eve.toml is never interpreted as a manifest.
func (c *Client) Manifest(ctx context.Context, root, oid string) ([]byte, error) {
	if !validOID(oid) {
		return nil, problem("E_GIT_REF", "a pinned full commit ID is required", "")
	}
	data, err := c.read(ctx, root, "ls-tree", "-z", oid, "--", "eve.toml")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, problem("E_TARGET_MANIFEST_MISSING", "commit a reviewed eve.toml in the target revision before create", "eve.toml")
	}
	metadata, name, ok := bytes.Cut(bytes.TrimSuffix(data, []byte{0}), []byte{'\t'})
	fields := strings.Fields(string(metadata))
	if !ok || string(name) != "eve.toml" || len(fields) != 3 || fields[1] != "blob" || !validOID(fields[2]) {
		return nil, problem("E_GIT_FORMAT", "target manifest is not a regular blob", "eve.toml")
	}
	if fields[0] != "100644" && fields[0] != "100755" {
		return nil, problem("E_SYMLINK", "target manifest must not be a symlink", "eve.toml")
	}
	sizeText, err := c.value(ctx, root, "cat-file", "-s", fields[2])
	if err != nil {
		return nil, err
	}
	size, err := strconv.Atoi(sizeText)
	if err != nil || size < 0 || size > config.MaxBytes {
		return nil, problem("E_MANIFEST_INVALID", "target manifest exceeds the supported size", "eve.toml")
	}
	data, err = c.read(ctx, root, "cat-file", "blob", fields[2])
	if err != nil {
		return nil, err
	}
	if len(data) != size {
		return nil, problem("E_GIT_FORMAT", "target blob size did not match Git metadata", "eve.toml")
	}
	return data, nil
}

func slug(label string) string {
	var out strings.Builder
	separator := false
	for _, ch := range strings.ToLower(label) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			if separator && out.Len() > 0 && out.Len() < 47 {
				out.WriteByte('-')
			}
			if out.Len() < 48 {
				out.WriteByte(byte(ch))
			}
			separator = false
		} else {
			separator = true
		}
	}
	if out.Len() == 0 {
		return "workspace"
	}
	return out.String()
}

// WorkspacePath uses UUIDs independently of human labels. It does not create
// directories; filesystem containment/identity is checked again during Add.
func WorkspacePath(source, label, repositoryID, branch, workspaceID string) (string, error) {
	for _, text := range []string{repositoryID, workspaceID} {
		id, err := uuid.Parse(text)
		if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != text {
			return "", problem("E_ID_INVALID", "full UUIDv4 identities are required", "")
		}
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return "", problem("E_PATH_ESCAPE", "canonical source path is required", source)
	}
	return filepath.Join(filepath.Dir(source), ".eve-worktrees", slug(label)+"-"+repositoryID[:8], slug(branch)+"-"+workspaceID[:8]), nil
}
