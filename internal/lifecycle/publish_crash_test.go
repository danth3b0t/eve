package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"eve/internal/domain"
	"eve/internal/files"
	"eve/internal/git"
	"eve/internal/state"
)

func publicationCheckpoint(t *testing.T, s *state.Store, g *git.Client, w *state.LockedWorkspace, phase string) {
	t.Helper()
	p, err := w.Publication(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := s.HMACKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Allocation(t.Context(), p.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	images, _, err := publicationImages(t.Context(), s, w, p, key, a)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := files.OpenPublisher(t.Context(), g, p.Identity, p.Workspace.Branch, p.Workspace.HeadOID, &p.Workspace.Manifest, images)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	if err := w.StartPublication(t.Context()); err != nil {
		t.Fatal(err)
	}
	pause := func() {
		if err := json.NewEncoder(os.Stdout).Encode(p.Intent); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	err = pub.Publish(t.Context(), p.Files[0].Path, func(receipt domain.FileIdentity) error {
		if err := w.RecordTemporary(t.Context(), p.Files[0].Path, receipt); err != nil {
			return err
		}
		if phase == "temporary" {
			pause()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately omit RecordPublished: this is a lost rename response.
	pause()
}
func TestSIGKILLPublicationReconcilesExactReceipt(t *testing.T) {
	for _, phase := range []string{"temporary", "rename", "edited-after-rename"} {
		t.Run(phase, func(t *testing.T) {
			r, p, w := stagedFixture(t)
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EVE_TEST_PUBLISH_CHECKPOINT", phase)
			_ = killStagingProcess(t, r, p, true)
			// Reopen the registry and reacquire the dead child's OS lock.
			if err := r.store.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := OpenForGit(t.Context(), r.client, r.root, filepath.Join(r.base, "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			lock, err := s.LockWorkspace(p.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			checkpoint, err := lock.Publication(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if checkpoint.Files[0].State != "inflight" || checkpoint.Files[0].Receipt == nil || checkpoint.Workspace.Generation != 0 {
				t.Fatal("rename evidence was not durable")
			}
			destination := filepath.Join(p.Path, checkpoint.Files[0].Path)
			var before os.FileInfo
			if phase != "temporary" {
				before, err = os.Stat(destination)
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase == "edited-after-rename" {
				if err := os.WriteFile(destination, []byte("new developer content"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := PublishFiles(t.Context(), s, r.client, lock)
			if phase == "edited-after-rename" {
				errorCode(t, err, "E_FILE_CHANGED")
				pending, err := lock.Publication(t.Context())
				if err != nil || pending.Workspace.Generation != 0 {
					t.Fatal("conflict completed generation")
				}
				if _, err := os.Stat(filepath.Join(r.base, "state", "pending", checkpoint.Files[0].StagedRef)); err != nil {
					t.Fatal("conflict purged recovery image")
				}
				return
			}
			if err != nil || result.State != "prepared" {
				t.Fatalf("publication recovery: %v", err)
			}
			after, err := os.Stat(destination)
			if err != nil {
				t.Fatal(err)
			}
			if before != nil && !os.SameFile(before, after) {
				t.Fatal("lost-response reconciliation repeated rename")
			}
		})
	}
}
