// Package state owns protected SQLite metadata. It never invokes Git, probes
// sockets, publishes application files, or authorizes provider operations.
package state

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"eve/internal/domain"
	"eve/internal/platform"
	"eve/schemas"
	"modernc.org/sqlite"
)

type Store struct {
	db         *sql.DB
	root       string
	readOnly   bool
	identities map[string]os.FileInfo
}

func failure(code, message string) error {
	return &domain.Error{Code: code, Message: message}
}

func dbError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var safe *domain.Error
	if errors.As(err, &safe) {
		return err
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 255 {
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return failure("E_STATE_BUSY", "state transaction is busy; retry the operation")
		case 19: // SQLITE_CONSTRAINT; the raw message can contain SQL/values.
			return failure("E_STATE_CONFLICT", "state uniqueness or ownership constraint rejected the operation")
		}
	}
	return failure("E_STATE_DB", "state database operation failed; retain the registry for inspection")
}

// Open initializes an empty database or opens exactly the supported version.
// This is a mutating opener, not the future list/plan read-only command path.
// State roots are per-user and must not be inside a repository (the lifecycle
// layer must check that relationship before calling Open).
func Open(ctx context.Context, path string) (_ *Store, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := platform.PrivateDir(path)
	if err != nil {
		return nil, err
	}
	if err := platform.RequireLocalFilesystem(root); err != nil {
		return nil, err
	}
	s := &Store{root: root, identities: make(map[string]os.FileInfo)}
	for _, name := range []string{"", "locks", "secrets", "pending"} {
		p := filepath.Join(root, name)
		if _, err := platform.PrivateDir(p); err != nil {
			return nil, err
		}
		info, err := platform.CheckPrivate(p, true)
		if err != nil {
			return nil, err
		}
		s.identities[p] = info
	}
	// Initialization is short and serialized across processes, including a fresh
	// database. Workspace locks, unlike this initialization lock, never wait.
	initCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := waitForInit(initCtx, filepath.Join(root, "locks", "schema.lock"))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	defer lock.Close()
	dbPath := filepath.Join(root, "state.sqlite")
	info, err := platform.CreatePrivateFile(dbPath)
	if err != nil {
		return nil, err
	}
	s.identities[dbPath] = info
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: dbPath}
	q := url.Values{
		"mode": {"rw"}, "_txlock": {"immediate"},
		"_foreign_keys": {"on"}, "_synchronous": {"full"}, "_busy_timeout": {"5000"},
		"_defensive": {"true"}, "_dqs": {"false"}, "_pragma": {"trusted_schema(OFF)"},
	}
	u.RawQuery = q.Encode()
	s.db, err = sql.Open("sqlite", u.String())
	if err != nil {
		return nil, dbError(err)
	}
	defer func() {
		if err != nil {
			s.db.Close()
		}
	}()
	// Each store needs one connection; the DSN reapplies connection-local
	// pragmas if database/sql ever replaces it. Independent processes/stores
	// coordinate via BEGIN IMMEDIATE and unique constraints, not this pool cap.
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	if err := s.initialize(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	return s, nil
}

func waitForInit(ctx context.Context, path string) (*platform.Lock, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := platform.TryLock(path)
		if !errors.Is(err, platform.ErrLocked) {
			return lock, err
		}
		select {
		case <-ctx.Done():
			return nil, failure("E_STATE_BUSY", "state initialization lock is busy")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s *Store) initialize(ctx context.Context) error {
	var tables int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return dbError(err)
	}
	if tables == 0 {
		if _, err := s.db.ExecContext(ctx, schemas.StateSQL); err != nil {
			return dbError(err)
		}
	} else {
		var applicationID int
		if err := s.db.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil || applicationID != 1163281713 {
			return failure("E_STATE_SCHEMA", "database is not an EVE registry; do not overwrite it")
		}
		var version, count int
		err := s.db.QueryRowContext(ctx, `SELECT max(version), count(*) FROM schema_migrations`).Scan(&version, &count)
		if err != nil || version != schemas.StateVersion || count != 1 {
			return failure("E_STATE_SCHEMA", "unsupported state schema; use a compatible EVE version, not a new registry")
		}
	}
	// Do not change persistent journal mode before rejecting newer/unknown
	// schemas. Connection-local FULL/FK/busy settings are already in the DSN.
	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		return dbError(err)
	}
	if mode != "wal" {
		return failure("E_STATE_FILESYSTEM", "SQLite WAL mode is required")
	}
	return nil
}

func (s *Store) checkStorage() error {
	if err := platform.CheckStateAncestors(s.root); err != nil {
		return err
	}
	for path, original := range s.identities {
		info, err := platform.CheckPrivate(path, original.IsDir())
		if err != nil {
			return &domain.Error{Code: "E_STATE_IDENTITY", Message: "state object is missing or no longer private", Path: path}
		}
		if !os.SameFile(info, original) {
			return &domain.Error{Code: "E_STATE_IDENTITY", Message: "state object was replaced or moved", Path: path}
		}
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_, err := platform.CheckPrivate(filepath.Join(s.root, "state.sqlite"+suffix), false)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return dbError(s.db.Close()) }

// transaction must only perform bounded SQLite work. Never call Git, a socket
// prober, a provider, or another workspace lock from inside the callback.
func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := s.checkStorage(); err != nil {
		if s.readOnly {
			return failure("E_STATE_READ_ONLY", "registry is open read-only")
		}
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil) // _txlock=immediate, on every connection.
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return dbError(err)
	}
	return dbError(tx.Commit())
}
