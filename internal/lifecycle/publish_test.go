package lifecycle

import (
	"bytes"
	"errors"
	"eve/internal/domain"
	"os"
	"path/filepath"
	"testing"

	"eve/internal/files"
	"eve/internal/state"
	"github.com/google/uuid"
)

func stagedFixture(t *testing.T) (repository, GitPlan, *state.LockedWorkspace) {
	t.Helper()
	r, p, w := stageFixture(t)
	if _, err := StageFiles(t.Context(), r.store, r.client, w, p.Files); err != nil {
		t.Fatal(err)
	}
	return r, p, w
}
func TestPublicationCompletesGenerationAndCleansOnlyOwnedImages(t *testing.T) {
	r, p, w := stagedFixture(t)
	objects, err := r.store.PendingObjects()
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	unrelated := uuid.NewString()
	if err := objects.Create(t.Context(), unrelated, []byte("other-operation")); err != nil {
		t.Fatal(err)
	}
	before, err := w.Publication(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := PublishFiles(t.Context(), r.store, r.client, w)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "prepared" || result.Generation != 1 || result.OperationID != "" {
		t.Fatal("generation did not durably complete")
	}
	for name, want := range map[string]string{".env.local": "# user\nSECRET=" + stageCanary + "\nPUBLIC_NAME=old\n", "tracked.env": "# tracked baseline\nPUBLIC_NAME=after\n"} {
		data, err := os.ReadFile(filepath.Join(p.Path, name))
		if err != nil || string(data) != want {
			t.Fatalf("incorrect publication: %v", err)
		}
		info, err := os.Stat(filepath.Join(p.Path, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("published mode is not private")
		}
	}
	for _, f := range before.Files {
		for _, ref := range []string{f.StagedRef, f.PreimageRef} {
			if ref != "" {
				if _, err := os.Lstat(filepath.Join(r.base, "state", "pending", ref)); !os.IsNotExist(err) {
					t.Fatal("completed snapshots were retained")
				}
			}
		}
	}
	got, err := objects.Read(t.Context(), unrelated, 64)
	if err != nil || string(got) != "other-operation" {
		t.Fatal("cleanup touched another operation")
	}
	// Completed reentry must neither need purged images nor overwrite later edits.
	if err := os.WriteFile(filepath.Join(p.Path, ".env.local"), []byte("developer edit"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := PublishFiles(t.Context(), r.store, r.client, w)
	if err != nil || again.Generation != 1 {
		t.Fatalf("completion response reconciliation: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(p.Path, ".env.local"))
	if string(data) != "developer edit" {
		t.Fatal("completed reentry republished")
	}
}
func TestPublicationRefusesLateChangesBeforeAnyWrite(t *testing.T) {
	for _, kind := range []string{"edited", "replaced", "linked", "ignore", "lock", "unreceipted-temp"} {
		t.Run(kind, func(t *testing.T) {
			r, p, w := stagedFixture(t)
			want := "E_FILE_CHANGED"
			switch kind {
			case "edited":
				if err := os.WriteFile(filepath.Join(p.Path, "tracked.env"), []byte("user edit\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				name := filepath.Join(p.Path, "tracked.env")
				if err := os.Rename(name, name+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte(trackedBefore), 0600); err != nil {
					t.Fatal(err)
				}
			case "linked":
				if err := os.Symlink(filepath.Join(r.root, "tracked.env"), filepath.Join(p.Path, ".env.local")); err != nil {
					t.Fatal(err)
				}
				want = "E_SYMLINK"
			case "ignore":
				if err := os.WriteFile(filepath.Join(p.Path, ".gitignore"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				want = "E_IGNORE_MISSING"
			case "lock":
				command(t, r.root, "worktree", "lock", "--reason", "user lock", p.Path)
				want = "E_GIT_LOCKED"
			case "unreceipted-temp":
				step, err := w.Publication(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				image, err := StagedImage(t.Context(), r.store, w, step.Files[0].Path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(p.Path, files.TemporaryPath(image.Path, step.Files[0].StagedRef)), image.Data, 0600); err != nil {
					t.Fatal(err)
				}
				want = "E_PUBLICATION_RECONCILE"
			}
			_, err := PublishFiles(t.Context(), r.store, r.client, w)
			errorCode(t, err, want)
			stage, err := w.Publication(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if stage.Started || stage.Workspace.Generation != 0 {
				t.Fatal("late preflight failure advanced publication")
			}
			if kind != "linked" {
				if _, err := os.Lstat(filepath.Join(p.Path, ".env.local")); !os.IsNotExist(err) {
					t.Fatal("partial publication on preflight failure")
				}
			}
		})
	}
}
func TestPublicationReconcilesLostTemporaryReceiptResponse(t *testing.T) {
	r, p, w := stagedFixture(t)
	if err := w.StartPublication(t.Context()); err != nil {
		t.Fatal(err)
	}
	step, err := w.Publication(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	id, key, err := r.store.HMACKey(t.Context())
	if err != nil || id != step.Intent.KeyID {
		t.Fatal("key unavailable")
	}
	a, err := r.store.Allocation(t.Context(), p.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	images, _, err := publicationImages(t.Context(), r.store, step, key, a)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := files.OpenPublisher(t.Context(), r.client, step.Identity, p.Target.Branch, p.Target.HeadOID, &step.Workspace.Manifest, images)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	// A durable receipt whose response is lost must be usable after reopening.
	stop := errors.New("lost receipt response")
	err = pub.Publish(t.Context(), step.Files[0].Path, func(receipt domain.FileIdentity) error {
		if err := w.RecordTemporary(t.Context(), step.Files[0].Path, receipt); err != nil {
			t.Fatal(err)
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("receipt interruption: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(p.Path, step.Files[0].Path)); !os.IsNotExist(err) {
		t.Fatal("renamed despite failed receipt callback")
	}
	result, err := PublishFiles(t.Context(), r.store, r.client, w)
	if err != nil || result.State != "prepared" {
		t.Fatalf("receipt reconciliation: %v", err)
	}
	final, _ := os.ReadFile(filepath.Join(p.Path, ".env.local"))
	if !bytes.Contains(final, []byte(stageCanary)) {
		t.Fatal("lost original staged content")
	}
}
