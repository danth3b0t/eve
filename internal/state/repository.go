package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	"eve/internal/platform"
	"github.com/google/uuid"
)

type Repository struct {
	ID, CommonDir, SourcePath, Label string
	CommonIdentity, SourceIdentity   string
}

// RegisterRepository records an explicitly selected source, never an implicit
// replacement. The caller must first establish these paths through Git and
// obtain registration consent; a directory alone is not proof of a repository.
func (s *Store) RegisterRepository(ctx context.Context, common, source, label string) (Repository, error) {
	var r Repository
	var err error
	r.CommonDir, r.CommonIdentity, err = platform.DirectoryIdentity(common)
	if err != nil {
		return r, err
	}
	r.SourcePath, r.SourceIdentity, err = platform.DirectoryIdentity(source)
	if err != nil {
		return r, err
	}
	r.ID, r.Label = uuid.NewString(), label
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		previous, err := scanRepository(tx.QueryRowContext(ctx, `SELECT id, common_dir, source_path, label, common_identity, source_identity FROM repositories WHERE common_dir=? OR common_identity=?`, r.CommonDir, r.CommonIdentity))
		if err == nil {
			if previous.CommonIdentity != r.CommonIdentity || previous.SourceIdentity != r.SourceIdentity {
				return failure("E_SOURCE_IDENTITY", "registered source differs or moved; explicit reviewed repair is required")
			}
			r = previous
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO repositories(id,common_dir,source_path,label,common_identity,source_identity,created_at_ms) VALUES(?,?,?,?,?,?,?)`,
			r.ID, r.CommonDir, r.SourcePath, r.Label, r.CommonIdentity, r.SourceIdentity, time.Now().UnixMilli())
		return err
	})
	if err != nil {
		return Repository{}, err
	}
	// Preserve the registered spelling/path for aliases of the same inode, but
	// require the old path still to exist. A move is not silent re-registration.
	return s.Repository(ctx, r.ID)
}

func scanRepository(row *sql.Row) (Repository, error) {
	var r Repository
	err := row.Scan(&r.ID, &r.CommonDir, &r.SourcePath, &r.Label, &r.CommonIdentity, &r.SourceIdentity)
	return r, err
}

func (s *Store) Repository(ctx context.Context, id string) (Repository, error) {
	if err := s.checkStorage(); err != nil {
		return Repository{}, err
	}
	r, err := scanRepository(s.db.QueryRowContext(ctx, `SELECT id,common_dir,source_path,label,common_identity,source_identity FROM repositories WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, failure("E_SOURCE_UNREGISTERED", "register the source checkout with init first")
	}
	if err != nil {
		return r, dbError(err)
	}
	for _, pair := range [][2]string{{r.CommonDir, r.CommonIdentity}, {r.SourcePath, r.SourceIdentity}} {
		canonical, identity, err := platform.DirectoryIdentity(pair[0])
		if err != nil || canonical != pair[0] || identity != pair[1] {
			return Repository{}, failure("E_SOURCE_IDENTITY", "registered source/common directory moved or was replaced; explicit repair is required")
		}
	}
	return r, nil
}

func (s *Store) LockRepository(ctx context.Context, id string) (*platform.Lock, error) {
	if !validID(id) {
		return nil, failure("E_ID_INVALID", "a full UUIDv4 is required")
	}
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, "locks", "repository-"+id+".lock")
	deadline := time.Now().Add(5 * time.Minute)
	for {
		l, err := platform.TryLock(path)
		if !errors.Is(err, platform.ErrLocked) {
			return l, err
		}
		if !time.Now().Before(deadline) {
			return nil, failure("E_REPOSITORY_BUSY", "another EVE repository Git mutation is in progress")
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func validID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.Version() == 4 && u.Variant() == uuid.RFC4122 && u.String() == id
}
