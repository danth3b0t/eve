package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"eve/internal/config"
	"eve/internal/private"
	"github.com/google/uuid"
)

// ResourceSpec is nonsecret policy copied from the frozen manifest for a later
// provider adapter. Credential values are never present here.
type ResourceSpec struct{ Project, Profile, Path, EnvFile, TTL, Region string }

type Resource struct {
	ID, ResourceKey, Provider, RemoteReference string
	Spec                                       ResourceSpec
	RemoteProjectID, RemoteID, RemoteName      string
	CredentialID                               string
	KeyGeneration                              int
	IntendedExpiresAtMS, ExpiresAtMS           int64
	State                                      string
	Outputs                                    map[string]string
	Ordered                                    int
}

func resourceSpec(r config.Resource) ResourceSpec {
	return ResourceSpec{Project: r.Project, Profile: r.CredentialProfile, Path: r.Path, EnvFile: r.EnvFile, TTL: r.TTL, Region: r.Region}
}
func resourceRows(id string, m config.Manifest, now time.Time) (map[string]map[string]string, []Resource, error) {
	specs := map[string]map[string]string{}
	var out []Resource
	keys := make([]string, 0, len(m.Resources))
	for key := range m.Resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, key := range keys {
		r := m.Resources[key]
		ttl, err := config.ParseTTL(r.TTL)
		if err != nil {
			return nil, nil, err
		}
		seed := sha256.Sum256([]byte("eve-resource-v1\x00" + id + "\x00" + key))
		suffix := hex.EncodeToString(seed[:4])
		uuidBytes := make([]byte, 16)
		copy(uuidBytes, seed[:])
		uuidBytes[6] = (uuidBytes[6] & 0x0f) | 0x40
		uuidBytes[8] = (uuidBytes[8] & 0x3f) | 0x80
		resourceUUID, err := uuid.FromBytes(uuidBytes)
		if err != nil {
			return nil, nil, failure("E_STATE_INTENT", "resource identity generation failed")
		}
		reference := fmt.Sprintf("dev/eve/%s/%s-%s", strings.ReplaceAll(id, "-", ""), key, suffix)
		if len(reference) > 128 || resourceUUID.String() == "" {
			return nil, nil, failure("E_PROVIDER_INTENT", "resource reference is out of bounds")
		}
		spec := resourceSpec(r)
		raw, _ := json.Marshal(spec)
		out = append(out, Resource{ID: resourceUUID.String(), ResourceKey: key, Provider: r.Provider, RemoteReference: reference, Spec: spec, RemoteProjectID: r.Project, State: "planned", Ordered: i, Outputs: map[string]string{}, IntendedExpiresAtMS: now.Add(ttl).UnixMilli()})
		specs[key] = map[string]string{"spec": string(raw)}
	}
	return specs, out, nil
}

func insertResourceRows(ctx context.Context, tx *sql.Tx, id string, m config.Manifest, now time.Time) error {
	_, resources, err := resourceRows(id, m, now)
	if err != nil {
		return err
	}
	for _, r := range resources {
		spec, _ := json.Marshal(r.Spec)
		outputs, _ := json.Marshal(r.Outputs)
		if _, err := tx.ExecContext(ctx, `INSERT INTO resources(id,workspace_id,resource_key,provider,spec_json,remote_reference,remote_project_id,state,outputs_json,key_generation,intended_expires_at_ms) VALUES(?,?,?,?,?,?,?,?,?,0,?)`, r.ID, id, r.ResourceKey, r.Provider, string(spec), r.RemoteReference, r.Spec.Project, r.State, string(outputs), r.IntendedExpiresAtMS); err != nil {
			return err
		}
	}
	return nil
}

func resourceQuery() string {
	return `SELECT id,resource_key,provider,spec_json,remote_reference,coalesce(remote_project_id,''),coalesce(remote_id,''),coalesce(remote_name,''),coalesce(r.credential_id,''),coalesce(r.key_generation,0),coalesce(r.intended_expires_at_ms,0),coalesce(r.expires_at_ms,0),r.state,outputs_json,rowid FROM resources r WHERE workspace_id=? ORDER BY rowid`
}

func (w *LockedWorkspace) Resources(ctx context.Context) ([]Resource, error) {
	rows := []Resource{}
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error { var err error; rows, err = readResources(ctx, tx, w.id); return err })
	})
	return rows, err
}
func (s *Store) Resources(ctx context.Context, id string) ([]Resource, error) {
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	rows, err := readResources(ctx, s.db, id)
	return rows, dbError(err)
}
func readResources(ctx context.Context, q queryer, id string) ([]Resource, error) {
	rows := []Resource{}
	result, err := q.QueryContext(ctx, resourceQuery(), id)
	if err != nil {
		return rows, err
	}
	defer result.Close()
	for result.Next() {
		var r Resource
		var spec, outs string
		if err := result.Scan(&r.ID, &r.ResourceKey, &r.Provider, &spec, &r.RemoteReference, &r.RemoteProjectID, &r.RemoteID, &r.RemoteName, &r.CredentialID, &r.KeyGeneration, &r.IntendedExpiresAtMS, &r.ExpiresAtMS, &r.State, &outs, &r.Ordered); err != nil {
			return rows, err
		}
		if json.Unmarshal([]byte(spec), &r.Spec) != nil || json.Unmarshal([]byte(outs), &r.Outputs) != nil || r.State == "" {
			return rows, failure("E_STATE_INTENT", "resource metadata is invalid")
		}
		rows = append(rows, r)
	}
	return rows, result.Err()
}
func ResourceEnvFile(r Resource) string {
	if r.Spec.EnvFile == "" {
		return ""
	}
	return pathJoin(r.Spec.Path, r.Spec.EnvFile)
}
func pathJoin(base, file string) string {
	if base == "." {
		return file
	}
	return strings.TrimSuffix(base, "/") + "/" + file
}

