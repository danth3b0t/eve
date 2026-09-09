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
	"github.com/google/uuid"
)

// ManagementProfile is nonsecret token scope metadata. SQL never contains the
// token. Filesystem permissions, not encryption, protect the secret object.
type ManagementProfile struct {
	Provider, Name, TeamSlug, CredentialID string
	TeamID                                 int64
	LastValidatedAtMS                      int64
}

func validManagement(provider, name, teamSlug string, teamID int64) bool {
	return provider == "convex" && envKey.MatchString(name) && teamSlug != "" && teamID > 0
}

// StoreManagementProfile persists a validated token scope BEFORE the token is
// written. A retry reconciles only the exact profile/checksum; missing, partial
// or changed secrets require restoration, never silent rotation/replacement.
func (s *Store) StoreManagementProfile(ctx context.Context, provider, name, teamSlug string, teamID int64, token string, validatedAt time.Time) (ManagementProfile, error) {
	if s.readOnly {
		return ManagementProfile{}, failure("E_STATE_READ_ONLY", "registry is open read-only")
	}
	if !validManagement(provider, name, teamSlug, teamID) || token == "" {
		return ManagementProfile{}, failure("E_CREDENTIAL_PROFILE", "invalid provider credential profile metadata")
	}
	checksum := sha256.Sum256([]byte(token))
	checksumHex := hex.EncodeToString(checksum[:])
	var profile ManagementProfile
	var ref, digest string
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := waitForInit(lockCtx, filepath.Join(s.root, "locks", "credential.lock"))
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return ManagementProfile{}, err
	}
	defer lock.Close()
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var team int64
		var slug, credentialID string
		err := tx.QueryRowContext(ctx, `SELECT credential_id,team_id,team_slug FROM credential_profiles WHERE provider=? AND name=?`, provider, name).Scan(&credentialID, &team, &slug)
		if errors.Is(err, sql.ErrNoRows) {
			ref = uuid.NewString()
			metadata, _ := json.Marshal(struct{ SHA256 string }{checksumHex})
			now := time.Now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `INSERT INTO credential_objects(id,secret_object_ref,kind,provider,metadata_json,created_at_ms) VALUES(?,?,'management_token',?,?,?)`, ref, ref, provider, string(metadata), now); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO credential_profiles(provider,name,credential_id,team_id,team_slug) VALUES(?,?,?,?,?)`, provider, name, ref, teamID, teamSlug)
			return err
		}
		if err != nil {
			return err
		}
		if team != teamID || slug != teamSlug {
			return failure("E_CREDENTIAL_PROFILE", "profile already belongs to a different team identity; use another profile name")
		}
		ref = credentialID
		var metadata string
		if err := tx.QueryRowContext(ctx, `SELECT metadata_json FROM credential_objects WHERE id=? AND kind='management_token' AND provider=? AND deleted_at_ms IS NULL`, ref, provider).Scan(&metadata); err != nil {
			return failure("E_CREDENTIAL_PROFILE", "profile credential metadata is incomplete")
		}
		var held struct{ SHA256 string }
		if json.Unmarshal([]byte(metadata), &held) != nil {
			return failure("E_CREDENTIAL_PROFILE", "profile credential metadata is invalid")
		}
		digest = held.SHA256
		return nil
	})
	if err != nil {
		return ManagementProfile{}, err
	}
	if digest == "" {
		digest = checksumHex
	}
	if digest != checksumHex {
		return ManagementProfile{}, failure("E_CREDENTIAL_PROFILE", "profile token differs from the recorded credential; rotation is not implemented")
	}
	objects, err := s.objects("secrets")
	if err != nil {
		return ManagementProfile{}, err
	}
	defer objects.Close()
	data, err := objects.Read(ctx, ref, int64(len(token)))
	if err != nil {
		if digest != checksumHex {
			return ManagementProfile{}, failure("E_CREDENTIAL_PROFILE", "profile credential has a different recorded identity")
		}
		if err := objects.Create(ctx, ref, []byte(token)); err != nil {
			return ManagementProfile{}, err
		}
		data, err = objects.Read(ctx, ref, int64(len(token)))
	}
	if err != nil || string(data) != token {
		return ManagementProfile{}, failure("E_CREDENTIAL_PROFILE", "profile credential is missing, partial or changed; restore the recorded object")
	}
	now := validatedAt.UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE credential_profiles SET last_validated_at_ms=? WHERE provider=? AND name=?`, now, provider, name); err != nil {
		return ManagementProfile{}, dbError(err)
	}
	profile = ManagementProfile{provider, name, teamSlug, ref, teamID, now}
	if err := s.checkStorage(); err != nil {
		return ManagementProfile{}, err
	}
	return profile, nil
}

// ManagementProjectIdentity resolves immutable profile scope for a later
// project binding; the caller must separately validate virtual team/slug/API.
func (s *Store) ManagementToken(ctx context.Context, provider, name string) (ManagementProfile, string, error) {
	if !validManagement(provider, name, "x", 1) {
		return ManagementProfile{}, "", failure("E_CREDENTIAL_PROFILE", "invalid provider profile")
	}
	var profile ManagementProfile
	var digest string
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var metadata string
		err := tx.QueryRowContext(ctx, `SELECT p.credential_id,p.team_id,p.team_slug,p.last_validated_at_ms,o.metadata_json FROM credential_profiles p JOIN credential_objects o ON o.id=p.credential_id WHERE p.provider=? AND p.name=? AND o.kind='management_token' AND o.deleted_at_ms IS NULL`, provider, name).Scan(&profile.CredentialID, &profile.TeamID, &profile.TeamSlug, &profile.LastValidatedAtMS, &metadata)
		if errors.Is(err, sql.ErrNoRows) {
			return failure("E_PROVIDER_AUTH", "management credential profile is not configured")
		}
		if err != nil {
			return err
		}
		var held struct{ SHA256 string }
		if json.Unmarshal([]byte(metadata), &held) != nil {
			return failure("E_CREDENTIAL_PROFILE", "profile credential metadata is invalid")
		}
		digest = held.SHA256
		return nil
	})
	if err != nil {
		return ManagementProfile{}, "", err
	}
	objects, err := s.objects("secrets")
	if err != nil {
		return ManagementProfile{}, "", err
	}
	defer objects.Close()
	data, err := objects.Read(ctx, profile.CredentialID, private.MaxBytes)
	if err != nil {
		return ManagementProfile{}, "", failure("E_PROVIDER_AUTH", "profile credential is unavailable or unsafe")
	}
	current := sha256.Sum256(data)
	if !private.Equal(digest, hex.EncodeToString(current[:])) {
		return ManagementProfile{}, "", failure("E_CREDENTIAL_PROFILE", "profile credential changed; restore the recorded object")
	}
	profile.Provider, profile.Name = provider, name
	return profile, string(data), nil
}
