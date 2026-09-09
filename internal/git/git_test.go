package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"eve/internal/config"
	"eve/internal/domain"
	"github.com/google/uuid"
)

const minimal = "version = 1\n[services.web]\npath = \".\"\nenv_file = \".env.local\"\n[services.web.env]\nPUBLIC_NAME = \"baseline\"\n"

type fixture struct {
	t             *testing.T
	root, scratch string
	client        *Client
	source        Checkout
}

func newRepo(t *testing.T) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	f := &fixture{t: t, root: filepath.Join(base, "source with space\nand newline"), scratch: filepath.Join(base, "scratch")}
	for _, dir := range []string{f.root, f.scratch} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.git(f.root, "init", "-b", "main")
	f.git(f.root, "config", "user.name", "Fixture")
	f.git(f.root, "config", "user.email", "fixture@example.invalid")
	f.put("eve.toml", minimal)
	f.put("app.txt", "baseline\n")
	f.put(".gitignore", ".env.local\ncache/\n")
	f.git(f.root, "add", ".")
	f.git(f.root, "commit", "-qm", "baseline")
	f.client, err = New()
	if err != nil {
		t.Fatal(err)
	}
	f.source, err = f.client.Inspect(t.Context(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) git(root string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = root, append(gitEnvironment(), "GIT_CONFIG_NOSYSTEM=1")
	data, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("fixture git %v: %v: %s", args, err, data)
	}
	return strings.TrimSuffix(string(data), "\n")
}

func (f *fixture) put(path, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, path), []byte(content), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) request(branch string) AddRequest {
	f.t.Helper()
	target, err := f.client.Plan(f.t.Context(), f.source, branch, "")
	if err != nil {
		f.t.Fatal(err)
	}
	ref, err := CreationReference(uuid.NewString())
	if err != nil {
		f.t.Fatal(err)
	}
	path, err := WorkspacePath(f.root, "demo", uuid.NewString(), branch, uuid.NewString())
	if err != nil {
		f.t.Fatal(err)
	}
	return AddRequest{Source: f.source.Identity, Target: target, Path: path, Reference: ref, Scratch: f.scratch}
}