func (w *LockedWorkspace) DeployKeyCredential(ctx context.Context, r Resource) (string, error) {
	if r.CredentialID == "" {
		return "", failure("E_PROVIDER_RESOURCE", "deployment credential is not fully recorded")
	}
	objects, err := w.store.objects("secrets")
	if err != nil {
		return "", err
	}
	defer objects.Close()
	data, err := objects.Read(ctx, r.CredentialID, 4096)
	if err != nil {
		return "", failure("E_PROVIDER_RESOURCE", "deployment credential object is missing, partial or unsafe")
	}
	return string(data), nil
}

// PersistResourceObservation stores verified public provider outputs. It never
// stores key text; management/deployment credentials use protected objects.
func (w *LockedWorkspace) RecordResource(ctx context.Context, r Resource) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			var existing Resource
			err := tx.QueryRowContext(ctx, `SELECT id,state FROM resources WHERE id=? AND workspace_id=? AND resource_key=?`, r.ID, w.id, r.ResourceKey).Scan(&existing.ID, &existing.State)
			if errors.Is(err, sql.ErrNoRows) {
				return failure("E_PROVIDER_RESOURCE", "recorded resource is missing")
			}
			if err != nil {
				return err
			}
			if r.Outputs == nil {
				r.Outputs = make(map[string]string)
			}
			raw, _ := json.Marshal(r.Outputs)
			var credential any
			if r.CredentialID != "" {
				credential = r.CredentialID
			}
			result, err := tx.ExecContext(ctx, `UPDATE resources SET remote_project_id=?,remote_id=?,remote_name=?,credential_id=?,key_generation=?,intended_expires_at_ms=?,expires_at_ms=?,state=?,outputs_json=? WHERE id=? AND workspace_id=?`, r.RemoteProjectID, r.RemoteID, r.RemoteName, credential, r.KeyGeneration, r.IntendedExpiresAtMS, r.ExpiresAtMS, r.State, string(raw), r.ID, w.id)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if rows != 1 {
				return failure("E_PROVIDER_RESOURCE", "recorded resource changed during metadata update")
			}
			return nil
		})
	})
}

func (w *LockedWorkspace) MarkResource(ctx context.Context, id, state string) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE resources SET state=? WHERE id=? AND workspace_id=?`, state, id, w.id)
			return err
		})
	})
}

func (w *LockedWorkspace) StartDeployKey(ctx context.Context, r Resource, name string) (string, int, error) {
	credential := uuid.NewString()
	generation := r.KeyGeneration + 1
	if generation > 10 || name == "" || len(name) > 128 {
		return "", 0, failure("E_PROVIDER_RESOURCE", "deploy key generation/name is out of bounds")
	}
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			var current string
			if err := tx.QueryRowContext(ctx, `SELECT state FROM resources WHERE id=? AND workspace_id=? AND key_generation=? AND (state='provisioned' OR state='configuring' OR state='unknown')`, r.ID, w.id, r.KeyGeneration).Scan(&current); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO credential_objects(id,secret_object_ref,kind,provider,metadata_json,created_at_ms) VALUES(?,?,'deployment_key','convex','{}',?)`, credential, credential, time.Now().UnixMilli()); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE resources SET credential_id=?,key_generation=?,state='configuring' WHERE id=? AND workspace_id=?`, credential, generation, r.ID, w.id)
			return err
		})
	})
	return credential, generation, err
}

// StoreCredential writes a secret object only after its SQL reference exists.
// A second attempt verifies the immutable object and high-entropy checksum.
func (s *Store) StoreCredential(ctx context.Context, id string, data []byte) error {
	if s.readOnly {
		return failure("E_STATE_READ_ONLY", "registry is open read-only")
	}
	if !private.ValidRef(id) || len(data) == 0 || len(data) > 4096 {
		return failure("E_CREDENTIAL_PROFILE", "credential object or payload is invalid")
	}
	var provider string
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT provider FROM credential_objects WHERE id=? AND kind IN ('management_token','deployment_key') AND deleted_at_ms IS NULL`, id).Scan(&provider)
	})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	metadata, _ := json.Marshal(struct{ SHA256 string }{hex.EncodeToString(digest[:])})
	objects, err := s.objects("secrets")
	if err != nil {
		return err
	}
	defer objects.Close()
	existing, readErr := objects.Read(ctx, id, int64(len(data)))
	if readErr != nil {
		if err := objects.Create(ctx, id, data); err != nil {
			return err
		}
		existing, err = objects.Read(ctx, id, int64(len(data)))
		if err != nil {
			return err
		}
	}
	if !bytes.Equal(existing, data) {
		return failure("E_CREDENTIAL_PROFILE", "credential secret differs from its recorded object")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE credential_objects SET metadata_json=?,provider=? WHERE id=?`, string(metadata), provider, id)
	return dbError(err)
}

func (s *Store) StoreDeploymentKey(ctx context.Context, resourceCredentialID string, value string) error {
	return s.StoreCredential(ctx, resourceCredentialID, []byte(value))
}
