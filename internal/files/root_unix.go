//go:build linux || darwin

// Package files authorizes and snapshots declared configuration inputs. It does
// not publish application files or grant deletion authority.
package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"syscall"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/platform"
	"golang.org/x/sys/unix"
)

const maxEntries = 100000

func failure(code, message, path string) error {
	return &domain.Error{Code: code, Message: message, Path: path}
}

func forbidden(name string) bool {
	return strings.EqualFold(name, ".git") || strings.EqualFold(name, ".convex") || strings.EqualFold(name, "node_modules")
}

type directory struct {
	info  os.FileInfo
	names map[string]bool
}

type root struct {
	fs             *os.Root
	path, identity string
	dirs           map[string]directory
	entries        int
}

func openRoot(location, identity string) (*root, error) {
	canonical, observed, err := platform.DirectoryIdentity(location)
	if err != nil || canonical != location || observed != identity {
		return nil, failure("E_FILE_IDENTITY", "repository root moved or was replaced", location)
	}
	before, err := os.Lstat(location)
	if err != nil {
		return nil, failure("E_FILE_IDENTITY", "cannot inspect repository root", location)
	}
	fs, err := os.OpenRoot(location)
	if err != nil {
		return nil, failure("E_FILE_IO", "cannot open repository root", location)
	}
	after, err := fs.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		fs.Close()
		return nil, failure("E_FILE_IDENTITY", "repository root changed during open", location)
	}
	return &root{fs: fs, path: location, identity: identity, dirs: make(map[string]directory)}, nil
}

func (r *root) check() error {
	canonical, id, err := platform.DirectoryIdentity(r.path)
	if err != nil || canonical != r.path || id != r.identity {
		return failure("E_FILE_IDENTITY", "repository root changed during file planning", r.path)
	}
	return nil
}

func same(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

// Names are read in bounded batches, never sorted/unbounded by ReadDir(-1).
// Cache only an unchanged directory snapshot. Exact spelling rejects existing
// case/Unicode filesystem aliases rather than merging distinct manifest paths.
func (r *root) names(ctx context.Context, name string) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := r.fs.OpenFile(name, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, failure("E_FILE_IO", "cannot inspect directory entries", name)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, failure("E_FILE_IO", "cannot inspect directory identity", name)
	}
	if cached, ok := r.dirs[name]; ok && same(before, cached.info) {
		return cached.names, nil
	}
	r.entries -= len(r.dirs[name].names)
	delete(r.dirs, name)
	names := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := f.Readdirnames(128)
		for _, entry := range batch {
			names[entry] = true
			if r.entries+len(names) > maxEntries {
				return nil, failure("E_FILE_LIMIT", "aggregate directory inventory exceeds the supported bound", name)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, failure("E_FILE_IO", "cannot enumerate directory", name)
		}
	}
	after, err := f.Stat()
	if err != nil || !same(before, after) {
		return nil, failure("E_FILE_CHANGED", "directory changed during enumeration", name)
	}
	r.entries += len(names)
	r.dirs[name] = directory{after, names}
	return names, nil
}

