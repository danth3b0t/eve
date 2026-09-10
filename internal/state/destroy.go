package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"eve/internal/domain"
	"github.com/google/uuid"
)

// DestroyStep reports a local-only destroy cycle. Git identity and the original
// operation reference are evidence checked by lifecycle, never deletion consent.
type DestroyStep struct {
	Workspace         Workspace
	Identity          domain.GitIdentity
	CreateOperationID string
	OperationID       string
	State             string // ready, inflight, cleanup_pending
}

func readDestroyStep(ctx context.Context, tx *sql.Tx, id string) (DestroyStep, error) {
	var step DestroyStep
	w, err := scanWorkspace(tx.QueryRowContext(ctx, workspaceQuery, id))
	if err != nil {
		return step, err
	}
	step.Workspace = w
	var git string
	err = tx.QueryRowContext(ctx, `SELECT o.id,s.outcome_metadata_json FROM operations o JOIN operation_steps s ON s.operation_id=o.id AND s.sequence=1 AND s.action='git_worktree' WHERE o.workspace_id=? AND o.command='create' AND s.state='succeeded'`, id).Scan(&step.CreateOperationID, &git)
	if err != nil || json.Unmarshal([]byte(git), &step.Identity) != nil || !validID(step.CreateOperationID) || step.Identity.Path != w.Path || step.Identity.PathIdentity == "" || step.Identity.AdminIdentity == "" {
		return step, failure("E_STATE_INTENT", "recorded Git identity is unavailable for destruction")
	}
	step.State = "ready"
	if w.State == "prepared" && w.Phase == "complete" && w.Generation >= 1 && w.OperationID == "" {
		return step, nil
	}
	if w.State == "cleanup_pending" && w.Phase == "claim_release" {
		step.State = "cleanup_pending"
		return step, nil
	}
	if w.State != "destroying" || w.Phase != "remove_local" || w.OperationID == "" {
		return step, failure("E_DESTROY_STATE", "workspace is not eligible for the implemented local destroy path")
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE id=? AND workspace_id=? AND command='destroy' AND state='inflight' AND phase='remove_local'`, w.OperationID, id).Scan(&step.OperationID)
	if err != nil {
		return step, failure("E_STATE_INTENT", "destroy operation state is incomplete")
	}
	step.State = "inflight"
	return step, nil
}
func (w *LockedWorkspace) DestroyStep(ctx context.Context) (DestroyStep, error) {
	var step DestroyStep
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error { var err error; step, err = readDestroyStep(ctx, tx, w.id); return err })
	})
	return step, err
}

// OwnedFile returns only ownership metadata needed to compare a current tracked
// file with the exact published postimage. It never returns its content/value.
func (w *LockedWorkspace) OwnedFile(ctx context.Context, name string) (hmac string, mode uint32, err error) {
	err = w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT published_content_hmac,mode FROM managed_files WHERE workspace_id=? AND path=? AND is_tracked=1 AND published_generation=(SELECT generation FROM workspaces WHERE id=?)`, w.id, name, w.id).Scan(&hmac, &mode)
		})
	})
	return hmac, mode, err
}

// StartDestroy commits a new durable local-removal operation BEFORE any deletion.
// Consent, Git ownership, file drift and endpoint observations are caller gates.
func (w *LockedWorkspace) StartDestroy(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readDestroyStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "ready" {
				return failure("E_DESTROY_STATE", "reconcile the existing destroy operation")
			}
			operationID := uuid.NewString()
			now := time.Now().UnixMilli()
			intent, _ := json.Marshal(struct {
				Workspace, Branch, Path string
				Generation              int
			}{w.id, step.Workspace.Branch, step.Workspace.Path, step.Workspace.Generation}) // nonsecret metadata
			if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,workspace_id,command,state,phase,intent_json,created_at_ms,updated_at_ms) VALUES(?,?,'destroy','inflight','remove_local',?,?,?)`, operationID, w.id, string(intent), now, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state,request_metadata_json) VALUES(?,0,'remove_local',?,'pending',?)`, operationID, w.id, string(intent)); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET state='destroying',phase='remove_local',updated_at_ms=? WHERE id=?`, now, w.id)
			return err
		})
	})
}

// RecordCleanupPending preserves an already removed local tree while its port
// claims remain deliberately held for a later assessed cleanup.
func (w *LockedWorkspace) RecordCleanupPending(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readDestroyStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "inflight" {
				return failure("E_DESTROY_STATE", "no local removal is awaiting cleanup")
			}
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',finished_at_ms=? WHERE operation_id=? AND sequence=0`, now, step.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operations SET state='failed',phase='cleanup_pending',updated_at_ms=?,finished_at_ms=? WHERE id=?`, now, now, step.OperationID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET state='cleanup_pending',phase='claim_release',diagnostic_code='E_CLEANUP_PENDING',updated_at_ms=? WHERE id=?`, now, w.id)
			return err
		})
	})
}

// RecordDestroyed releases live allocation references only after the caller has
// confirmed checkout/admin absence and endpoint availability. A small workspace
// tombstone and ownership audit remain. Delete order respects endpoint FKs.
func (w *LockedWorkspace) RecordDestroyed(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readDestroyStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "inflight" && step.State != "cleanup_pending" {
				return failure("E_DESTROY_STATE", "no removal result is ready to finalize")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM endpoints WHERE workspace_id=?`, w.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM port_claims WHERE workspace_id=?`, w.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM port_blocks WHERE workspace_id=?`, w.id); err != nil {
				return err
			}
			now := time.Now().UnixMilli()
			if step.State == "inflight" {
				if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',finished_at_ms=? WHERE operation_id=? AND sequence=0`, now, step.OperationID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE operations SET state='succeeded',phase='complete',updated_at_ms=?,finished_at_ms=? WHERE id=?`, now, now, step.OperationID); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET state='destroyed',phase='destroyed',diagnostic_code=NULL,updated_at_ms=?,destroyed_at_ms=? WHERE id=?`, now, now, w.id)
			return err
		})
	})
}
