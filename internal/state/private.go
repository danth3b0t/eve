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
		return "", nil, failure("E_HMAC_KEY", "machine key is missing, unsafe or incomplete; restore the recorded key, never regenerate it")
	}
	digest := sha256.Sum256(data)
	if !private.Equal(checksum, hex.EncodeToString(digest[:])) {
		return "", nil, failure("E_HMAC_KEY", "machine key changed; restore the recorded key")
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
