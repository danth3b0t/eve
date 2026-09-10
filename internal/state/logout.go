package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"eve/internal/private"
)

type LogoutResult struct{ References int }

// DeleteManagementProfile removes only local token scope. Workspace/resource
// references are counted for user review after the exact object is gone.
func (s *Store) DeleteManagementProfile(ctx context.Context, provider, name string) (LogoutResult, error) {
	if s.readOnly {
		return LogoutResult{}, failure("E_STATE_READ_ONLY", "registry is open read-only")
	}
	if !validManagement(provider, name, "x", 1) {
		return LogoutResult{}, failure("E_CREDENTIAL_PROFILE", "invalid provider profile")
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := waitForInit(lockCtx, filepath.Join(s.root, "locks", "credential.lock"))
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return LogoutResult{}, err
	}
	defer lock.Close()
	var credentialID, metadata string
	var references int
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT p.credential_id,o.metadata_json FROM credential_profiles p JOIN credential_objects o ON o.id=p.credential_id WHERE p.provider=? AND p.name=? AND o.kind='management_token' AND o.deleted_at_ms IS NULL`, provider, name).Scan(&credentialID, &metadata)
		if errors.Is(err, sql.ErrNoRows) {
			return failure("E_PROVIDER_AUTH", "management credential profile is not configured")
		}
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM resources WHERE provider=? AND spec_json LIKE ? AND state<>'deleted'`, provider, "%\"Profile\":\""+name+"\"%").Scan(&references)
	})
	if err != nil {
		return LogoutResult{}, err
	}
	var held struct{ SHA256 string }
	if json.Unmarshal([]byte(metadata), &held) != nil || !private.ValidRef(credentialID) {
		return LogoutResult{}, failure("E_CREDENTIAL_PROFILE", "profile credential metadata is invalid")
	}
	objects, err := s.objects("secrets")
	if err != nil {
		return LogoutResult{}, err
	}
	defer objects.Close()
	data, readErr := objects.Read(ctx, credentialID, private.MaxBytes)
	size := int64(0)
	if readErr == nil {
		size = int64(len(data))
	}
	verify := func(current []byte) bool {
		digest := sha256.Sum256(current)
		return private.Equal(held.SHA256, hex.EncodeToString(digest[:]))
	}
	if err := objects.RemoveVerified(ctx, credentialID, size, verify); err != nil {
		return LogoutResult{}, failure("E_CREDENTIAL_PROFILE", "profile credential differs from recorded metadata and could not be deleted")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE credential_objects SET deleted_at_ms=? WHERE id=? AND kind='management_token' AND deleted_at_ms IS NULL`, time.Now().UnixMilli(), credentialID); err != nil {
		return LogoutResult{}, dbError(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM credential_profiles WHERE provider=? AND name=?`, provider, name); err != nil {
		return LogoutResult{}, dbError(err)
	}
	return LogoutResult{References: references}, nil
}
