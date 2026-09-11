package state

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"eve/internal/platform"
	"eve/schemas"
)

// OpenReadOnly opens an existing registry without creating directories, lock
// files, schema, WAL metadata or the machine key. It permits inspection only;
// mutations and workspace capability locks are denied by Store checks below.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	return OpenReadOnlyWithBusyTimeout(ctx, path, 5000)
}

// OpenReadOnlyWithBusyTimeout gives bounded metadata integrations an interruptible
// read-only open. It still never initializes, migrates, or acquires write authority.
func OpenReadOnlyWithBusyTimeout(ctx context.Context, path string, busyTimeoutMS int) (*Store, error) {
	if busyTimeoutMS < 0 || busyTimeoutMS > 5000 {
		busyTimeoutMS = 5000
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, failure("E_STATE_PATH", "state directory must be absolute")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil, failure("E_STATE_PATH", "state directory must exist and must not be a symlink")
	}
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, failure("E_STATE_PATH", "state directory is inaccessible")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, failure("E_STATE_PATH", "state directory is inaccessible")
	}
	if err := platform.CheckStateAncestors(root); err != nil {
		return nil, err
	}
	if err := platform.RequireLocalFilesystem(root); err != nil {
		return nil, err
	}
	s := &Store{root: root, readOnly: true, identities: make(map[string]os.FileInfo)}
	for _, name := range []string{"", "locks", "secrets", "pending"} {
		p := filepath.Join(root, name)
		info, err := platform.CheckPrivate(p, true)
		if err != nil {
			return nil, err
		}
		s.identities[p] = info
	}
	dbPath := filepath.Join(root, "state.sqlite")
	info, err := platform.CheckPrivate(dbPath, false)
	if err != nil {
		return nil, err
	}
	s.identities[dbPath] = info
	u := url.URL{Scheme: "file", Path: dbPath}
	q := url.Values{
		"mode": {"ro"}, "_foreign_keys": {"on"}, "_busy_timeout": {strconv.Itoa(busyTimeoutMS)}, "_defensive": {"true"}, "_dqs": {"false"}, "_pragma": {"trusted_schema(OFF)", "query_only(ON)"},
	}
	u.RawQuery = q.Encode()
	s.db, err = sql.Open("sqlite", u.String())
	if err != nil {
		return nil, dbError(err)
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	defer func() {
		if err != nil {
			s.db.Close()
		}
	}()
	if err = s.checkReadOnlySchema(ctx); err != nil {
		return nil, dbError(err)
	}
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) checkReadOnlySchema(ctx context.Context) error {
	var tables int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		return failure("E_STATE_SCHEMA", "database is not an EVE registry; do not overwrite it")
	}
	var applicationID int
	if err := s.db.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil || applicationID != 1163281713 {
		return failure("E_STATE_SCHEMA", "database is not an EVE registry; do not overwrite it")
	}
	var version, count int
	if err := s.db.QueryRowContext(ctx, `SELECT max(version),count(*) FROM schema_migrations`).Scan(&version, &count); err != nil || version != schemas.StateVersion || count != 1 {
		return failure("E_STATE_SCHEMA", "unsupported state schema; use a compatible EVE version, not a new registry")
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return err
	}
	return nil
}

// WorkspaceByBranch resolves one live workspace in an explicitly registered
// repository. Destroyed tombstones are excluded; destroyed branches remain Git's.
func (s *Store) WorkspaceByBranch(ctx context.Context, repositoryID, branch string) (Workspace, error) {
	if err := s.checkStorage(); err != nil {
		return Workspace{}, err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE repository_id=? AND branch=? AND state<>'destroyed'`, repositoryID, branch).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, failure("E_WORKSPACE_NOT_FOUND", "no live workspace is recorded for that branch")
	}
	if err != nil {
		return Workspace{}, err
	}
	return s.Workspace(ctx, id)
}
func (s *Store) WorkspaceByPath(ctx context.Context, repositoryID, path string) (Workspace, error) {
	if err := s.checkStorage(); err != nil {
		return Workspace{}, err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE repository_id=? AND path=? AND state<>'destroyed'`, repositoryID, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, failure("E_WORKSPACE_NOT_FOUND", "no live workspace is recorded for that path")
	}
	if err != nil {
		return Workspace{}, err
	}
	return s.Workspace(ctx, id)
}
