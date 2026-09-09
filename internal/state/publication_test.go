package state

import (
	"strings"
	"testing"

	"eve/internal/domain"
	"github.com/google/uuid"
)

func TestPublicationAcknowledgmentsAndGenerationAreAtomic(t *testing.T) {
	s, r := fixture(t)
	w := stageWorkspace(t, s, r)
	keyID, key, err := s.HMACKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f := FileRecord{Path: ".env.local", Mode: 0600, StagedRef: uuid.NewString(), StagedHMAC: key.File(w.id, ".env.local", nil), Values: map[string]string{"LITERAL": key.Value(w.id, ".env.local", "LITERAL", "sensitive-value")}}
	if err := w.StartFiles(t.Context(), FileIntent{KeyID: keyID, Files: []FileRecord{f}}); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	owners := map[string]map[string][]string{f.Path: {"LITERAL": {"service.web"}}}
	code(t, w.CompletePublication(t.Context(), owners), "E_PUBLICATION_STATE")
	code(t, w.RecordImagesPurged(t.Context(), f.Path), "E_PUBLICATION_STATE")
	if err := w.StartPublication(t.Context()); err != nil {
		t.Fatal(err)
	}
	code(t, w.RecordPublished(t.Context(), f.Path), "E_PUBLICATION_STATE")
	if err := w.RecordTemporary(t.Context(), f.Path, domain.FileIdentity{Identity: "a:b", Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	code(t, w.CompletePublication(t.Context(), owners), "E_PUBLICATION_STATE")
	if err := w.RecordPublished(t.Context(), f.Path); err != nil {
		t.Fatal(err)
	}
	code(t, w.CompletePublication(t.Context(), nil), "E_PUBLICATION_STATE")
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM managed_files`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed generation partially committed metadata")
	}
	if err := w.CompletePublication(t.Context(), owners); err != nil {
		t.Fatal(err)
	}
	workspace, err := s.Workspace(t.Context(), w.id)
	if err != nil || workspace.Generation != 1 || workspace.State != "prepared" || workspace.OperationID != "" {
		t.Fatal("generation completion incorrect")
	}
	var mac, recordedOwners, sensitivity string
	if err := s.db.QueryRow(`SELECT value_hmac,owners_json,sensitivity FROM managed_values WHERE workspace_id=? AND env_key='LITERAL'`, w.id).Scan(&mac, &recordedOwners, &sensitivity); err != nil {
		t.Fatal(err)
	}
	if mac != f.Values["LITERAL"] || strings.Contains(recordedOwners, "sensitive-value") || sensitivity != "private" {
		t.Fatal("managed value metadata lost privacy/fingerprint")
	}
	code(t, w.CompletePublication(t.Context(), owners), "E_PUBLICATION_STATE")
	if err := w.RecordImagesPurged(t.Context(), f.Path); err != nil {
		t.Fatal(err)
	}
}
