package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/private"
)

// FileRecord contains only keyed fingerprints, identities and opaque references.
// No image bytes or managed plaintext values belong in SQLite.
type FileRecord struct {
	Path                      string
	Tracked                   bool
	Preimage                  *domain.FileIdentity
	PreimageRef, PreimageHMAC string
	StagedRef, StagedHMAC     string
	Size                      int64
	Mode                      uint32
	Values                    map[string]string // env key -> HMAC; ownership remains in frozen manifest
}
type FileIntent struct {
	KeyID                   string
	HeadOID, ManifestSHA256 string
	Owners                  map[string]map[string][]string
	Manifest                *config.Manifest
	Files                   []FileRecord
}
type FileStep struct {
	Workspace Workspace
	Identity  domain.GitIdentity
	State     string // pending, inflight, succeeded (staged, NOT published)
	Intent    FileIntent
}

var fingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)
var fileIdentity = regexp.MustCompile(`^[0-9a-f]+:[0-9a-f]+$`)
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateFiles(in FileIntent) error {
	bad := func() error { return failure("E_FILE_INTENT", "invalid bounded file staging metadata") }
	if !validID(in.KeyID) || len(in.Files) > 10000 {
		return bad()
	}
	names, refs := map[string]bool{}, map[string]bool{}
	var before, after int64
	for _, f := range in.Files {
		clean, err := config.RelativePath(".", f.Path)
		if err != nil || clean == "." || clean != f.Path || f.Mode != 0600 || f.Size < 0 || f.Size > private.MaxBytes || names[f.Path] {
			return bad()
		}
		for _, part := range strings.Split(f.Path, "/") {
			if part == ".." || strings.EqualFold(part, ".git") || strings.EqualFold(part, "node_modules") || strings.EqualFold(part, ".convex") {
				return bad()
			}
		}
		names[f.Path] = true
		if !validID(f.StagedRef) || refs[f.StagedRef] || !fingerprint.MatchString(f.StagedHMAC) {
			return bad()
		}
		refs[f.StagedRef] = true
		if f.Preimage == nil {
			if f.Tracked || f.PreimageRef != "" || f.PreimageHMAC != "" {
				return bad()
			}
		} else {
			p := f.Preimage
			if !fileIdentity.MatchString(p.Identity) || !p.Mode.IsRegular() || p.Size < 0 || p.Size > private.MaxBytes || !validID(f.PreimageRef) || refs[f.PreimageRef] || !fingerprint.MatchString(f.PreimageHMAC) {
				return bad()
			}
			refs[f.PreimageRef] = true
			before += p.Size
		}
		after += f.Size
		if before > private.MaxBytes || after > private.MaxBytes {
			return bad()
		}
		for key, mac := range f.Values {
			if !envKey.MatchString(key) || !fingerprint.MatchString(mac) {
				return bad()
			}
		}
	}
	for name := range names {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if names[parent] {
				return bad()
			}
		}
	}
	return nil
}

func readFileStep(ctx context.Context, tx *sql.Tx, id string) (FileStep, error) {
	var step FileStep
	w, err := scanWorkspace(tx.QueryRowContext(ctx, workspaceQuery, id))
	if err != nil {
		return step, err
	}
	completed := w.State == "prepared" && w.Phase == "complete" && w.Generation == 1
	if completed {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE workspace_id=? AND command='create' AND state='succeeded'`, id).Scan(&w.OperationID); err != nil {
			return step, err
		}
	} else if w.State != "creating" || (w.Phase != "stage" && w.Phase != "publish") || w.Generation != 0 {
		return step, failure("E_FILE_STEP_STATE", "workspace is not in its initial file transaction")
	}
	step.Workspace = w
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT outcome_metadata_json FROM operation_steps WHERE operation_id=? AND sequence=1 AND action='git_worktree' AND state='succeeded'`, w.OperationID).Scan(&raw); err != nil {
		return step, failure("E_FILE_STEP_STATE", "Git identity must be recorded before file staging")
	}
	if json.Unmarshal([]byte(raw), &step.Identity) != nil || step.Identity.Path != w.Path || step.Identity.PathIdentity == "" || step.Identity.AdminIdentity == "" {
		return step, failure("E_STATE_INTENT", "recorded Git identity is invalid")
	}
	err = tx.QueryRowContext(ctx, `SELECT state,request_metadata_json FROM operation_steps WHERE operation_id=? AND sequence=2 AND action='stage_files'`, w.OperationID).Scan(&step.State, &raw)
	if errors.Is(err, sql.ErrNoRows) && w.Phase == "stage" {
		step.State = "pending"
		return step, nil
	}
	if err != nil {
		return step, err
	}
	if len(raw) > 16<<20 || (step.State != "inflight" && step.State != "succeeded") || json.Unmarshal([]byte(raw), &step.Intent) != nil {
		return step, failure("E_STATE_INTENT", "file staging intent is invalid")
	}
	if err := validateFiles(step.Intent); err != nil {
		return step, err
	}
	if (step.State == "inflight") != (w.Phase == "stage") {
		return step, failure("E_FILE_STEP_STATE", "file staging phase disagrees with its operation")
	}
	return step, nil
}
func (w *LockedWorkspace) FileStep(ctx context.Context) (FileStep, error) {
	var step FileStep
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error { var err error; step, err = readFileStep(ctx, tx, w.id); return err })
	})
	return step, err
}

// StartFiles commits all references before creating any sensitive image object.
// It never replaces an existing attempt, even when no object was written yet.
func (w *LockedWorkspace) StartFiles(ctx context.Context, in FileIntent) error {
	if err := validateFiles(in); err != nil {
		return err
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 16<<20 {
		return failure("E_FILE_INTENT", "file staging metadata exceeds 16 MiB")
	}
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readFileStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "pending" {
				return failure("E_FILE_STEP_STATE", "reconcile the recorded image objects instead of restaging")
			}
			var key string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM credential_objects WHERE id=? AND kind='hmac_key' AND deleted_at_ms IS NULL`, in.KeyID).Scan(&key); err != nil {
				return failure("E_HMAC_KEY", "staging key is not registered")
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,sequence,action,subject,state,request_metadata_json,started_at_ms) VALUES(?,2,'stage_files',?,'inflight',?,?)`, step.Workspace.OperationID, w.id, string(raw), time.Now().UnixMilli())
			return err
		})
	})
}

// RecordFiles acknowledges externally re-read, synced and fingerprint-verified
// objects. No filesystem I/O occurs here. This is a metadata primitive, not an
// authorization to publish or mark the workspace prepared.
func (w *LockedWorkspace) RecordFiles(ctx context.Context) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			step, err := readFileStep(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if step.State != "inflight" {
				return failure("E_FILE_STEP_STATE", "no outstanding staging attempt to acknowledge")
			}
			for _, f := range step.Intent.Files {
				var preRef, preMAC any
				if f.Preimage != nil {
					preRef, preMAC = f.PreimageRef, f.PreimageHMAC
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO file_transactions(operation_id,path,preimage_ref,staged_image_ref,preimage_hmac,staged_hmac,desired_mode,state) VALUES(?,?,?,?,?,?,?,'staged')`, step.Workspace.OperationID, f.Path, preRef, f.StagedRef, preMAC, f.StagedHMAC, f.Mode); err != nil {
					return err
				}
			}
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',finished_at_ms=? WHERE operation_id=? AND sequence=2`, now, step.Workspace.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operations SET phase='publish',state='inflight',updated_at_ms=? WHERE id=?`, now, step.Workspace.OperationID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET phase='publish',updated_at_ms=? WHERE id=?`, now, w.id)
			return err
		})
	})
}
