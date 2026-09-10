package lifecycle

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"eve/internal/state"
	"testing"
)

func preparedFixture(t *testing.T) (repository, GitPlan, *state.LockedWorkspace) {
	r, p, w := stagedFixture(t)
	if _, err := PublishFiles(t.Context(), r.store, r.client, w); err != nil {
		t.Fatal(err)
	}
	return r, p, w
}
func TestDestroyLocalPreservesSourceAndTombstone(t *testing.T) {
	r, p, w := preparedFixture(t)
	step, err := w.DestroyStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Workspace.State != "destroyed" || !strings.Contains(result.Warning, "Ignored local") {
		t.Fatal("destroy did not produce tombstone/warning")
	}
	for _, name := range []string{p.Path, step.Identity.AdminDir} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatal("destroyed path remains")
		}
	}
	if command(t, r.root, "branch", "--list", p.Target.Branch) != "" {
		t.Fatal("destroy retained EVE-created branch metadata")
	}
	sourceDotenv, err := os.ReadFile(filepath.Join(r.root, ".env.local"))
	if err != nil || string(sourceDotenv) != "# user\nSECRET="+stageCanary+"\n" {
		t.Fatal("canonical source local configuration changed")
	}
	sourceData, err := os.ReadFile(filepath.Join(r.root, "tracked.env"))
	if err != nil || string(sourceData) != trackedBefore {
		t.Fatal("source tracked configuration changed")
	}
	state, err := r.store.Workspace(t.Context(), p.WorkspaceID)
	if err != nil || state.State != "destroyed" {
		t.Fatal("destroy tombstone missing")
	}
	a, err := r.store.Allocation(t.Context(), p.WorkspaceID)
	if err != nil || len(a.Endpoints) != 0 {
		t.Fatal("endpoint tombstone was not released")
	}
	_, err = DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{Approved: true})
	errorCode(t, err, "E_DESTROY_STATE")
}
func TestDestroyUserWorkRequiresExplicitDiscard(t *testing.T) {
	for _, kind := range []string{"managed-edit", "other-untracked"} {
		t.Run(kind, func(t *testing.T) {
			r, p, w := preparedFixture(t)
			result, err := DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{})
			errorCode(t, err, "E_APPROVAL_REQUIRED")
			if _, err := os.Stat(p.Path); err != nil {
				t.Fatal("unapproved destroy changed worktree")
			}
			switch kind {
			case "managed-edit":
				if err := os.WriteFile(filepath.Join(p.Path, "tracked.env"), []byte("developer edit"), 0600); err != nil {
					t.Fatal(err)
				}
			case "other-untracked":
				if err := os.WriteFile(filepath.Join(p.Path, "user-file"), []byte("user"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{Approved: true})
			errorCode(t, err, "E_WORKTREE_DIRTY")
			step, err := w.DestroyStep(t.Context())
			if err != nil || step.State != "ready" {
				t.Fatal("dirty preflight started destroy")
			}
			if _, err := os.Stat(p.Path); err != nil {
				t.Fatal("blocked destroy removed worktree")
			}
			result, err = DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{Approved: true, DiscardChanges: true})
			if err != nil {
				t.Fatal(err)
			}
			if result.Workspace.State != "destroyed" {
				t.Fatal("discard did not complete")
			}
			if command(t, r.root, "branch", "--list", p.Target.Branch) != "" {
				t.Fatal("discard retained EVE-created branch metadata")
			}
		})
	}
}
func TestDestroyRunningEndpointAndCleanupPending(t *testing.T) {
	r := repositoryFixture(t)
	text := strings.Replace(manifest, "[services.api.env]", "port = \"PORT\"\n[services.api.env]", 1)
	for name, data := range map[string]string{"eve.toml": text, ".gitignore": "/.env.local\n", ".env.local": "LOCAL=keep\n"} {
		if err := os.WriteFile(filepath.Join(r.root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command(t, r.root, "add", ".")
	command(t, r.root, "commit", "-qm", "endpoint destroy")
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGit(t.Context(), r.store, r.client, r.root, "endpoint-destroy", "")
	if err != nil {
		t.Fatal(err)
	}
	w := approved(t, r, plan)
	allocation, err := r.store.Allocation(t.Context(), plan.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareGit(t.Context(), r.store, r.client, w); err != nil {
		t.Fatal(err)
	}
	if _, err := StageFiles(t.Context(), r.store, r.client, w, plan.Files); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFiles(t.Context(), r.store, r.client, w); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(allocation.Endpoints[0].Port))
	if err != nil {
		t.Fatal(err)
	}
	_, err = DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{Approved: true})
	errorCode(t, err, "E_POSSIBLY_RUNNING")
	if _, err := os.Stat(plan.Path); err != nil {
		t.Fatal("listener caused premature removal")
	}
	_, err = DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{Approved: true, AssumeStopped: true})
	errorCode(t, err, "E_CLEANUP_PENDING")
	if _, err := os.Lstat(plan.Path); !os.IsNotExist(err) {
		t.Fatal("worktree not removed")
	}
	state, err := r.store.Workspace(t.Context(), plan.WorkspaceID)
	if err != nil || state.State != "cleanup_pending" {
		t.Fatal("occupied residual was destroyed")
	}
	if a, err := r.store.Allocation(t.Context(), plan.WorkspaceID); err != nil || len(a.Endpoints) != 1 {
		t.Fatal("occupied residual claims were released")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(plan.Path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{})
	errorCode(t, err, "E_GIT_RECONCILE")
	if a, err := r.store.Allocation(t.Context(), plan.WorkspaceID); err != nil || len(a.Endpoints) != 1 {
		t.Fatal("claims were released while a path was replaced")
	}
	if err := os.Remove(plan.Path); err != nil {
		t.Fatal(err)
	}
	result, err := DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Workspace.State != "destroyed" {
		t.Fatal("cleanup did not finish")
	}
	released, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(allocation.Endpoints[0].Port))
	if err != nil {
		t.Fatal("port was not released")
	}
	released.Close()
}
func TestDestroyReconcilesCompletedRemovalResponseLoss(t *testing.T) {
	r, p, w := preparedFixture(t)
	if err := w.StartDestroy(t.Context()); err != nil {
		t.Fatal(err)
	}
	step, err := w.DestroyStep(t.Context())
	if err != nil || step.State != "inflight" {
		t.Fatal("destroy intent missing")
	}
	command(t, r.root, "worktree", "remove", "--force", "--", p.Path)
	command(t, r.root, "worktree", "prune")
	// The durable operation sees proven absence rather than another removal.
	result, err := DestroyLocal(t.Context(), r.store, r.client, w, DestroyOptions{Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Workspace.State != "destroyed" {
		t.Fatal("destroy response was not reconciled")
	}
	if command(t, r.root, "branch", "--list", p.Target.Branch) != "" {
		t.Fatal("reconciliation retained EVE-created branch metadata")
	}
}
