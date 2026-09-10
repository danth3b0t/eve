package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func missingHMACBootstrap(t *testing.T, s *Store) string {
	t.Helper()
	ref := uuid.NewString()
	digest := sha256.Sum256([]byte(ref))
	metadata, _ := json.Marshal(struct{ SHA256 string }{hex.EncodeToString(digest[:])})
	if _, err := s.db.Exec(`INSERT INTO credential_objects(id,secret_object_ref,kind,metadata_json,created_at_ms) VALUES(?,?,'hmac_key',?,1)`, ref, ref, string(metadata)); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestIncompleteHMACBootstrapRepairsOnlyWithoutDependents(t *testing.T) {
	s, _ := fixture(t)
	old := missingHMACBootstrap(t, s)
	repaired, key, err := s.HMACKey(t.Context())
	if err != nil || repaired == old || key == nil {
		t.Fatalf("incomplete bootstrap not repaired: %v key=%v", err, key)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM credential_objects WHERE kind='hmac_key' AND deleted_at_ms IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("bootstrap list is wrong: %d %v", count, err)
	}
	if _, err := os.ReadFile(filepath.Join(s.root, "secrets", repaired)); err != nil {
		t.Fatal("repaired key object missing")
	}
	if info, _ := os.Stat(filepath.Join(s.root, "secrets", repaired)); info.Mode().Perm() != 0600 {
		t.Fatal("repaired key mode is broad")
	}
}

func TestDependentHMACKeyIsNeverRegenerated(t *testing.T) {
	s, repo := fixture(t)
	w, id := begin(t, s, repo, "machine-key")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	old := missingHMACBootstrap(t, s)
	workspace, err := s.Workspace(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	intent := `{"KeyID":"` + old + `"}`
	if _, err := s.db.Exec(`UPDATE operation_steps SET request_metadata_json=? WHERE operation_id=? AND sequence=0`, intent, workspace.OperationID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.HMACKey(t.Context())
	code(t, err, "E_HMAC_KEY")
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM credential_objects WHERE id=? AND kind='hmac_key'`, old).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dependent machine key was replaced: count=%d %v", count, err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "secrets", old)); !os.IsNotExist(err) {
		t.Fatal("lost dependent key was invented")
	}
	if !strings.Contains(err.Error(), "dependents") && !strings.Contains(err.Error(), "restore") {
		t.Fatalf("diagnostic not actionable: %v", err)
	}
}
