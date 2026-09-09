package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/platform"
	"github.com/google/uuid"
)

const localManifest = `version = 1
[workspace]
port_block_size = 4
[services.web]
path = "."
env_file = ".env.local"
port = "PORT"
[services.web.env]
LITERAL = "snapshot-only-sentinel"
`

func fixture(t *testing.T) (*Store, Repository) {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "state #?name"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := s.RegisterRepository(t.Context(), filepath.Join(source, ".git"), source, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func begin(t *testing.T, s *Store, r Repository, branch string) (*LockedWorkspace, string) {
	t.Helper()
	id := uuid.NewString()
	w, err := s.LockWorkspace(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	_, err = w.BeginCreate(t.Context(), CreateRequest{RepositoryID: r.ID, Branch: branch, Path: filepath.Join(filepath.Dir(r.SourcePath), id), HeadOID: strings.Repeat("a", 40), Manifest: []byte(localManifest), Ports: config.UserConfig{MinPort: 28000, MaxPort: 28099}})
	if err != nil {
		t.Fatal(err)
	}
	return w, id
}

func code(t *testing.T, err error, want string) {
	t.Helper()
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

func TestDriverDurabilityAndConnectionPragmas(t *testing.T) {
	s, r := fixture(t)
	w, id := begin(t, s, r, "stable")
	a, err := w.Candidate(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AcceptCandidate(t.Context(), a.Base); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "locks", "secrets", "pending", "state.sqlite", "state.sqlite-wal", "state.sqlite-shm"} {
		_, err := platform.CheckPrivate(filepath.Join(s.root, p), !strings.HasPrefix(p, "state.sqlite"))
		if err != nil {
			t.Fatalf("permissions %q: %v", p, err)
		}
	}
	// Force multiple actual driver connections, not merely repeated queries on
	// the one connection that initialized the database.
	s.db.SetMaxOpenConns(3)
	var conns []*sql.Conn
	for range 3 {
		conn, err := s.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		for pragma, want := range map[string]string{"foreign_keys": "1", "journal_mode": "wal", "synchronous": "2", "busy_timeout": "5000"} {
			var got string
			if err := conn.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("%s=%s want %s: %v", pragma, got, want, err)
			}
		}
	}
	for _, conn := range conns {
		conn.Close()
	}
	w.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	meta, err := reopened.Workspace(t.Context(), id)
	if err != nil || meta.State != "creating" || meta.Phase != "worktree" || meta.Generation != 0 || meta.OperationID == "" {
		t.Fatalf("intent must persist, but never be falsely prepared: %+v %v", meta, err)
	}
	encoded, err := json.Marshal(meta)
	if err != nil || strings.Contains(string(encoded), "snapshot-only-sentinel") {
		t.Fatal("workspace JSON exposed manifest values")
	}
	got, err := reopened.Allocation(t.Context(), id)
	if err != nil || !got.Ready || got.Base != a.Base || len(got.Endpoints) != 1 || got.Endpoints[0].Port != a.Base {
		t.Fatalf("durable allocation: %+v %v", got, err)
	}
	var integrity string
	if err := reopened.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%s: %v", integrity, err)
	}
}

func TestSourceRegistrationIdentity(t *testing.T) {
	s, r := fixture(t)
	again, err := s.RegisterRepository(t.Context(), r.CommonDir, r.SourcePath, "new label is not replacement authority")
	if err != nil || again.ID != r.ID || !validID(r.ID) {
		t.Fatalf("registration changed: %+v %v", again, err)
	}
	_, err = s.RegisterRepository(t.Context(), r.CommonDir, t.TempDir(), "other source")
	code(t, err, "E_SOURCE_IDENTITY")
	if err := os.Rename(r.CommonDir, r.CommonDir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(r.CommonDir, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = s.Repository(t.Context(), r.ID)
	code(t, err, "E_SOURCE_IDENTITY")
	_, err = s.RegisterRepository(t.Context(), r.CommonDir, r.SourcePath, "replaced common directory")
	code(t, err, "E_SOURCE_IDENTITY")
}

func TestMovedSourceIsNotReregistered(t *testing.T) {
	s, r := fixture(t)
	moved := r.SourcePath + "-moved"
	if err := os.Rename(r.SourcePath, moved); err != nil {
		t.Fatal(err)
	}
	_, err := s.RegisterRepository(t.Context(), filepath.Join(moved, ".git"), moved, "moved source")
	code(t, err, "E_SOURCE_IDENTITY")
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM repositories`).Scan(&count); err != nil || count != 1 {
		t.Fatal("source movement created another ownership identity", err)
	}
}

func TestSchemaRefusalDoesNotMigrateOrRewrite(t *testing.T) {
	for _, version := range []string{"99", "unknown", "unrelated-with-version-1"} {
		t.Run(version, func(t *testing.T) {
			root, err := platform.PrivateDir(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "state.sqlite")
			if _, err := platform.CreatePrivateFile(path); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			query := `CREATE TABLE other(value TEXT)`
			if version == "99" {
				query = `PRAGMA application_id=1163281713; CREATE TABLE schema_migrations(version INTEGER, applied_at_ms INTEGER); INSERT INTO schema_migrations VALUES(99,0)`
			} else if version == "unrelated-with-version-1" {
				query = `CREATE TABLE schema_migrations(version INTEGER, applied_at_ms INTEGER); INSERT INTO schema_migrations VALUES(1,0)`
			}
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			db.Close()
			before, _ := os.ReadFile(path) // No live SQLite handles when hashing.
			_, err = Open(t.Context(), root)
			code(t, err, "E_STATE_SCHEMA")
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("unsupported database was rewritten")
			}
		})
	}
}

func TestAllocationAndOperationConstraints(t *testing.T) {
	s, r := fixture(t)
	w, id := begin(t, s, r, "one")
	a, err := w.Candidate(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AcceptCandidate(t.Context(), a.Base); err != nil {
		t.Fatal(err)
	}
	other, otherID := begin(t, s, r, "two")
	b, err := other.Candidate(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sql string
		args      []any
	}{
		{"outside block", `INSERT INTO port_claims VALUES(?,?,0)`, []any{a.Base - 1, id}},
		{"overlapping claim", `INSERT INTO port_claims VALUES(?,?,0)`, []any{a.Base, otherID}},
		{"other workspace port", `INSERT INTO endpoints VALUES(?,'bad','primary',0,?,'PORT','localhost','http')`, []any{otherID, a.Base}},
		{"wrong slot", `INSERT INTO endpoints VALUES(?,'bad','primary',1,?,'PORT','localhost','http')`, []any{otherID, b.Base}},
		{"claim reassignment", `UPDATE port_claims SET workspace_id=? WHERE port=?`, []any{otherID, a.Base}},
		{"endpoint movement", `UPDATE endpoints SET slot=1,port=? WHERE workspace_id=?`, []any{a.Base + 1, id}},
		{"block resize", `UPDATE port_blocks SET size=3 WHERE workspace_id=?`, []any{id}},
		{"second unfinished operation", `INSERT INTO operations(id,workspace_id,command,state,phase,intent_json,created_at_ms,updated_at_ms) VALUES(?,?,'create','pending','reserve','{}',0,0)`, []any{uuid.NewString(), id}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.db.ExecContext(t.Context(), tc.sql, tc.args...); err == nil {
				t.Fatal("required database constraint missing")
			}
		})
	}
	code(t, w.RejectCandidate(t.Context(), a.Base), "E_ALLOCATION_FROZEN")
	var claims int
	if err := s.db.QueryRow(`SELECT count(*) FROM port_claims WHERE workspace_id=?`, id).Scan(&claims); err != nil || claims != 4 {
		t.Fatalf("finalized block lost claims: %d %v", claims, err)
	}
	_, err = s.LockWorkspace(id)
	code(t, err, "E_WORKSPACE_BUSY")
	w.Close()
	_, err = w.Candidate(t.Context(), 0)
	code(t, err, "E_WORKSPACE_LOCK")
}

func TestInvalidIntentHasNoPartialWorkspace(t *testing.T) {
	s, r := fixture(t)
	w, err := s.LockWorkspace(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_, err = w.BeginCreate(t.Context(), CreateRequest{RepositoryID: r.ID, Branch: "bad", Path: "/unused", HeadOID: strings.Repeat("a", 40), Manifest: []byte(localManifest), Ports: config.UserConfig{MinPort: 65534, MaxPort: 65535}})
	code(t, err, "E_PORT_RANGE")
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workspaces`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid intent left a workspace: %d %v", count, err)
	}
	_, err = s.Workspace(t.Context(), uuid.NewString())
	code(t, err, "E_WORKSPACE_NOT_FOUND")
	_, err = s.Allocation(t.Context(), uuid.NewString())
	code(t, err, "E_WORKSPACE_NOT_FOUND")
	_, raw := s.db.Exec(`SELECT secret_sentinel_that_must_not_be_printed`)
	if raw == nil || strings.Contains(dbError(raw).Error(), "secret_sentinel") {
		t.Fatal("database diagnostic leaks SQL")
	}
	if !errors.Is(dbError(context.Canceled), context.Canceled) {
		t.Fatal("cancellation was lost")
	}
}
