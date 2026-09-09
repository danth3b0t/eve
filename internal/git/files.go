package git

import (
	"bytes"
	"context"
	"strconv"
	"strings"
)

// TreeEntry describes committed data, never a working-copy override or filter
// result. Tree and Index are bounded by the common Git stdout limit.
type TreeEntry struct {
	Mode, OID string
	Size      int64 // -1 for directories/gitlinks
}

func (c *Client) Tree(ctx context.Context, root, oid string) (map[string]TreeEntry, error) {
	if !validOID(oid) {
		return nil, problem("E_GIT_REF", "a pinned commit ID is required", "")
	}
	data, err := c.read(ctx, root, "ls-tree", "-r", "-t", "-l", "-z", "--full-tree", oid)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]TreeEntry)
	for _, row := range bytes.Split(data, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(row, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !ok || len(name) == 0 || len(fields) != 4 || !validOID(fields[2]) {
			return nil, problem("E_GIT_FORMAT", "invalid committed tree inventory", root)
		}
		size := int64(-1)
		if fields[3] != "-" {
			size, err = strconv.ParseInt(fields[3], 10, 64)
			if err != nil || size < 0 {
				return nil, problem("E_GIT_FORMAT", "invalid committed object size", root)
			}
		}
		entries[string(name)] = TreeEntry{fields[0], fields[2], size}
	}
	return entries, nil
}

func (c *Client) Index(ctx context.Context, root string) (map[string]bool, error) {
	data, err := c.read(ctx, root, "ls-files", "--cached", "-z")
	if err != nil {
		return nil, err
	}
	entries := make(map[string]bool)
	for _, name := range bytes.Split(data, []byte{0}) {
		if len(name) != 0 {
			entries[string(name)] = true
		}
	}
	return entries, nil
}

func (c *Client) Blob(ctx context.Context, root string, entry TreeEntry, limit int64) ([]byte, error) {
	if !validOID(entry.OID) || (entry.Mode != "100644" && entry.Mode != "100755") {
		return nil, problem("E_FILE_TYPE", "a regular committed blob is required", "")
	}
	if entry.Size < 0 || entry.Size > limit || entry.Size > maxOutput {
		return nil, problem("E_FILE_LIMIT", "committed file exceeds the supported byte limit", "")
	}
	data, err := c.read(ctx, root, "cat-file", "blob", entry.OID)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != entry.Size {
		return nil, problem("E_GIT_FORMAT", "committed file size differs from metadata", "")
	}
	return data, nil
}

// Ignored uses the ACTUAL checkout's repository/global ignore rules, not those
// of a different source branch. Callers separately establish index membership.
// A target tree alone cannot establish this working-tree policy.
func (c *Client) Ignored(ctx context.Context, root, path string) (bool, error) {
	// check-ignore accepts concrete filenames, not pathspec magic; even Git's
	// global --literal-pathspecs flag makes it fail. Prefix ./ so a filename
	// beginning with ':' cannot be interpreted as magic either.
	r, err := c.run(ctx, root, "--no-literal-pathspecs", "check-ignore", "--quiet", "--no-index", "--", "./"+path)
	if err != nil {
		return false, err
	}
	switch r.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, problem("E_GIT_READ", "ignore policy could not be established", path)
	}
}