func code(t *testing.T, err error, want string) {
	t.Helper()
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

func TestCommittedPlanningAndPinnedCheckout(t *testing.T) {
	f := newRepo(t)
	f.put("eve.toml", "working copy is deliberately invalid")
	request := f.request("feature/支付;literal")
	if string(request.Target.Manifest) != minimal || !request.Target.NewBranch {
		t.Fatal("plan did not use committed target")
	}
	if _, err := os.Stat(filepath.Dir(request.Path)); !os.IsNotExist(err) {
		t.Fatal("plan created destination parent")
	}
	f.put("app.txt", "later source commit\n")
	f.git(f.root, "add", "app.txt")
	f.git(f.root, "commit", "-qm", "source moved after planning")
	checkout, err := f.client.Add(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.HeadOID != request.Target.HeadOID || checkout.Identity.AdminDir == f.source.Identity.AdminDir {
		t.Fatal("checkout was not pinned and independent")
	}
	if data, _ := os.ReadFile(filepath.Join(checkout.Identity.Path, "app.txt")); string(data) != "baseline\n" {
		t.Fatal("new worktree silently followed moving source HEAD")
	}
	if data, _ := os.ReadFile(filepath.Join(f.root, "eve.toml")); string(data) != "working copy is deliberately invalid" {
		t.Fatal("source manifest changed")
	}
	if _, err := f.client.ObserveCreation(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := f.client.FinishCreation(t.Context(), checkout.Identity, request.Reference, f.scratch); err != nil {
		t.Fatal(err)
	}
	_, err = f.client.ObserveCreation(t.Context(), request)
	code(t, err, "E_GIT_OWNERSHIP")
	if err := f.client.Remove(t.Context(), checkout.Identity, request.Target.Branch, request.Reference, f.scratch, RemovalOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := f.git(f.root, "rev-parse", "refs/heads/"+request.Target.Branch); got != request.Target.HeadOID {
		t.Fatal("removal deleted/moved branch")
	}
}

func TestExistingBranchNeverResetOrStolen(t *testing.T) {
	f := newRepo(t)
	f.git(f.root, "branch", "existing")
	request := f.request("existing")
	if request.Target.NewBranch {
		t.Fatal("existing branch classified as new")
	}
	_, err := f.client.Plan(t.Context(), f.source, "existing", "HEAD")
	code(t, err, "E_GIT_REF")
	f.put("app.txt", "new commit")
	f.git(f.root, "add", "app.txt")
	f.git(f.root, "commit", "-qm", "next")
	f.git(f.root, "branch", "-f", "existing", "HEAD")
	_, err = f.client.Add(t.Context(), request)
	code(t, err, "E_GIT_REF_CHANGED")
	if _, err := os.Stat(request.Path); !os.IsNotExist(err) {
		t.Fatal("moved ref created a directory")
	}
	_, err = f.client.Plan(t.Context(), f.source, "main", "")
	code(t, err, "E_GIT_WORKTREE_CONFLICT")
	for _, name := range []string{"-bad", "@{-1}", "foo..bar", "a\x00b"} {
		_, err := f.client.Plan(t.Context(), f.source, name, "")
		code(t, err, "E_GIT_BRANCH")
	}
	// An existing free branch is supported, without changing its original tip.
	request = f.request("existing")
	created, err := f.client.Add(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.HeadOID != request.Target.HeadOID {
		t.Fatal("existing branch changed")
	}
}

func TestTargetManifestAndLayouts(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "oversized", "submodule", "sparse", "skip-worktree", "assume-unchanged"} {
		t.Run(kind, func(t *testing.T) {
			f := newRepo(t)
			want := "E_GIT_LAYOUT"
			switch kind {
			case "missing":
				f.git(f.root, "rm", "eve.toml")
				want = "E_TARGET_MANIFEST_MISSING"
			case "symlink":
				if err := os.Remove(filepath.Join(f.root, "eve.toml")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("app.txt", filepath.Join(f.root, "eve.toml")); err != nil {
					t.Fatal(err)
				}
				f.git(f.root, "add", "eve.toml")
				want = "E_SYMLINK"
			case "oversized":
				f.put("eve.toml", strings.Repeat("#", config.MaxBytes+1))
				f.git(f.root, "add", "eve.toml")
				want = "E_MANIFEST_INVALID"
			case "submodule":
				f.git(f.root, "update-index", "--add", "--cacheinfo", "160000,"+f.source.HeadOID+",vendor")
			case "sparse":
				f.git(f.root, "config", "core.sparseCheckout", "true")
			case "skip-worktree", "assume-unchanged":
				f.git(f.root, "update-index", "--"+kind, "app.txt")
			}
			if kind == "missing" || kind == "symlink" || kind == "oversized" || kind == "submodule" {
				f.git(f.root, "commit", "-qm", "target layout")
			}
			from := ""
			if kind == "submodule" {
				from = f.git(f.root, "rev-parse", "HEAD")
				f.git(f.root, "reset", "--hard", "HEAD^")
			}
			_, err := f.client.Plan(t.Context(), f.source, "feature", from)
			code(t, err, want)
		})
	}
}

func TestHookSuppressionEnvironmentAndReadOnlyInspection(t *testing.T) {
	f := newRepo(t)
	marker := filepath.Join(f.root, "hook-ran")
	hook := filepath.Join(f.source.Identity.CommonDir, "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf ran > hook-ran\n"), 0700); err != nil {
		t.Fatal(err)
	}
	monitor := filepath.Join(f.scratch, "fsmonitor")
	if err := os.WriteFile(monitor, []byte("#!/bin/sh\nprintf ran > hook-ran\n"), 0700); err != nil {
		t.Fatal(err)
	}
	f.git(f.root, "config", "core.fsmonitor", monitor)
	t.Setenv("GIT_DIR", "/does-not-exist")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Dir(hook))
	t.Setenv("CONVEX_DEPLOY_KEY", "sentinel-not-a-real-key")
	index := filepath.Join(f.source.Identity.AdminDir, "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	request := f.request("hook-test")
	if _, err := f.client.Changes(t.Context(), f.root); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("read-only Git commands refreshed the index")
	}
	created, err := f.client.Add(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{marker, filepath.Join(created.Identity.Path, "hook-ran")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("a hook/fsmonitor executed")
		}
	}
	if got := f.git(f.root, "config", "--get", "core.fsmonitor"); got != monitor {
		t.Fatal("common configuration rewritten")
	}
}

func TestRemovalRefusesUserChangesAndUnrelatedLocks(t *testing.T) {
	f := newRepo(t)
	request := f.request("dirty")
	created, err := f.client.Add(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Identity
	file := filepath.Join(id.Path, "app.txt")
	if err := os.WriteFile(file, []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	err = f.client.Remove(t.Context(), id, request.Target.Branch, request.Reference, f.scratch, RemovalOptions{})
	code(t, err, "E_WORKTREE_DIRTY")
	// Matching EVE content may approve only an unstaged tracked edit. A staged
	// user edit cannot be waived by a materializer callback.
	options := RemovalOptions{OwnedEdit: func(path string) (bool, error) { return path == "app.txt", nil }}
	if _, err := f.client.CheckRemoval(t.Context(), id, request.Target.Branch, request.Reference, options); err != nil {
		t.Fatal(err)
	}
	f.git(id.Path, "add", "app.txt")
	_, err = f.client.CheckRemoval(t.Context(), id, request.Target.Branch, request.Reference, options)
	code(t, err, "E_WORKTREE_DIRTY")
	f.git(id.Path, "reset", "--hard", "HEAD")
	if err := os.WriteFile(filepath.Join(id.Path, "untracked\nfile"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = f.client.CheckRemoval(t.Context(), id, request.Target.Branch, request.Reference, options)
	code(t, err, "E_WORKTREE_DIRTY")
	if err := f.client.FinishCreation(t.Context(), id, request.Reference, f.scratch); err != nil {
		t.Fatal(err)
	}
	f.git(f.root, "worktree", "lock", "--reason", "user backup", id.Path)
	err = f.client.Remove(t.Context(), id, request.Target.Branch, request.Reference, f.scratch, RemovalOptions{DiscardChanges: true})
	code(t, err, "E_GIT_LOCKED")
	f.git(f.root, "worktree", "unlock", id.Path)
	if err := f.client.Remove(t.Context(), id, request.Target.Branch, request.Reference, f.scratch, RemovalOptions{DiscardChanges: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.git(f.root, "rev-parse", "refs/heads/dirty"); got != request.Target.HeadOID {
		t.Fatal("discard deleted branch")
	}
	_, err = f.client.CheckRemoval(t.Context(), f.source.Identity, "main", request.Reference, RemovalOptions{DiscardChanges: true})
	code(t, err, "E_GIT_OWNERSHIP")
}

func TestIdentityReplacementAndUnownedDestination(t *testing.T) {
	f := newRepo(t)
	request := f.request("owned")
	if err := os.MkdirAll(request.Path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := f.client.Add(t.Context(), request)
	code(t, err, "E_WORKTREE_EXISTS")
	if err := os.Remove(request.Path); err != nil {
		t.Fatal(err)
	}
	created, err := f.client.Add(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	moved := request.Path + "-moved"
	if err := os.Rename(request.Path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(request.Path, 0700); err != nil {
		t.Fatal(err)
	}
	pointer, err := os.ReadFile(filepath.Join(moved, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.Path, ".git"), pointer, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = f.client.Verify(t.Context(), created.Identity)
	code(t, err, "E_GIT_IDENTITY")
	if _, err := os.Stat(moved); err != nil {
		t.Fatal("identity check removed original")
	}
}

func TestBoundaryErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result Result
		err    error
		want   string
	}{
		{"failed", Result{Output: []byte("sentinel-secret"), ExitCode: 128}, nil, "E_GIT_READ"},
		{"exec", Result{}, errors.New("sentinel-secret"), "E_GIT_EXEC"},
		{"overflow", Result{Output: bytes.Repeat([]byte{'x'}, maxOutput+1)}, nil, "E_GIT_OUTPUT_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{Runner: func(context.Context, Command) (Result, error) { return tc.result, tc.err }}
			_, err := c.read(t.Context(), "/", "status")
			code(t, err, tc.want)
			if strings.Contains(err.Error(), "sentinel-secret") {
				t.Fatal("subprocess output leaked")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := &Client{Runner: func(context.Context, Command) (Result, error) {
		t.Fatal("cancelled request executed Git")
		return Result{}, nil
	}}
	if _, err := c.read(ctx, "/", "status"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
