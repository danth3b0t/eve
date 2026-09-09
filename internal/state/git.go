package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"eve/internal/domain"
)

type GitStep struct {
	Workspace Workspace
	NewBranch bool
	State     string // pending, inflight, unknown, succeeded
	Identity  *domain.GitIdentity
}

func readGitStep(ctx context.Context, tx *sql.Tx, id string) (GitStep, error) {
	var result GitStep
	w, err := scanWorkspace(tx.QueryRowContext(ctx, workspaceQuery, id))
	if err != nil {
		return result, err
	}
	if (w.State != "creating" && w.State != "failed") || (w.Phase != "worktree" && w.Phase != "stage") {
		return result, failure("E_GIT_STEP_STATE", "workspace is not awaiting its recorded Git creation step")
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT intent_json FROM operations WHERE id=? AND command='create' AND state IN ('pending','inflight','failed','unknown')`, w.OperationID).Scan(&raw); err != nil {
		return result, err
	}
	var intent createIntent
	if err := json.Unmarshal([]byte(raw), &intent); err != nil {
		return result, failure("E_STATE_INTENT", "creation intent is invalid")
	}
	result.Workspace, result.NewBranch, result.State = w, intent.NewBranch, "pending"
	err = tx.QueryRowContext(ctx, `SELECT state,outcome_metadata_json FROM operation_steps WHERE operation_id=? AND sequence=1 AND action='git_worktree'`, w.OperationID).Scan(&result.State, &raw)
	if errors.Is(err, sql.ErrNoRows) && w.Phase == "worktree" {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if result.State == "succeeded" {
		var identity domain.GitIdentity
		if json.Unmarshal([]byte(raw), &identity) != nil || identity.PathIdentity == "" || identity.AdminIdentity == "" {
			return result, failure("E_STATE_INTENT", "recorded Git identity is invalid")
		}
		result.Identity = &identity
	}
	return result, nil
}

func (w *LockedWorkspace) GitStep(ctx context.Context) (GitStep, error) {
	var step GitStep
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			var err error
			step, err = readGitStep(ctx, tx, w.id)
			return err
		})
	})
	return step, err
}

// StartGit commits the inflight intent before ANY Git mutation. It is not a
// lease and must not be interpreted as proof that the subprocess ran/failed.
func (w *LockedWorkspace) StartGit(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readGitStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "pending" {
				return failure("E_GIT_STEP_STATE", "reconcile the existing Git attempt instead of repeating it")
			}
			now := time.Now().UnixMilli()
			_, err = tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state,started_at_ms) VALUES(?,1,'git_worktree',?,'inflight',?)`, step.Workspace.OperationID, w.id, now)
			return err
		})
	})
}

// RecordGit persists the real identity before a caller removes the temporary
// native Git creation lock. It never marks the workspace prepared.
func (w *LockedWorkspace) RecordGit(ctx context.Context, identity domain.GitIdentity) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readGitStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "inflight" && step.State != "unknown" {
				return failure("E_GIT_STEP_STATE", "no outstanding Git creation intent")
			}
			var common, commonIdentity string
			if err := tx.QueryRowContext(ctx, `SELECT common_dir,common_identity FROM repositories WHERE id=?`, step.Workspace.RepositoryID).Scan(&common, &commonIdentity); err != nil {
				return err
			}
			if identity.Path != step.Workspace.Path || identity.CommonDir != common || identity.CommonIdentity != commonIdentity || identity.PathIdentity == "" || identity.AdminIdentity == "" || filepath.Dir(identity.AdminDir) != filepath.Join(common, "worktrees") {
				return failure("E_GIT_IDENTITY", "Git observation does not match the recorded workspace/repository intent")
			}
			encoded, _ := json.Marshal(identity) // only paths and filesystem identities
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',outcome_metadata_json=?,finished_at_ms=?,error_code=NULL WHERE operation_id=? AND sequence=1`, string(encoded), now, step.Workspace.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operations SET phase='stage',state='inflight',updated_at_ms=? WHERE id=?`, now, step.Workspace.OperationID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET git_admin_dir=?,phase='stage',state='creating',diagnostic_code=NULL,updated_at_ms=? WHERE id=?`, identity.AdminDir, now, w.id)
			return err
		})
	})
}

func (w *LockedWorkspace) GitUnknown(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readGitStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "inflight" && step.State != "unknown" {
				return failure("E_GIT_STEP_STATE", "no inflight Git attempt to reconcile")
			}
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='unknown',error_code='E_GIT_RECONCILE' WHERE operation_id=? AND sequence=1`, step.Workspace.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operations SET state='unknown',updated_at_ms=? WHERE id=?`, now, step.Workspace.OperationID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET state='failed',diagnostic_code='E_GIT_RECONCILE',updated_at_ms=? WHERE id=?`, now, w.id)
			return err
		})
	})
}

func (s *Store) ScratchDir() (string, error) {
	if err := s.checkStorage(); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "pending"), nil
}

// RepositoryForCommon uses filesystem identity as well as canonical spelling.
// It always returns the registered source, not the invoking linked worktree.
func (s *Store) RepositoryForCommon(ctx context.Context, common, identity string) (Repository, error) {
	if err := s.checkStorage(); err != nil {
		return Repository{}, err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM repositories WHERE common_dir=? OR common_identity=?`, common, identity).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Repository{}, failure("E_SOURCE_UNREGISTERED", "explicitly register the canonical source checkout first")
	}
	if err != nil {
		return Repository{}, dbError(err)
	}
	r, err := s.Repository(ctx, id)
	if err != nil {
		return Repository{}, err
	}
	if r.CommonIdentity != identity {
		return Repository{}, failure("E_SOURCE_IDENTITY", "invoking repository differs from the registered identity")
	}
	return r, nil
}
