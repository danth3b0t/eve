//go:build linux || darwin

// Package private stores immutable sensitive objects outside repositories. It
// provides no application-file writes or deletion authority. Objects are private,
// not encrypted: the current user/root can read them.
package private

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"eve/internal/domain"
	"eve/internal/platform"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const MaxBytes int64 = 256 << 20

func fail() error {
	return &domain.Error{Code: "E_PRIVATE_OBJECT", Message: "private object is missing, unsafe, changed or incomplete; retain its recorded reference for recovery"}
}
func ValidRef(ref string) bool {
	id, err := uuid.Parse(ref)
	return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == ref
}

type Objects struct {
	root *os.Root
	path string
	info os.FileInfo
}

func (s Objects) String() string   { return "private.Objects" }
func (s Objects) GoString() string { return s.String() }

// Open only opens an existing private directory; the state layer creates and
// pins it. No path supplied to Read/Create can name anything but a UUIDv4.
func Open(path string) (*Objects, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fail()
	}
	if err := platform.RequireLocalFilesystem(path); err != nil {
		return nil, err
	}
	if err := platform.CheckStateAncestors(path); err != nil {
		return nil, err
	}
	info, err := platform.CheckPrivate(path, true)
	if err != nil {
		return nil, fail()
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fail()
	}
	s := &Objects{root: root, path: path, info: info}
	if err := s.check(); err != nil {
		root.Close()
		return nil, err
	}
	return s, nil
}
func (s *Objects) Close() error { return s.root.Close() }
func (s *Objects) check() error {
	if err := platform.CheckStateAncestors(s.path); err != nil {
		return err
	}
	located, err := platform.CheckPrivate(s.path, true)
	if err != nil || !os.SameFile(s.info, located) {
		return fail()
	}
	opened, err := s.root.Stat(".")
	if err != nil || !os.SameFile(s.info, opened) {
		return fail()
	}
	return nil
}
func stable(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
func (s *Objects) sync() error {
	f, err := s.root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail()
	}
	defer f.Close()
	if err := f.Sync(); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) {
		return fail()
	}
	return s.check()
}

// Create requires a durable external intent for ref. It never replaces an
// existing object, even a partial one. On any failure retain the object/reference;
// unlinking would destroy evidence of an ambiguous write.
func (s *Objects) Create(ctx context.Context, ref string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidRef(ref) || int64(len(data)) > MaxBytes {
		return fail()
	}
	if err := s.check(); err != nil {
		return err
	}
	f, err := s.root.OpenFile(ref, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return fail()
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || platform.ValidatePrivateInfo(info, false) != nil {
		return fail()
	}
	for remaining := data; len(remaining) > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := f.Write(remaining[:min(len(remaining), 64<<10)])
		if err != nil || n == 0 {
			return fail()
		}
		remaining = remaining[n:]
	}
	if err := f.Sync(); err != nil {
		return fail()
	}
	after, err := f.Stat()
	if err != nil || after.Size() != int64(len(data)) || platform.ValidatePrivateInfo(after, false) != nil {
		return fail()
	}
	located, err := s.root.Lstat(ref)
	if err != nil || !stable(after, located) {
		return fail()
	}
	if err := f.Close(); err != nil {
		return fail()
	}
	return s.sync()
}

// Read validates and re-syncs a complete object before it can be acknowledged in
// SQL. Its caller must verify the expected size and keyed fingerprint. No raw OS
// errors or content are returned in diagnostics.
func (s *Objects) Read(ctx context.Context, ref string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidRef(ref) || limit < 0 || limit > MaxBytes {
		return nil, fail()
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	before, err := s.root.Lstat(ref)
	if err != nil || platform.ValidatePrivateInfo(before, false) != nil || before.Size() > limit {
		return nil, fail()
	}
	f, err := s.root.OpenFile(ref, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fail()
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !stable(before, opened) || platform.ValidatePrivateInfo(opened, false) != nil {
		return nil, fail()
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx, f}, limit+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || int64(len(data)) > limit || int64(len(data)) != opened.Size() {
		return nil, fail()
	}
	after, err := f.Stat()
	if err != nil || !stable(opened, after) {
		return nil, fail()
	}
	located, err := s.root.Lstat(ref)
	if err != nil || !stable(after, located) {
		return nil, fail()
	}
	if err := f.Sync(); err != nil {
		return nil, fail()
	}
	if err := s.sync(); err != nil {
		return nil, err
	}
	return data, nil
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
