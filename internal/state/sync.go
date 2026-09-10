package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"eve/internal/domain"
	"github.com/google/uuid"
)

// SyncStep is a bounded value-sync generation journal. It is separate from the
// completed create operation and carries only fingerprints/references/metadata.
type SyncStep struct {
	Workspace          Workspace
	Identity           domain.GitIdentity
	OperationID        string
	State              string // pending, images_inflight, staged, publish_inflight
	Intent             FileIntent
	PublicationStarted bool
	Files              []PublicationFile
}
type ManagedValue struct {
	Path, EnvKey, HMAC string
	Owners             []string
	Sensitivity        string
}

func readSyncStep(ctx context.Context, tx *sql.Tx, id string) (SyncStep, error) {
	var step SyncStep
	w, err := scanWorkspace(tx.QueryRowContext(ctx, workspaceQuery, id))
	if err != nil {
		return step, err
	}
	if !((w.State == "prepared" && w.Generation >= 1) || w.State == "syncing") {
		return step, failure("E_SYNC_STATE", "sync requires a completed prepared generation")
	}
	step.Workspace = w
	var git string
	if err := tx.QueryRowContext(ctx, `SELECT o.id,s.outcome_metadata_json FROM operations o JOIN operation_steps s ON s.operation_id=o.id AND s.sequence=1 AND s.action='git_worktree' AND s.state='succeeded' WHERE o.workspace_id=? AND o.command='create'`, id).Scan(&step.OperationID, &git); err != nil {
		return step, err
	}
	if json.Unmarshal([]byte(git), &step.Identity) != nil || step.Identity.Path != w.Path || step.Identity.PathIdentity == "" || step.Identity.AdminIdentity == "" {
		return step, failure("E_STATE_INTENT", "recorded Git identity is invalid")
	}
	if w.State == "prepared" {
		var latestID, latestState string
		err = tx.QueryRowContext(ctx, `SELECT id,state FROM operations WHERE workspace_id=? AND command='sync' ORDER BY created_at_ms DESC LIMIT 1`, id).Scan(&latestID, &latestState)
		if errors.Is(err, sql.ErrNoRows) {
			step.State = "pending"
			return step, nil
		}
		if err != nil || latestState != "succeeded" {
			return step, failure("E_SYNC_STATE", "an incomplete sync journal requires explicit recovery")
		}
		step.OperationID = latestID
		var request string
		if err = tx.QueryRowContext(ctx, `SELECT request_metadata_json FROM operation_steps WHERE operation_id=? AND sequence=20 AND action='sync_images' AND state='succeeded'`, latestID).Scan(&request); err != nil {
			return step, err
		}
		if len(request) > 16<<20 || json.Unmarshal([]byte(request), &step.Intent) != nil || validateFiles(step.Intent) != nil {
			return step, failure("E_STATE_INTENT", "sync image intent is invalid")
		}
		var outstanding int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM file_transactions WHERE operation_id=? AND state<>'purged'`, latestID).Scan(&outstanding); err != nil {
			return step, err
		}
		if outstanding == 0 {
			step.State = "pending"
			step.Intent = FileIntent{}
			return step, nil
		}
		step.State = "complete"
		step.PublicationStarted = true
		for _, f := range step.Intent.Files {
			step.Files = append(step.Files, PublicationFile{FileRecord: f, State: "succeeded"})
		}
		return step, nil
	}
	step.OperationID = w.OperationID
	var request string
	err = tx.QueryRowContext(ctx, `SELECT state,request_metadata_json FROM operation_steps WHERE operation_id=? AND sequence=20 AND action='sync_images'`, w.OperationID).Scan(&step.State, &request)
	if err != nil {
		return step, failure("E_SYNC_STATE", "sync image journal is missing")
	}
	if len(request) > 16<<20 || json.Unmarshal([]byte(request), &step.Intent) != nil || validateFiles(step.Intent) != nil {
		return step, failure("E_STATE_INTENT", "sync image intent is invalid")
	}
	switch step.State {
	case "inflight":
		step.State = "images_inflight"
	case "succeeded":
		step.State = "staged"
	default:
		return step, failure("E_SYNC_STATE", "invalid sync image journal state")
	}
	err = tx.QueryRowContext(ctx, `SELECT state FROM operation_steps WHERE operation_id=? AND sequence=21 AND action='sync_publish'`, w.OperationID).Scan(&step.State)
	if errors.Is(err, sql.ErrNoRows) {
		step.PublicationStarted = false
		return step, nil
	}
	if err != nil {
		return step, err
	}
	if step.State != "inflight" && step.State != "succeeded" {
		return step, failure("E_SYNC_STATE", "invalid sync publication journal")
	}
	step.PublicationStarted = true
	for i, f := range step.Intent.Files {
		current := PublicationFile{FileRecord: f, State: "pending"}
		var receipt string
		if err := tx.QueryRowContext(ctx, `SELECT state,outcome_metadata_json FROM operation_steps WHERE operation_id=? AND sequence=? AND action='sync_publish_file' AND subject=?`, step.OperationID, 300+i, f.Path).Scan(&current.State, &receipt); err != nil {
			return step, err
		}
		if receipt != "{}" {
			if json.Unmarshal([]byte(receipt), &current.Receipt) != nil || current.Receipt == nil || !fileIdentity.MatchString(current.Receipt.Identity) || current.Receipt.Mode != 0600 || current.Receipt.Size != f.Size {
				return step, failure("E_SYNC_STATE", "invalid per-file sync receipt")
			}
		}
		if (current.State == "pending") != (current.Receipt == nil) || (current.State != "pending" && current.State != "inflight" && current.State != "succeeded") {
			return step, failure("E_SYNC_STATE", "invalid per-file sync state")
		}
		step.Files = append(step.Files, current)
	}
	step.State = "publish_inflight"
	return step, nil
}
func (w *LockedWorkspace) SyncStep(ctx context.Context) (SyncStep, error) {
	var step SyncStep
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error { var err error; step, err = readSyncStep(ctx, tx, w.id); return err })
	})
	return step, err
}

// StartSyncImages commits the whole new value/file intent before any sensitive
// image object exists. The returned intent can reconcile only those references.
func (w *LockedWorkspace) StartSyncImages(ctx context.Context, in FileIntent) error {
	if err := validateFiles(in); err != nil {
		return err
	}
	raw, _ := json.Marshal(in)
	if len(raw) > 16<<20 {
		return failure("E_STATE_INTENT", "sync image intent exceeds 16 MiB")
	}
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readSyncStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "pending" {
				return failure("E_SYNC_STATE", "reconcile the existing sync journal instead of restaging")
			}
			var key string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM credential_objects WHERE id=? AND kind='hmac_key' AND deleted_at_ms IS NULL`, in.KeyID).Scan(&key); err != nil {
				return failure("E_HMAC_KEY", "sync key is not registered")
			}
			operationID := uuid.NewString()
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,workspace_id,command,state,phase,intent_json,created_at_ms,updated_at_ms) VALUES(?,?,'sync','inflight','sync_images',?,?,?)`, operationID, w.id, string(raw), now, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state,request_metadata_json,started_at_ms) VALUES(?,20,'sync_images',?,'inflight',?,?)`, operationID, w.id, string(raw), now); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET state='syncing',phase='sync_images',updated_at_ms=? WHERE id=? AND state='prepared'`, now, w.id)
			return err
		})
	})
}
func (w *LockedWorkspace) RecordSyncImages(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readSyncStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "images_inflight" {
				return failure("E_SYNC_STATE", "no outstanding sync images")
			}
			for _, f := range step.Intent.Files {
				var preRef, preMAC any
				if f.Preimage != nil {
					preRef, preMAC = f.PreimageRef, f.PreimageHMAC
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO file_transactions(operation_id,path,preimage_ref,staged_image_ref,preimage_hmac,staged_hmac,desired_mode,state) VALUES(?,?,?,?,?,?,?,'staged')`, step.OperationID, f.Path, preRef, f.StagedRef, preMAC, f.StagedHMAC, f.Mode); err != nil {
					return err
				}
			}
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',finished_at_ms=? WHERE operation_id=? AND sequence=20`, now, step.OperationID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET phase='sync_publish',updated_at_ms=? WHERE id=?`, now, w.id)
			return err
		})
	})
}
func (w *LockedWorkspace) StartSyncPublication(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readSyncStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "staged" || step.PublicationStarted {
				return failure("E_SYNC_STATE", "sync publication cannot start")
			}
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state,started_at_ms) VALUES(?,21,'sync_publish',?,'inflight',?)`, step.OperationID, w.id, now); err != nil {
				return err
			}
			for i, f := range step.Intent.Files {
				if _, err := tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state) VALUES(?,?,'sync_publish_file',?,'pending')`, step.OperationID, 300+i, f.Path); err != nil {
					return err
				}
			}
			return nil
		})
	})
}
func (w *LockedWorkspace) RecordSyncTemporary(ctx context.Context, name string, receipt domain.FileIdentity) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readSyncStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "publish_inflight" {
				return failure("E_SYNC_STATE", "no sync publication is active")
			}
			for i, f := range step.Files {
				if f.Path == name {
					if f.State != "pending" || !fileIdentity.MatchString(receipt.Identity) || receipt.Size != f.Size || receipt.Mode != 0600 {
						return failure("E_SYNC_STATE", "invalid sync temporary receipt")
					}
					raw, _ := json.Marshal(receipt)
					_, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='inflight',outcome_metadata_json=?,started_at_ms=? WHERE operation_id=? AND sequence=?`, string(raw), time.Now().UnixMilli(), step.OperationID, 300+i)
					return err
				}
			}
			return failure("E_SYNC_STATE", "file is not part of sync publication")
		})
	})
}
func (w *LockedWorkspace) RecordSyncPublished(ctx context.Context, name string) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readSyncStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "publish_inflight" {
				return failure("E_SYNC_STATE", "no sync publication is active")
			}
			for i, f := range step.Files {
				if f.Path == name {
					if f.State != "inflight" {
						return failure("E_SYNC_STATE", "sync file has no outstanding rename receipt")
					}
					if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',finished_at_ms=? WHERE operation_id=? AND sequence=?`, time.Now().UnixMilli(), step.OperationID, 300+i); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `UPDATE file_transactions SET state='published' WHERE operation_id=? AND path=? AND state='staged'`, step.OperationID, name)
					return err
				}
			}
			return failure("E_SYNC_STATE", "file is not part of sync publication")
		})
	})
}
func (w *LockedWorkspace) CompleteSync(ctx context.Context, owners map[string]map[string][]string) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readSyncStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "publish_inflight" {
				return failure("E_SYNC_STATE", "no sync publication is ready")
			}
			if step.Intent.Manifest == nil || !fingerprint.MatchString(step.Intent.ManifestSHA256) {
				return failure("E_SYNC_STATE", "applied manifest intent is incomplete")
			}
			applied, err := json.Marshal(step.Intent.Manifest)
			if err != nil {
				return failure("E_SYNC_STATE", "applied manifest could not be encoded")
			}
			for _, f := range step.Files {
				if f.State != "succeeded" || len(owners[f.Path]) != len(f.Values) {
					return failure("E_SYNC_STATE", "sync publication or ownership is incomplete")
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM managed_values WHERE workspace_id=?`, w.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM managed_files WHERE workspace_id=?`, w.id); err != nil {
				return err
			}
			next := step.Workspace.Generation + 1
			for _, f := range step.Files {
				if _, err := tx.ExecContext(ctx, `INSERT INTO managed_files(workspace_id,path,is_tracked,is_generated_only,copy_source_path,published_content_hmac,published_generation,mode) VALUES(?,?,?,0,NULL,?,?,?)`, w.id, f.Path, f.Tracked, f.StagedHMAC, next, f.Mode); err != nil {
					return err
				}
				for key, mac := range f.Values {
					if len(owners[f.Path][key]) == 0 {
						return failure("E_SYNC_STATE", "managed ownership missing")
					}
					raw, _ := json.Marshal(owners[f.Path][key])
					if _, err := tx.ExecContext(ctx, `INSERT INTO managed_values(workspace_id,path,env_key,owners_json,value_hmac,sensitivity) VALUES(?,?,?,?,?,'private')`, w.id, f.Path, key, string(raw), mac); err != nil {
						return err
					}
				}
			}
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `UPDATE file_transactions SET state='verified' WHERE operation_id=? AND state='published'`, step.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',finished_at_ms=? WHERE operation_id=? AND sequence=21`, now, step.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operations SET state='succeeded',phase='complete',updated_at_ms=?,finished_at_ms=? WHERE id=?`, now, now, step.OperationID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET state='prepared',phase='complete',generation=?,applied_manifest_json=?,updated_at_ms=?,diagnostic_code=NULL WHERE id=?`, next, string(applied), now, w.id)
			return err
		})
	})
}
func (w *LockedWorkspace) RecordSyncImagesPurged(ctx context.Context, name string) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readSyncStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.Workspace.State != "prepared" {
				return failure("E_SYNC_STATE", "retain sync snapshots until durable completion")
			}
			n, err := tx.ExecContext(ctx, `UPDATE file_transactions SET state='purged' WHERE operation_id=? AND path=? AND state IN ('verified','purged')`, step.OperationID, name)
			if err != nil {
				return err
			}
			count, err := n.RowsAffected()
			if err != nil || count != 1 {
				return failure("E_SYNC_STATE", "sync snapshots are incomplete")
			}
			return err
		})
	})
}
func (w *LockedWorkspace) ManagedValues(ctx context.Context) ([]ManagedValue, error) {
	rows := []ManagedValue{}
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			result, err := tx.QueryContext(ctx, `SELECT v.path,v.env_key,v.value_hmac,v.owners_json,v.sensitivity FROM managed_values v JOIN managed_files f ON f.workspace_id=v.workspace_id AND f.path=v.path WHERE v.workspace_id=? AND f.published_generation=(SELECT generation FROM workspaces WHERE id=?)`, w.id, w.id)
			if err != nil {
				return err
			}
			defer result.Close()
			for result.Next() {
				var value ManagedValue
				var owners string
				if err := result.Scan(&value.Path, &value.EnvKey, &value.HMAC, &owners, &value.Sensitivity); err != nil {
					return err
				}
				if json.Unmarshal([]byte(owners), &value.Owners) != nil {
					return failure("E_SYNC_STATE", "recorded managed ownership is invalid")
				}
				rows = append(rows, value)
			}
			return result.Err()
		})
	})
	return rows, err
}

type ManagedFile struct {
	Path, HMAC string
	Mode       uint32
	Tracked    bool
}

func (w *LockedWorkspace) ManagedFiles(ctx context.Context) ([]ManagedFile, error) {
	rows := []ManagedFile{}
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			result, err := tx.QueryContext(ctx, `SELECT path,published_content_hmac,mode,is_tracked FROM managed_files WHERE workspace_id=? AND published_generation=(SELECT generation FROM workspaces WHERE id=?)`, w.id, w.id)
			if err != nil {
				return err
			}
			defer result.Close()
			for result.Next() {
				var row ManagedFile
				var tracked int
				if err := result.Scan(&row.Path, &row.HMAC, &row.Mode, &tracked); err != nil {
					return err
				}
				row.Tracked = tracked != 0
				rows = append(rows, row)
			}
			return result.Err()
		})
	})
	return rows, err
}