// inspect validates ORIGINAL components before lexical normalization. In
// particular link/../file must not erase evidence of a link traversal. All links
// are conservatively refused, including links that remain inside the root.
// os.Root separately confines the actual I/O during namespace/link races.
func (r *root) inspect(ctx context.Context, raw string) (string, os.FileInfo, error) {
	clean, err := config.RelativePath(".", raw)
	if err != nil {
		return "", nil, failure("E_PATH_ESCAPE", "invalid repository-relative file path", raw)
	}
	parts := strings.Split(raw, "/")
	current := "."
	missing := false
	var info os.FileInfo
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		if forbidden(part) {
			return "", nil, failure("E_PATH_ESCAPE", "Git, installed dependencies and local backend state cannot be selected", raw)
		}
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if missing {
				return "", nil, failure("E_PATH_ESCAPE", "cannot normalize through a missing path component", raw)
			}
			current = path.Dir(current)
			continue
		}
		parent := current
		current = path.Join(current, part)
		if missing {
			continue
		}
		info, err = r.fs.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil {
			return "", nil, failure("E_FILE_IO", "cannot inspect path component", current)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, failure("E_SYMLINK", "configuration paths must not traverse links", current)
		}
		names, err := r.names(ctx, parent)
		if err != nil {
			return "", nil, err
		}
		if !names[part] {
			return "", nil, failure("E_PATH_ALIAS", "path spelling aliases another filesystem name", current)
		}
		if i != len(parts)-1 && !info.IsDir() {
			return "", nil, failure("E_FILE_TYPE", "intermediate path component is not a directory", current)
		}
	}
	if missing {
		return clean, nil, nil
	}
	info, err = r.fs.Lstat(clean)
	if err != nil {
		return "", nil, failure("E_FILE_CHANGED", "path changed during inspection", clean)
	}
	return clean, info, nil
}

type snapshot struct {
	info os.FileInfo
	data []byte
}

func regular(info os.FileInfo, destination bool, name string) error {
	if info == nil {
		return nil
	}
	if !info.Mode().IsRegular() {
		return failure("E_FILE_TYPE", "selected file must be regular", name)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return failure("E_FILE_IDENTITY", "file identity is unavailable", name)
	}
	if destination && st.Nlink != 1 {
		return failure("E_HARDLINK", "configuration destinations must have exactly one link", name)
	}
	return nil
}

func (r *root) read(ctx context.Context, raw string, limit int64, destination bool) (snapshot, error) {
	name, before, err := r.inspect(ctx, raw)
	if err != nil {
		return snapshot{}, err
	}
	if before == nil {
		return snapshot{}, nil
	}
	if err := regular(before, destination, name); err != nil {
		return snapshot{}, err
	}
	if limit < 0 || before.Size() > limit {
		return snapshot{}, failure("E_FILE_LIMIT", "selected file exceeds the remaining byte budget", name)
	}
	f, err := r.fs.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return snapshot{}, failure("E_FILE_CHANGED", "selected file could not be opened safely", name)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !same(before, opened) {
		return snapshot{}, failure("E_FILE_CHANGED", "selected file changed during open", name)
	}
	if err := regular(opened, destination, name); err != nil {
		return snapshot{}, err
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx, f}, limit+1))
	if ctx.Err() != nil {
		return snapshot{}, ctx.Err()
	}
	if err != nil {
		return snapshot{}, failure("E_FILE_IO", "cannot snapshot selected file", name)
	}
	if int64(len(data)) > limit {
		return snapshot{}, failure("E_FILE_LIMIT", "selected file exceeds the remaining byte budget", name)
	}
	after, err := f.Stat()
	if err != nil || !same(opened, after) {
		return snapshot{}, failure("E_FILE_CHANGED", "selected file changed while being read", name)
	}
	_, located, err := r.inspect(ctx, raw)
	if err != nil {
		return snapshot{}, err
	}
	if !same(after, located) {
		return snapshot{}, failure("E_FILE_CHANGED", "selected file was replaced while being read", name)
	}
	return snapshot{after, data}, nil
}

func (r *root) verify(ctx context.Context, name string, previous snapshot, destination bool) error {
	_, info, err := r.inspect(ctx, name)
	if err != nil {
		return err
	}
	if previous.info == nil && info == nil {
		return nil
	}
	if !same(previous.info, info) {
		return failure("E_FILE_CHANGED", "selected file identity, mode or size changed", name)
	}
	current, err := r.read(ctx, name, int64(len(previous.data)), destination)
	if err != nil {
		return err
	}
	if previous.info == nil && current.info == nil {
		return nil
	}
	if !same(previous.info, current.info) || !bytes.Equal(previous.data, current.data) {
		return failure("E_FILE_CHANGED", "selected file changed since the reviewed snapshot", name)
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
