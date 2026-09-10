package state

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
)

func TestSchemaOneToTwoMigration(t *testing.T) {
	s, r := fixture(t)
	w, id := begin(t, s, r, "migrated")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dbURL := &url.URL{Scheme: "file", Path: filepath.Join(s.root, "state.sqlite")}
	db, err := sql.Open("sqlite", dbURL.String())
	for _, statement := range []string{
		`ALTER TABLE resources DROP COLUMN attempt_started_at_ms`,
		`ALTER TABLE workspaces DROP COLUMN applied_manifest_json`,
		`UPDATE schema_migrations SET version=1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(t.Context(), s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	workspace, err := migrated.Workspace(t.Context(), id)
	if err != nil || workspace.AppliedManifest.Version != 1 {
		t.Fatalf("migrated effective manifest lost: %+v %v", workspace, err)
	}
	var resourceCols, workspaceCols int
	if err := migrated.db.QueryRow(`SELECT count(*) FROM pragma_table_info('resources') WHERE name='attempt_started_at_ms'`).Scan(&resourceCols); err != nil || resourceCols != 1 {
		t.Fatal("resource attempt column missing")
	}
	if err := migrated.db.QueryRow(`SELECT count(*) FROM pragma_table_info('workspaces') WHERE name='applied_manifest_json'`).Scan(&workspaceCols); err != nil || workspaceCols != 1 {
		t.Fatal("applied manifest column missing")
	}
}
