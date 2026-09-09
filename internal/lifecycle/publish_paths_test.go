package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/domain"
	"eve/internal/files"
)

func TestPublicationMissingParentsAndMutationTimeLinkChecks(t *testing.T) {
	for _, swap := range []bool{false, true} {
		t.Run(map[bool]string{false: "create-parents", true: "parent-link-race"}[swap], func(t *testing.T) {
			r := repositoryFixture(t)
			for name, data := range map[string]string{"eve.toml": strings.ReplaceAll(manifest, ".env.local", "generated/nested/local.env"), ".gitignore": "/generated/\n"} {
				if err := os.WriteFile(filepath.Join(r.root, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command(t, r.root, "add", ".")
			command(t, r.root, "commit", "-qm", "nested native destination")
			if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
				t.Fatal(err)
			}
			plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "nested", "")
			if err != nil {
				t.Fatal(err)
			}
			w := approved(t, r, plan)
			if _, err := PrepareGit(t.Context(), r.store, r.client, w); err != nil {
				t.Fatal(err)
			}
			if _, err := StageFiles(t.Context(), r.store, r.client, w, plan.Files); err != nil {
				t.Fatal(err)
			}
			if !swap {
				if _, err := PublishFiles(t.Context(), r.store, r.client, w); err != nil {
					t.Fatal(err)
				}
				for name, mode := range map[string]os.FileMode{"generated": 0700, "generated/nested": 0700, "generated/nested/local.env": 0600} {
					info, err := os.Stat(filepath.Join(plan.Path, name))
					if err != nil || info.Mode().Perm() != mode {
						t.Fatalf("incorrect created path/mode: %s %v", name, err)
					}
				}
				return
			}
			step, err := w.Publication(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, key, err := r.store.HMACKey(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			a, err := r.store.Allocation(t.Context(), plan.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			images, _, err := publicationImages(t.Context(), r.store, w, step, key, a)
			if err != nil {
				t.Fatal(err)
			}
			pub, err := files.OpenPublisher(t.Context(), r.client, step.Identity, plan.Target.Branch, plan.Target.HeadOID, &step.Workspace.Manifest, images)
			if err != nil {
				t.Fatal(err)
			}
			defer pub.Close()
			if err := w.StartPublication(t.Context()); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			victim := filepath.Join(outside, "local.env")
			if err := os.WriteFile(victim, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			err = pub.Publish(t.Context(), step.Files[0].Path, func(receipt domain.FileIdentity) error {
				if err := w.RecordTemporary(t.Context(), step.Files[0].Path, receipt); err != nil {
					return err
				}
				parent := filepath.Join(plan.Path, "generated/nested")
				if err := os.Rename(parent, parent+"-moved"); err != nil {
					return err
				}
				return os.Symlink(outside, parent)
			})
			errorCode(t, err, "E_SYMLINK")
			data, err := os.ReadFile(victim)
			if err != nil || string(data) != "outside" {
				t.Fatal("publication followed substituted parent")
			}
		})
	}
}
