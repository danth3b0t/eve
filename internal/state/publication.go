package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"eve/internal/domain"
)

type PublicationFile struct {
	FileRecord
	State   string
	Receipt *domain.FileIdentity
}
type Publication struct {
	FileStep
	Started bool
	Files   []PublicationFile
}

func readPublication(ctx context.Context, tx *sql.Tx, id string) (Publication, error) {
	step, err := readFileStep(ctx, tx, id)
	if err != nil {
		return Publication{}, err
	}
	p := Publication{FileStep: step}
	if step.State != "succeeded" {
		return p, failure("E_PUBLICATION_STATE", "all images must be staged before publication")
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT state FROM operation_steps WHERE operation_id=? AND sequence=3 AND action='publish_files'`, step.Workspace.OperationID).Scan(&status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.Started = err == nil
	if (!p.Started && step.Workspace.State == "prepared") || (p.Started && status != "inflight" && status != "succeeded") {
		return p, failure("E_PUBLICATION_STATE", "invalid publication journal")
	}
	for i, f := range step.Intent.Files {
		file := PublicationFile{FileRecord: f, State: "pending"}
		if p.Started {
			var raw string
			if err := tx.QueryRowContext(ctx, `SELECT state,outcome_metadata_json FROM operation_steps WHERE operation_id=? AND sequence=? AND action='publish_file' AND subject=?`, step.Workspace.OperationID, 4+i, f.Path).Scan(&file.State, &raw); err != nil {
				return p, err
			}
			if raw != "{}" {
				if json.Unmarshal([]byte(raw), &file.Receipt) != nil || file.Receipt == nil || !fileIdentity.MatchString(file.Receipt.Identity) || file.Receipt.Mode != 0600 || file.Receipt.Size != f.Size {
					return p, failure("E_PUBLICATION_STATE", "invalid publication receipt")
				}
			}
			if file.State != "pending" && file.State != "inflight" && file.State != "succeeded" {
				return p, failure("E_PUBLICATION_STATE", "invalid per-file publication state")
			}
			if (file.State == "pending") != (file.Receipt == nil) {
				return p, failure("E_PUBLICATION_STATE", "publication state disagrees with its receipt")
			}
		}
		p.Files = append(p.Files, file)
	}
	return p, nil
}
func (w *LockedWorkspace) Publication(ctx context.Context) (Publication, error) {
	var p Publication
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error { var err error; p, err = readPublication(ctx, tx, w.id); return err })
	})
	return p, err
}

// StartPublication records sibling-temp intents before directory/file mutation.
// The deterministic temp name is derived from each immutable staged reference.
func (w *LockedWorkspace) StartPublication(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			p, err := readPublication(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if p.Started {
				return failure("E_PUBLICATION_STATE", "reconcile the existing publication attempt")
			}
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state,started_at_ms) VALUES(?,3,'publish_files',?,'inflight',?)`, p.Workspace.OperationID, w.id, now); err != nil {
				return err
			}
			for i, f := range p.Files {
				if _, err := tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state) VALUES(?,?,'publish_file',?,'pending')`, p.Workspace.OperationID, 4+i, f.Path); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// RecordTemporary persists a complete, synced sibling's identity BEFORE rename.
func (w *LockedWorkspace) RecordTemporary(ctx context.Context, name string, receipt domain.FileIdentity) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			p, err := readPublication(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if !p.Started || p.Workspace.State != "creating" {
				return failure("E_PUBLICATION_STATE", "no active publication attempt")
			}
			for i, f := range p.Files {
				if f.Path == name {
					if f.State != "pending" || !fileIdentity.MatchString(receipt.Identity) || receipt.Size != f.Size || receipt.Mode != 0600 {
						return failure("E_PUBLICATION_STATE", "invalid new temporary receipt")
					}
					raw, _ := json.Marshal(receipt)
					_, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='inflight',outcome_metadata_json=?,started_at_ms=? WHERE operation_id=? AND sequence=?`, string(raw), time.Now().UnixMilli(), p.Workspace.OperationID, 4+i)
					return err
				}
			}
			return failure("E_PUBLICATION_STATE", "file is not part of this publication")
		})
	})
}
func (w *LockedWorkspace) RecordPublished(ctx context.Context, name string) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			p, err := readPublication(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if !p.Started || p.Workspace.State != "creating" {
				return failure("E_PUBLICATION_STATE", "no active publication attempt")
			}
			for i, f := range p.Files {
				if f.Path == name {
					if f.State != "inflight" {
						return failure("E_PUBLICATION_STATE", "file has no outstanding rename receipt")
					}
					if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',finished_at_ms=? WHERE operation_id=? AND sequence=?`, time.Now().UnixMilli(), p.Workspace.OperationID, 4+i); err != nil {
						return err
					}
					result, err := tx.ExecContext(ctx, `UPDATE file_transactions SET state='published' WHERE operation_id=? AND path=? AND state='staged'`, p.Workspace.OperationID, name)
					if err != nil {
						return err
					}
					n, err := result.RowsAffected()
					if err != nil {
						return err
					}
					if n != 1 {
						return failure("E_PUBLICATION_STATE", "staged file journal is missing or inconsistent")
					}
					return nil
				}
			}
			return failure("E_PUBLICATION_STATE", "file is not part of this publication")
		})
	})
}

