//go:build linux || darwin

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"eve/internal/domain"
	"golang.org/x/sys/unix"
)

func pathError(code, message, path string) error {
	return &domain.Error{Code: code, Message: message, Path: path}
}

// PrivateDir creates a state directory, never a worktree destination. Existing
// broad permissions are diagnosed, not silently repaired. Ancestor aliases
// (notably macOS /var) are resolved; the directory itself may not be a symlink.
func PrivateDir(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", pathError("E_STATE_PATH", "state directory must be absolute", path)
	}
	path = filepath.Clean(path)
	var created []string
	for p := path; ; p = filepath.Dir(p) {
		_, err := os.Lstat(p)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", pathError("E_STATE_IO", "cannot inspect state ancestors", p)
		}
		created = append(created, p)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", pathError("E_STATE_IO", "cannot create state directory", path)
	}
	if _, err := CheckPrivate(path, true); err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", pathError("E_STATE_IO", "cannot resolve state directory", path)
	}
	if err := CheckStateAncestors(canonical); err != nil {
		return "", err
	}
	for _, p := range created {
		if err := SyncDirectory(p); err != nil {
			return "", err
		}
		if err := SyncDirectory(filepath.Dir(p)); err != nil {
			return "", err
		}
	}
	return canonical, nil
}

// CheckStateAncestors rejects a namespace another unprivileged user can replace.
// Root/current-user owned sticky directories such as /tmp are allowed. This is
// not protection against root or another process acting as the current user.
func CheckStateAncestors(canonical string) error {
	for p := filepath.Dir(canonical); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() {
			return pathError("E_STATE_PATH", "state ancestor is missing, linked or inaccessible", p)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return pathError("E_STATE_PERMISSIONS", "state ancestors must not permit another user to replace the state directory", p)
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

// SyncDirectory makes newly created directory entries durable where the host
// filesystem supports directory fsync; actual I/O failures remain failures.
func SyncDirectory(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return pathError("E_STATE_IO", "cannot open directory for sync", path)
	}
	defer f.Close()
	if err := f.Sync(); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) {
		return pathError("E_STATE_IO", "cannot sync directory", path)
	}
	return nil
}

// CheckPrivate uses lstat, not open/close: closing an unrelated descriptor for a
// SQLite database or SHM file can drop the process's POSIX database locks.
func CheckPrivate(path string, directory bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, pathError("E_STATE_IO", "cannot inspect state object", path)
	}
	if err := privateInfo(path, info, directory); err != nil {
		return nil, err
	}
	return info, nil
}

func privateInfo(path string, info os.FileInfo, directory bool) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return pathError("E_STATE_SYMLINK", "state objects must not be symlinks", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return pathError("E_STATE_OWNER", "state object must belong to the current user", path)
	}
	want := os.FileMode(0600)
	if directory {
		want = 0700
		if !info.IsDir() {
			return pathError("E_STATE_TYPE", "state directory required", path)
		}
	} else if !info.Mode().IsRegular() || st.Nlink != 1 {
		return pathError("E_STATE_TYPE", "state file must be regular and have exactly one link", path)
	}
	if info.Mode().Perm() != want || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return pathError("E_STATE_PERMISSIONS", "state directories require 0700 and files require 0600", path)
	}
	return nil
}

// CreatePrivateFile exclusively creates an empty file. Existing files are only
// inspected; in particular we never open/close a live SQLite file to inspect it.
func CreatePrivateFile(path string) (os.FileInfo, error) {
	if info, err := CheckPrivate(path, false); err == nil {
		return info, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if errors.Is(err, os.ErrExist) {
		return CheckPrivate(path, false)
	}
	if err != nil {
		return nil, pathError("E_STATE_IO", "cannot create private state file", path)
	}
	defer f.Close()
	if err = f.Sync(); err != nil {
		return nil, pathError("E_STATE_IO", "cannot sync private state file", path)
	}
	if err := SyncDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return CheckPrivate(path, false)
}

// DirectoryIdentity is metadata, not authorization for Git or application-file
// mutations. The Git layer must independently verify repository membership.
func DirectoryIdentity(path string) (canonical, identity string, err error) {
	canonical, err = filepath.EvalSymlinks(path)
	if err == nil {
		canonical, err = filepath.Abs(canonical)
	}
	if err != nil {
		return "", "", pathError("E_SOURCE_IDENTITY", "registered directory is missing or inaccessible", path)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", "", pathError("E_SOURCE_IDENTITY", "registered directory is missing or not a directory", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", pathError("E_SOURCE_IDENTITY", "filesystem identity unavailable", path)
	}
	return canonical, fmt.Sprintf("%x:%x", st.Dev, st.Ino), nil
}

var ErrLocked = errors.New("E_LOCK_BUSY: advisory lock already held")

type Lock struct {
	mu   sync.Mutex
	file *os.File
}

// TryLock never waits and never unlinks the lock file. File existence is not a
// live lock; the kernel releases flock on process death. The parent must be an
// EVE-owned private directory outside worktrees.
func TryLock(path string) (*Lock, error) {
	if _, err := CheckPrivate(filepath.Dir(path), true); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, pathError("E_STATE_IO", "cannot open advisory lock", path)
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, pathError("E_STATE_IO", "cannot inspect advisory lock", path)
	}
	if err := privateInfo(path, info, false); err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, pathError("E_STATE_LOCK", "filesystem advisory locking failed", path)
	}
	after, err := CheckPrivate(path, false)
	if err != nil || !os.SameFile(info, after) {
		return nil, pathError("E_STATE_IDENTITY", "lock path changed during acquisition", path)
	}
	ok = true
	return &Lock{file: f}, nil
}

func (l *Lock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
