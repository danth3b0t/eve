package lifecycle

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/git"
	"eve/internal/ports"
	"eve/internal/state"
)

const manifest = "version = 1\n[services.api]\npath = \".\"\nenv_file = \".env.local\"\n[services.api.env]\nPUBLIC_NAME = \"old\"\n"

type repository struct {
	root, base string
	client     *git.Client
	store      *state.Store
}

func command(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, data)
	}
	return strings.TrimSuffix(string(data), "\n")
}

func repositoryFixture(t *testing.T) repository {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	root := filepath.Join(base, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	command(t, root, "init", "-b", "main")
	command(t, root, "config", "user.name", "Fixture")
	command(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "eve.toml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, root, "add", "eve.toml")
	command(t, root, "commit", "-qm", "baseline")
	g, err := git.New()
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenForGit(t.Context(), g, root, filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return repository{root, base, g, s}
}

func approved(t *testing.T, r repository, p GitPlan) *state.LockedWorkspace {
	t.Helper()
	lock, err := r.store.LockWorkspace(p.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	})
	settings, err := config.ParseUser([]byte("version = 1\n[ports]\nmin = 41200\nmax = 41900\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.BeginCreate(t.Context(), p.Intent(settings)); err != nil {
		t.Fatal(err)
	}
	if _, err := ports.Reserve(t.Context(), lock, nil); err != nil {
		t.Fatal(err)
	}
	return lock
}

func errorCode(t *testing.T, err error, want string) {
	t.Helper()
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

func TestRegisteredSourceWinsOverInvokingWorktree(t *testing.T) {
	r := repositoryFixture(t)
	_, err := PlanGit(t.Context(), r.store, r.client, r.root, "first", "")
	errorCode(t, err, "E_SOURCE_UNREGISTERED")
	registered, err := RegisterSource(t.Context(), r.store, r.client, r.root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := PlanGit(t.Context(), r.store, r.client, r.root, "first", "")
	if err != nil {
		t.Fatal(err)
	}
	lock := approved(t, r, first)
	id, err := PrepareGit(t.Context(), r.store, r.client, lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "eve.toml"), []byte(strings.ReplaceAll(manifest, "old", "new")), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, r.root, "add", "eve.toml")
	command(t, r.root, "commit", "-qm", "source advanced")
	head := command(t, r.root, "rev-parse", "HEAD")
	second, err := PlanGit(t.Context(), r.store, r.client, id.Path, "second", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Repository.ID != registered.ID || second.Source.Identity.Path != r.root || second.Target.HeadOID != head || second.Target.HeadOID == first.Target.HeadOID {
		t.Fatal("invoking worktree replaced canonical source")
	}
	if string(second.Target.Manifest) != strings.ReplaceAll(manifest, "old", "new") {
		t.Fatal("used invoking worktree's older manifest")
	}
	if _, err := os.Stat(second.Path); !os.IsNotExist(err) {
		t.Fatal("plan mutated Git")
	}
	_, err = RegisterSource(t.Context(), r.store, r.client, id.Path)
	errorCode(t, err, "E_SOURCE_IDENTITY")
	step, err := lock.GitStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if step.Identity == nil || *step.Identity != id || step.Workspace.State != "creating" || step.Workspace.Phase != "stage" || step.Workspace.Generation != 0 {
		t.Fatal("Git receipt missing or incorrectly marked prepared")
	}
	// Known successful identity can finish an interrupted unlock, without a new
	// branch, checkout, manifest selection or allocation.
	again, err := PrepareGit(t.Context(), r.store, r.client, lock)
	if err != nil || again != id {
		t.Fatalf("receipt replay: %v", err)
	}
}

func TestLostGitResponseKeepsIntentAndReconcilesExactReceipt(t *testing.T) {
	r := repositoryFixture(t)
	registered, err := RegisterSource(t.Context(), r.store, r.client, r.root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanGit(t.Context(), r.store, r.client, r.root, "response-lost", "")
	if err != nil {
		t.Fatal(err)
	}
	lock := approved(t, r, p)
	runner := r.client.Runner
	attempts := 0
	r.client.Runner = func(ctx context.Context, cmd git.Command) (git.Result, error) {
		isAdd := slices.Contains(cmd.Args, "worktree") && slices.Contains(cmd.Args, "add")
		if isAdd {
			attempts++
			step, err := lock.GitStep(ctx)
			if err != nil || step.State != "inflight" {
				t.Fatalf("Git began without durable intent: %v", err)
			}
			// A real immediate write transaction must succeed while Git runs.
			probe, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if _, err := r.store.RegisterRepository(probe, registered.CommonDir, registered.SourcePath, registered.Label); err != nil {
				t.Fatalf("SQL transaction held across Git: %v", err)
			}
		}
		result, err := runner(ctx, cmd)
		if isAdd && err == nil && result.ExitCode == 0 {
			return git.Result{}, errors.New("lost response sentinel")
		}
		return result, err
	}
	_, err = PrepareGit(t.Context(), r.store, r.client, lock)
	errorCode(t, err, "E_GIT_EXEC")
	step, err := lock.GitStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if step.State != "unknown" || step.Workspace.State != "failed" || step.Identity != nil {
		t.Fatal("ambiguous success lost its outstanding intent")
	}
	id, err := PrepareGit(t.Context(), r.store, r.client, lock)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || id.Path != p.Path {
		t.Fatal("recovery repeated mutation or adopted wrong path")
	}
	observed, err := r.client.Verify(t.Context(), id)
	if err != nil || observed.HeadOID != p.Target.HeadOID {
		t.Fatalf("receipt was not pinned: %v", err)
	}
}

func TestInflightWithoutReceiptIsNotAdoptedOrRetried(t *testing.T) {
	r := repositoryFixture(t)
	if _, err := RegisterSource(t.Context(), r.store, r.client, r.root); err != nil {
		t.Fatal(err)
	}
	p, err := PlanGit(t.Context(), r.store, r.client, r.root, "interrupted", "")
	if err != nil {
		t.Fatal(err)
	}
	lock := approved(t, r, p)
	if err := lock.StartGit(t.Context()); err != nil {
		t.Fatal(err)
	}
	// This is a different actor's checkout at the planned name, even though its
	// branch and commit match. No EVE creation reference exists.
	command(t, r.root, "worktree", "add", "-b", p.Target.Branch, p.Path, p.Target.HeadOID)
	_, err = PrepareGit(t.Context(), r.store, r.client, lock)
	errorCode(t, err, "E_GIT_OWNERSHIP")
	step, err := lock.GitStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if step.Identity != nil || step.State != "unknown" {
		t.Fatal("unowned worktree acquired registry ownership")
	}
	if _, err := os.Stat(p.Path); err != nil {
		t.Fatal("unowned worktree was removed")
	}
}

func TestContainedStateRejectedBeforeOpening(t *testing.T) {
	r := repositoryFixture(t)
	alias := filepath.Join(r.base, "alias")
	if err := os.Symlink(r.root, alias); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(r.root, "forbidden-state"), filepath.Join(r.root, ".git", "forbidden-state"), filepath.Join(alias, "forbidden-state")} {
		s, err := OpenForGit(t.Context(), r.client, r.root, root)
		if s != nil {
			s.Close()
			t.Fatal("contained state opened")
		}
		errorCode(t, err, "E_STATE_PATH")
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatal("state opener wrote inside the repository")
		}
	}
}
