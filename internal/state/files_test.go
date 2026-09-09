package state

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/domain"
	"github.com/google/uuid"
)

func stageWorkspace(t *testing.T, s *Store, r Repository) *LockedWorkspace {
	t.Helper()
	w, _ := begin(t, s, r, "stage")
	a, err := w.Candidate(t.Context(), 28000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AcceptCandidate(t.Context(), a.Base); err != nil {
		t.Fatal(err)
	}
	if err := w.StartGit(t.Context()); err != nil {
		t.Fatal(err)
	}
	workspace, err := s.Workspace(t.Context(), w.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RecordGit(t.Context(), domain.GitIdentity{Path: workspace.Path, PathIdentity: "a:b", CommonDir: r.CommonDir, CommonIdentity: r.CommonIdentity, AdminDir: filepath.Join(r.CommonDir, "worktrees", "stage"), AdminIdentity: "c:d"}); err != nil {
		t.Fatal(err)
	}
	return w
}
func TestFileJournalFrozenAndNotPrepared(t *testing.T) {
	s, r := fixture(t)
	w := stageWorkspace(t, s, r)
	keyID, key, err := s.HMACKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	canary := "private-image-value-canary"
	in := FileIntent{KeyID: keyID, Files: []FileRecord{{Path: ".env.local", Mode: 0600, Size: int64(len(canary)), StagedRef: uuid.NewString(), StagedHMAC: key.File(w.id, ".env.local", []byte(canary)), Values: map[string]string{"PORT": key.Value(w.id, ".env.local", "PORT", "28000")}}}}
	if err := w.StartFiles(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	step, err := w.FileStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT request_metadata_json FROM operation_steps WHERE operation_id=? AND action='stage_files'`, step.Workspace.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, canary) || strings.Contains(raw, `"28000"`) {
		t.Fatal("plaintext in file journal")
	}
	// References are durable before there are any file_transactions acknowledgments.
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM file_transactions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("premature staged acknowledgment")
	}
	in.Files[0].StagedRef = uuid.NewString()
	code(t, w.StartFiles(t.Context(), in), "E_FILE_STEP_STATE")
	// Metadata primitive: the caller is responsible for verifying disk objects.
	if err := w.RecordFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := w.FileStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Workspace.State != "creating" || after.Workspace.Phase != "publish" || after.Workspace.Generation != 0 || after.State != "succeeded" {
		t.Fatal("staging incorrectly completed workspace")
	}
	a, _ := json.Marshal(after.Intent)
	b, _ := json.Marshal(step.Intent)
	if string(a) != string(b) {
		t.Fatal("staging intent changed")
	}
	var ref, status string
	if err := s.db.QueryRow(`SELECT staged_image_ref,state FROM file_transactions WHERE operation_id=? AND path='.env.local'`, step.Workspace.OperationID).Scan(&ref, &status); err != nil || ref != step.Intent.Files[0].StagedRef || status != "staged" {
		t.Fatal("wrong journal reference/state")
	}
	code(t, w.RecordFiles(t.Context()), "E_FILE_STEP_STATE")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = w.FileStep(t.Context())
	code(t, err, "E_WORKSPACE_LOCK")
}
func TestInvalidFileIntent(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "a/../file", ".git/config", "node_modules/a", "a/.convex/state", "invalid\xff"} {
		t.Run(name, func(t *testing.T) {
			in := FileIntent{KeyID: uuid.NewString(), Files: []FileRecord{{Path: name, Mode: 0600, StagedRef: uuid.NewString(), StagedHMAC: strings.Repeat("a", 64)}}}
			code(t, validateFiles(in), "E_FILE_INTENT")
		})
	}
	for _, mutate := range []func(*FileIntent){
		func(in *FileIntent) { in.Files[0].Mode = 0644 },
		func(in *FileIntent) { in.Files[0].Tracked = true },
		func(in *FileIntent) { in.Files[0].Size = 257 << 20 },
		func(in *FileIntent) { in.Files[0].Values = map[string]string{"PORT": "plaintext"} },
		func(in *FileIntent) { in.Files = append(in.Files, in.Files[0]) },
		func(in *FileIntent) {
			f := in.Files[0]
			f.Path = "env/child"
			f.StagedRef = uuid.NewString()
			in.Files = append(in.Files, f)
		},
	} {
		in := FileIntent{KeyID: uuid.NewString(), Files: []FileRecord{{Path: "env", Mode: 0600, StagedRef: uuid.NewString(), StagedHMAC: strings.Repeat("a", 64)}}}
		mutate(&in)
		code(t, validateFiles(in), "E_FILE_INTENT")
	}
}
