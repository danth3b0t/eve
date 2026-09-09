package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"eve/internal/config"
	"eve/internal/platform"
	"github.com/google/uuid"
)

// LockedWorkspace is a capability for one long-running workspace operation.
// Keep it until the lifecycle command finishes; acquire repository locks next,
// and only then short SQL transactions. Close never releases port claims.
type LockedWorkspace struct {
	mu    sync.Mutex
	store *Store
	id    string
	lock  *platform.Lock
}

func (s *Store) LockWorkspace(id string) (*LockedWorkspace, error) {
	if !validID(id) {
		return nil, failure("E_ID_INVALID", "a full UUIDv4 is required")
	}
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	if s.readOnly {
		return nil, failure("E_STATE_READ_ONLY", "registry is open read-only; use a mutating operation context")
	}
	l, err := platform.TryLock(filepath.Join(s.root, "locks", "workspace-"+id+".lock"))
	if errors.Is(err, platform.ErrLocked) {
		return nil, failure("E_WORKSPACE_BUSY", "another EVE operation holds this workspace lock")
	}
	if err != nil {
		return nil, err
	}
	return &LockedWorkspace{store: s, id: id, lock: l}, nil
}

func (w *LockedWorkspace) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lock == nil {
		return nil
	}
	err := w.lock.Close()
	w.lock = nil
	return err
}

func (w *LockedWorkspace) withLock(fn func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lock == nil {
		return failure("E_WORKSPACE_LOCK", "workspace operation lock is closed")
	}
	return fn()
}

type CreateRequest struct {
	RepositoryID, Branch, Path, HeadOID string
	Manifest                            []byte `json:"-"` // committed target TOML, not source dotenv
	Ports                               config.UserConfig
	NewBranch                           bool
}

type Workspace struct {
	ID, RepositoryID, Branch, Path, HeadOID string
	State, Phase, OperationID               string
	ManifestSHA256                          string
	Generation                              int
	CreatedAtMS                             int64
	Manifest                                config.Manifest `json:"-"`
}

type createIntent struct {
	Min, Max, Size int
	NewBranch      bool
}

// BeginCreate commits the immutable local intent before Git/provider effects.
// It cannot mark anything prepared and does not claim an existing directory.
// The lifecycle caller must already have validated Git refs, target manifest,
// input files, filesystem identities and approval.
func (w *LockedWorkspace) BeginCreate(ctx context.Context, in CreateRequest) (string, error) {
	var operationID string
	err := w.withLock(func() error {
		if !validID(in.RepositoryID) || !filepath.IsAbs(in.Path) || filepath.Clean(in.Path) != in.Path || in.Branch == "" || strings.HasPrefix(in.Branch, "-") || strings.ContainsRune(in.Branch, 0) {
			return failure("E_INTENT_INVALID", "invalid repository, branch or absolute workspace path")
		}
		oid, err := hex.DecodeString(in.HeadOID)
		if err != nil || (len(oid) != 20 && len(oid) != 32) {
			return failure("E_INTENT_INVALID", "a resolved full Git commit ID is required")
		}
		m, err := config.Parse(in.Manifest)
		if err != nil {
			return err
		}
		if in.Ports.MinPort < 1 || in.Ports.MinPort > in.Ports.MaxPort || in.Ports.MaxPort > 65535 || (len(m.Endpoints()) != 0 && m.Workspace.PortBlockSize > in.Ports.MaxPort-in.Ports.MinPort+1) {
			return failure("E_PORT_RANGE", "workspace block must fit the configured user port range")
		}
		r, err := w.store.Repository(ctx, in.RepositoryID)
		if err != nil {
			return err
		}
		if in.Path == r.SourcePath || in.Path == r.CommonDir {
			return failure("E_INTENT_INVALID", "workspace path must differ from the source and common directory")
		}
		manifestJSON, err := json.Marshal(m)
		if err != nil {
			return failure("E_INTENT_INVALID", "manifest snapshot could not be encoded")
		}
		intentJSON, _ := json.Marshal(createIntent{in.Ports.MinPort, in.Ports.MaxPort, m.Workspace.PortBlockSize, in.NewBranch})
		hash := sha256.Sum256(in.Manifest)
		operationID = uuid.NewString()
		createdAt := time.Now()
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO workspaces(id,repository_id,branch,path,head_oid,manifest_json,manifest_sha256,state,phase,created_at_ms,updated_at_ms) VALUES(?,?,?,?,?,?,?,'creating','reserve',?,?)`,
				w.id, in.RepositoryID, in.Branch, in.Path, in.HeadOID, string(manifestJSON), hex.EncodeToString(hash[:]), createdAt.UnixMilli(), createdAt.UnixMilli())
			if err != nil {
				return err
			}
			if err := insertResourceRows(ctx, tx, w.id, *m, createdAt); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,workspace_id,command,state,phase,intent_json,created_at_ms,updated_at_ms) VALUES(?,?,'create','pending','reserve',?,?,?)`, operationID, w.id, string(intentJSON), createdAt.UnixMilli(), createdAt.UnixMilli())
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state,request_metadata_json) VALUES(?,0,'reserve_ports',?,'pending',?)`, operationID, w.id, string(intentJSON))
			return err
		})
	})
	if err != nil {
		return "", err
	}
	return operationID, nil
}

const workspaceQuery = `SELECT w.id,w.repository_id,w.branch,w.path,w.head_oid,w.state,w.phase,w.manifest_sha256,w.generation,w.created_at_ms,w.manifest_json,coalesce((SELECT id FROM operations o WHERE o.workspace_id=w.id AND o.state NOT IN ('succeeded','cancelled')),'') FROM workspaces w WHERE w.id=?`

func scanWorkspace(row *sql.Row) (Workspace, error) {
	var w Workspace
	var manifest string
	err := row.Scan(&w.ID, &w.RepositoryID, &w.Branch, &w.Path, &w.HeadOID, &w.State, &w.Phase, &w.ManifestSHA256, &w.Generation, &w.CreatedAtMS, &manifest, &w.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return w, failure("E_WORKSPACE_NOT_FOUND", "workspace is not recorded in this registry")
	}
	if err != nil {
		return w, err
	}
	if err := json.Unmarshal([]byte(manifest), &w.Manifest); err != nil {
		return Workspace{}, failure("E_STATE_INTENT", "stored manifest snapshot is invalid")
	}
	return w, nil
}

func (s *Store) Workspace(ctx context.Context, id string) (Workspace, error) {
	if err := s.checkStorage(); err != nil {
		return Workspace{}, err
	}
	w, err := scanWorkspace(s.db.QueryRowContext(ctx, workspaceQuery, id))
	return w, dbError(err)
}
