package state

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"time"

	"eve/internal/private"
	"github.com/google/uuid"
)

func (s *Store) objects(name string) (*private.Objects, error) {
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	objects, err := private.Open(filepath.Join(s.root, name))
	if err != nil {
		return nil, err
	}
	if err := s.checkStorage(); err != nil {
		objects.Close()
		return nil, err
	}
	return objects, nil
}

// PendingObjects exposes only opaque object operations, never a worktree path.
// The caller must first record every reference in its workspace operation intent.
func (s *Store) PendingObjects() (*private.Objects, error) { return s.objects("pending") }

// HMACKey records a single random key's identity and high-entropy checksum BEFORE
// writing it. Missing/partial/changed keys are never silently regenerated. A
// complete lost-response write can be verified and re-synced on the next call.
// The OS key lock precedes short SQL transactions; no SQL spans filesystem I/O.
func (s *Store) HMACKey(ctx context.Context) (string, *private.Key, error) {
	if s.readOnly {
		return "", nil, failure("E_STATE_READ_ONLY", "registry is open read-only")
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if err := s.checkStorage(); err != nil {
		return "", nil, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := waitForInit(lockCtx, filepath.Join(s.root, "locks", "hmac.lock"))
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", nil, err
	}
	defer lock.Close()
	var ref, checksum string
	var generated []byte
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM credential_objects WHERE kind='hmac_key'`).Scan(&count); err != nil {
			return err
		}
		if count > 1 {
			return failure("E_HMAC_KEY", "multiple machine keys require explicit recovery")
		}
		if count == 1 {
			var metadata string
			if err := tx.QueryRowContext(ctx, `SELECT secret_object_ref,metadata_json FROM credential_objects WHERE kind='hmac_key' AND deleted_at_ms IS NULL`).Scan(&ref, &metadata); err != nil {
				return failure("E_HMAC_KEY", "recorded machine key is unavailable; do not replace it")
			}
			var m struct{ SHA256 string }
			if json.Unmarshal([]byte(metadata), &m) != nil {
				return failure("E_HMAC_KEY", "machine key metadata is invalid")
			}
			checksum = m.SHA256
			if !private.ValidRef(ref) || !private.Equal(checksum, checksum) {
				return failure("E_HMAC_KEY", "machine key metadata is invalid")
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if ref == "" {
		generated = make([]byte, 32)
		if _, err := rand.Read(generated); err != nil {
			return "", nil, failure("E_HMAC_KEY", "cannot generate machine key")
		}
		digest := sha256.Sum256(generated)
		checksum = hex.EncodeToString(digest[:])
		ref = uuid.NewString()
		metadata, _ := json.Marshal(struct{ SHA256 string }{checksum})
		err = s.transaction(ctx, func(tx *sql.Tx) error {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM credential_objects WHERE kind='hmac_key'`).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return failure("E_HMAC_KEY", "machine key intent changed during initialization")
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO credential_objects(id,secret_object_ref,kind,metadata_json,created_at_ms) VALUES(?,?,'hmac_key',?,?)`, ref, ref, string(metadata), time.Now().UnixMilli())
			return err
		})
	}
	if err != nil {
		return "", nil, err
	}
	objects, err := s.objects("secrets")
	if err != nil {
		return "", nil, err
	}
	defer objects.Close()
	if generated != nil {
		if err := objects.Create(ctx, ref, generated); err != nil {
			return "", nil, err
		}
	}
	data, err := objects.Read(ctx, ref, 32)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		recoverable, repairErr := s.hmacBootstrapRecoverable(ctx, ref)
		if repairErr != nil {
			return "", nil, repairErr
		}
		if !recoverable {
			return "", nil, failure("E_HMAC_KEY", "machine key is missing, unsafe or incomplete; restore the recorded key, never regenerate it")
		}
		generated = make([]byte, 32)
		if _, err := rand.Read(generated); err != nil {
			return "", nil, failure("E_HMAC_KEY", "cannot generate machine key")
		}
		digest := sha256.Sum256(generated)
		checksum = hex.EncodeToString(digest[:])
		newRef := uuid.NewString()
		metadata, _ := json.Marshal(struct{ SHA256 string }{checksum})
		if err := s.replaceIncompleteHMACBootstrap(ctx, ref, newRef, string(metadata)); err != nil {
			return "", nil, err
		}
		ref = newRef
		if err := objects.Create(ctx, ref, generated); err != nil {
			return "", nil, err
		}
		if data, err = objects.Read(ctx, ref, 32); err != nil {
			return "", nil, failure("E_HMAC_KEY", "repaired machine key could not be verified")
		}
	}
	digest := sha256.Sum256(data)
	if !private.Equal(checksum, hex.EncodeToString(digest[:])) {
		recoverable, repairErr := s.hmacBootstrapRecoverable(ctx, ref)
		if repairErr != nil {
			return "", nil, repairErr
		}
		if !recoverable || len(data) == 32 {
			return "", nil, failure("E_HMAC_KEY", "machine key changed; restore the recorded key")
		}
		generated = make([]byte, 32)
		if _, err := rand.Read(generated); err != nil {
			return "", nil, failure("E_HMAC_KEY", "cannot generate machine key")
		}
		digest = sha256.Sum256(generated)
		checksum = hex.EncodeToString(digest[:])
		newRef := uuid.NewString()
		metadata, _ := json.Marshal(struct{ SHA256 string }{checksum})
		if err := s.replaceIncompleteHMACBootstrap(ctx, ref, newRef, string(metadata)); err != nil {
			return "", nil, err
		}
		ref = newRef
		if err := objects.Create(ctx, ref, generated); err != nil {
			return "", nil, err
		}
		data = generated
	}
	key, err := private.LoadKey(data)
	if err != nil {
		return "", nil, failure("E_HMAC_KEY", "machine key is incomplete; restore the recorded key")
	}
	if err := s.checkStorage(); err != nil {
		return "", nil, err
	}
	return ref, key, nil
}

func (s *Store) hmacBootstrapRecoverable(ctx context.Context, ref string) (bool, error) {
	dependents, err := hmacDependentCount(ctx, s.db, ref)
	if err != nil {
		return false, err
	}
	return dependents == 0, nil
}

func hmacDependentCount(ctx context.Context, q queryer, ref string) (int, error) {
	var dependents int
	err := q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM file_transactions) + (SELECT count(*) FROM managed_files) + (SELECT count(*) FROM managed_values) + (SELECT count(*) FROM operation_steps WHERE instr(request_metadata_json,?) > 0)`, ref).Scan(&dependents)
	return dependents, dbError(err)
}

func (s *Store) replaceIncompleteHMACBootstrap(ctx context.Context, oldRef, newRef, metadata string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		dependents, err := hmacDependentCount(ctx, tx, oldRef)
		if err != nil {
			return err
		}
		if dependents != 0 {
			return failure("E_HMAC_KEY", "machine key already has dependents; restore the recorded key")
		}
		var existing string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM credential_objects WHERE id=? AND secret_object_ref=? AND kind='hmac_key' AND deleted_at_ms IS NULL`, oldRef, oldRef).Scan(&existing); err != nil {
			return failure("E_HMAC_KEY", "incomplete machine key record changed; retain it for recovery")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM credential_objects WHERE id=? AND kind='hmac_key'`, oldRef); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO credential_objects(id,secret_object_ref,kind,metadata_json,created_at_ms) VALUES(?,?,'hmac_key',?,?)`, newRef, newRef, metadata, time.Now().UnixMilli())
		return err
	})
}
