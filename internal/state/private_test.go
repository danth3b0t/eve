package state

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"context"
	"github.com/google/uuid"
	"strconv"
)

func TestConcurrentMachineKeyAndMissingKeyRefusal(t *testing.T) {
	s, repo := fixture(t)
	type result struct {
		ref, mac string
		err      error
	}
	results := make(chan result, 6)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, err := Open(t.Context(), s.root)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer other.Close()
			ref, key, err := other.HMACKey(t.Context())
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{ref, key.File("workspace", "env", []byte("sentinel")), nil}
		}()
	}
	wg.Wait()
	close(results)
	var want result
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if want.ref == "" {
			want = got
		}
		if got != want {
			t.Fatal("concurrent stores selected different machine keys")
		}
	}
	w, id := begin(t, s, repo, "machine-key-dependent")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	workspace, err := s.Workspace(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE operation_steps SET request_metadata_json=? WHERE operation_id=? AND sequence=0`, `{"KeyID":"`+want.ref+`"}`, workspace.OperationID); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(s.root, "secrets", want.ref)
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.HMACKey(t.Context())
	code(t, err, "E_HMAC_KEY")
	entries, err := os.ReadDir(filepath.Dir(name))
	if err != nil || len(entries) != 0 {
		t.Fatal("missing key was regenerated")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM credential_objects WHERE kind='hmac_key'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("key intent lost or duplicated")
	}
}

func TestMachineKeyIntentBeforeWriteAndLostResponse(t *testing.T) {
	for _, size := range []int{-1, 7, 32} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			s, _ := fixture(t)
			ref := uuid.NewString()
			keyBytes := bytes.Repeat([]byte{0x42}, 32)
			digest := sha256.Sum256(keyBytes)
			metadata, _ := json.Marshal(struct{ SHA256 string }{hex.EncodeToString(digest[:])})
			if err := s.transaction(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO credential_objects(id,secret_object_ref,kind,metadata_json,created_at_ms) VALUES(?,?,'hmac_key',?,?)`, ref, ref, string(metadata), time.Now().UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			objects, err := s.objects("secrets")
			if err != nil {
				t.Fatal(err)
			}
			defer objects.Close()
			if size >= 0 {
				if err := objects.Create(t.Context(), ref, keyBytes[:size]); err != nil {
					t.Fatal(err)
				}
			}
			got, key, err := s.HMACKey(t.Context())
			if size != 32 {
				if err != nil || got == ref || key == nil {
					t.Fatalf("independent incomplete bootstrap not repaired: %v", err)
				}
				again, againKey, err := s.HMACKey(t.Context())
				if err != nil || again != got || againKey == nil {
					t.Fatalf("repaired bootstrap not durable: %v", err)
				}
				return
			}
			if err != nil || got != ref || key.File("workspace", "env", []byte("PORT=1234\n")) != "0c76f2f655aefad9d24774e38ba00b955cc884b4d94396c24191a8c447a81229" {
				t.Fatalf("complete intent not reconciled: %v", err)
			}
			// Same length and permissions do not authorize a different key.
			if err := os.WriteFile(filepath.Join(s.root, "secrets", ref), bytes.Repeat([]byte{0x43}, 32), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err = s.HMACKey(t.Context())
			code(t, err, "E_HMAC_KEY")
		})
	}
}

func TestCancelledMachineKeyHasNoIntent(t *testing.T) {
	s, _ := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := s.HMACKey(ctx)
	if err != context.Canceled {
		t.Fatalf("cancelled key: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM credential_objects`).Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled key left intent")
	}
}
