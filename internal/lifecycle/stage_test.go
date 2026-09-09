package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"eve/internal/resolve"
	"eve/internal/state"
)

func TestStagingUsesFrozenAllocations(t *testing.T) {
	r := repositoryFixture(t)
	text := strings.Replace(manifest, "[services.api.env]", "port = \"PORT\"\n[services.api.env]", 1) + "WORKSPACE = \"${workspace.id}\"\n"
	for name, data := range map[string]string{"eve.toml": text, ".gitignore": "/.env.local\n"} {
		if err := os.WriteFile(filepath.Join(r.root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "allocated staging")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	p, err := PlanGit(t.Context(), r.store, r.client, r.root, "allocated", "")
	if err != nil {
		t.Fatal(err)
	}
	w := approved(t, r, p)
	allocation, err := r.store.Allocation(t.Context(), p.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareGit(t.Context(), r.store, r.client, w); err != nil {
		t.Fatal(err)
	}
	if _, err := StageFiles(t.Context(), r.store, r.client, w, p.Files); err != nil {
		t.Fatal(err)
	}
	image, err := StagedImage(t.Context(), r.store, w, ".env.local")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"PORT=" + strconv.Itoa(allocation.Endpoints[0].Port), "WORKSPACE=" + p.WorkspaceID} {
		if !strings.Contains("\n"+string(image.Data), "\n"+entry+"\n") {
			t.Fatal("staged configuration did not use recorded allocation/identity")
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(allocation.Endpoints[0].Port))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, err = PublishFiles(t.Context(), r.store, r.client, w)
	errorCode(t, err, "E_PORT_OCCUPIED")
	if _, err := os.Lstat(filepath.Join(p.Path, ".env.local")); !os.IsNotExist(err) {
		t.Fatal("occupied endpoint allowed publication")
	}
	step, err := w.Publication(t.Context())
	if err != nil || step.Started {
		t.Fatal("occupied endpoint advanced publication intent")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, w); err != nil {
		t.Fatal(err)
	}
}

const stageCanary = "unmanaged-stage-private-canary"
const trackedBefore = "# tracked baseline\nPUBLIC_NAME=before\n"

func stageFixture(t *testing.T) (repository, GitPlan, *state.LockedWorkspace) {
	t.Helper()
	r := repositoryFixture(t)
	for name, content := range map[string]string{
		".gitignore":  "/.env.local\n",
		"tracked.env": trackedBefore,
		"eve.toml":    manifest + "[services.other]\npath = \".\"\nenv_file = \"tracked.env\"\nallow_tracked = true\n[services.other.env]\nPUBLIC_NAME = \"after\"\n",
	} {
		if err := os.WriteFile(filepath.Join(r.root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "staging fixture")
	if err := os.WriteFile(filepath.Join(r.root, ".env.local"), []byte("# user\nSECRET="+stageCanary+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	p, err := PlanGit(t.Context(), r.store, r.client, r.root, "staged", "")
	if err != nil {
		t.Fatal(err)
	}
	w := approved(t, r, p)
	if _, err := PrepareGit(t.Context(), r.store, r.client, w); err != nil {
		t.Fatal(err)
	}
	return r, p, w
}
func unchangedTargets(t *testing.T, p GitPlan) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(p.Path, ".env.local")); !os.IsNotExist(err) {
		t.Fatal("staging published new configuration")
	}
	data, err := os.ReadFile(filepath.Join(p.Path, "tracked.env"))
	if err != nil || string(data) != trackedBefore {
		t.Fatal("staging changed tracked target")
	}
}
func TestProtectedStagingAndPrivateReads(t *testing.T) {
	r, p, w := stageFixture(t)
	step, err := StageFiles(t.Context(), r.store, r.client, w, p.Files)
	if err != nil {
		t.Fatal(err)
	}
	if step.Workspace.State != "creating" || step.Workspace.Phase != "publish" || step.Workspace.Generation != 0 {
		t.Fatal("staging completed workspace")
	}
	for name, want := range map[string]string{".env.local": "# user\nSECRET=" + stageCanary + "\nPUBLIC_NAME=old\n", "tracked.env": "# tracked baseline\nPUBLIC_NAME=after\n"} {
		image, err := StagedImage(t.Context(), r.store, w, name)
		if err != nil {
			t.Fatal(err)
		}
		if string(image.Data) != want || image.Mode != 0600 {
			t.Fatal("incorrect staged image/mode")
		}
		if name == "tracked.env" && (image.PreimageIdentity == nil || string(image.Preimage) != trackedBefore) {
			t.Fatal("tracked preimage lost")
		}
		if name == ".env.local" && (image.PreimageIdentity != nil || image.Preimage != nil) {
			t.Fatal("absent destination became an existing preimage")
		}
		raw, _ := json.Marshal(image)
		if bytes.Contains(raw, []byte(stageCanary)) || bytes.Contains([]byte(fmt.Sprintf("%v %#v", image, image)), []byte(stageCanary)) {
			t.Fatal("private image formatting leaked")
		}
	}
	unchangedTargets(t, p)
	// Completed staging reentry checks immutable objects, not mutable source input.
	if err := os.WriteFile(filepath.Join(r.root, ".env.local"), []byte("edited after staging"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := StageFiles(t.Context(), r.store, r.client, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(step.Intent)
	b, _ := json.Marshal(again.Intent)
	if !bytes.Equal(a, b) {
		t.Fatal("reentry regenerated staging")
	}
	// Same-size corruption must fail even with the old timestamp restored.
	f := step.Intent.Files[0]
	name := filepath.Join(r.base, "state", "pending", f.StagedRef)
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, bytes.Repeat([]byte{'X'}, int(f.Size)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	_, err = StagedImage(t.Context(), r.store, w, f.Path)
	errorCode(t, err, "E_STAGE_CHANGED")
	_, err = StageFiles(t.Context(), r.store, r.client, w, nil)
	errorCode(t, err, "E_STAGE_CHANGED")
	unchangedTargets(t, p)
}

// interruptedStage exercises the same durable step/object boundary separately
// to stop before SQL acknowledgment without a production-only fault hook.
func interruptedStage(t *testing.T, r repository, p GitPlan, w *state.LockedWorkspace, complete bool) state.FileIntent {
	t.Helper()
	step, err := w.FileStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	images, err := p.Files.Prepare(t.Context(), r.client, step.Identity, resolve.Inputs{Workspace: resolve.Workspace{ID: p.WorkspaceID, Branch: p.Target.Branch, Slug: filepath.Base(p.Path)}})
	if err != nil {
		t.Fatal(err)
	}
	id, key, err := r.store.HMACKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	in := imageIntent(p.WorkspaceID, id, key, images)
	if err := w.StartFiles(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	objects, err := r.store.PendingObjects()
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	for i, f := range in.Files {
		if f.Preimage != nil {
			if err := objects.Create(t.Context(), f.PreimageRef, images[i].Preimage); err != nil {
				t.Fatal(err)
			}
		}
		if err := objects.Create(t.Context(), f.StagedRef, images[i].Data); err != nil {
			t.Fatal(err)
		}
		if !complete {
			break
		}
	}
	return in
}
func TestStagingReopenReconcilesOnlyCompleteObjects(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			r, p, w := stageFixture(t)
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			in := killStagingProcess(t, r, p, complete)
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
			step, err := StageFiles(t.Context(), s, r.client, lock, nil)
			if !complete {
				errorCode(t, err, "E_PRIVATE_OBJECT")
				pending, err := lock.FileStep(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if pending.State != "inflight" || pending.Workspace.Phase != "stage" {
					t.Fatal("incomplete images acknowledged")
				}
				a, _ := json.Marshal(in)
				b, _ := json.Marshal(pending.Intent)
				if !bytes.Equal(a, b) {
					t.Fatal("incomplete intent changed")
				}
				_, err = StagedImage(t.Context(), s, lock, in.Files[0].Path)
				errorCode(t, err, "E_FILE_STEP_STATE")
			} else if err != nil || step.State != "succeeded" {
				t.Fatalf("complete lost-response stage not reconciled: %v", err)
			}
			unchangedTargets(t, p)
		})
	}
}
func TestCancelledStagingDoesNotCreateImageIntent(t *testing.T) {
	r, p, w := stageFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := StageFiles(ctx, r.store, r.client, w, p.Files)
	if err != context.Canceled {
		t.Fatalf("cancelled stage: %v", err)
	}
	step, err := w.FileStep(t.Context())
	if err != nil || step.State != "pending" {
		t.Fatal("cancelled staging left image intent")
	}
	unchangedTargets(t, p)
}