// CompletePublication is called only after fresh verification of EVERY final
// file and endpoint. Filesystem observations occur outside this short transaction.
// Owners come from resolution of the frozen manifest, not dotenv values.
func (w *LockedWorkspace) CompletePublication(ctx context.Context, owners map[string]map[string][]string) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			p, err := readPublication(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if !p.Started || p.Workspace.State != "creating" {
				return failure("E_PUBLICATION_STATE", "no active publication attempt")
			}
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM file_transactions WHERE operation_id=? AND state='published'`, p.Workspace.OperationID).Scan(&count); err != nil {
				return err
			}
			if count != len(p.Files) {
				return failure("E_PUBLICATION_STATE", "not every journaled file was published")
			}
			for _, f := range p.Files {
				if f.State != "succeeded" || len(owners[f.Path]) != len(f.Values) {
					return failure("E_PUBLICATION_STATE", "publication or key ownership is incomplete")
				}
				// Unknown source provenance stays NULL; do not infer whole-file ownership.
				if _, err := tx.ExecContext(ctx, `INSERT INTO managed_files(workspace_id,path,is_tracked,is_generated_only,copy_source_path,published_content_hmac,published_generation,mode) VALUES(?,?,?,0,NULL,?,1,?)`, w.id, f.Path, f.Tracked, f.StagedHMAC, f.Mode); err != nil {
					return err
				}
				for key, mac := range f.Values {
					if len(owners[f.Path][key]) == 0 {
						return failure("E_PUBLICATION_STATE", "managed key ownership is missing")
					}
					raw, _ := json.Marshal(owners[f.Path][key])
					// Literal manifest values are not automatically public.
					if _, err := tx.ExecContext(ctx, `INSERT INTO managed_values(workspace_id,path,env_key,owners_json,value_hmac,sensitivity) VALUES(?,?,?,?,?,'private')`, w.id, f.Path, key, string(raw), mac); err != nil {
						return err
					}
				}
			}
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `UPDATE file_transactions SET state='verified' WHERE operation_id=? AND state='published'`, p.Workspace.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',finished_at_ms=? WHERE operation_id=? AND sequence=3`, now, p.Workspace.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operations SET state='succeeded',phase='complete',finished_at_ms=?,updated_at_ms=? WHERE id=?`, now, now, p.Workspace.OperationID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET state='prepared',phase='complete',generation=1,updated_at_ms=?,diagnostic_code=NULL WHERE id=?`, now, w.id)
			return err
		})
	})
}
func (w *LockedWorkspace) RecordImagesPurged(ctx context.Context, name string) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			p, err := readPublication(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if p.Workspace.State != "prepared" {
				return failure("E_PUBLICATION_STATE", "retain snapshots until durable completion")
			}
			result, err := tx.ExecContext(ctx, `UPDATE file_transactions SET state='purged' WHERE operation_id=? AND path=? AND state IN ('verified','purged')`, p.Workspace.OperationID, name)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return failure("E_PUBLICATION_STATE", "file snapshots are not eligible for cleanup")
			}
			return nil
		})
	})
}
