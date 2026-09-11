package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInspectForCompletionMatchesRepositoryIdentity(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "source")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	runGit(t, root, "commit", "--allow-empty", "-qm", "baseline")
	client, err := New()
	if err != nil {
		t.Skip(err)
	}
	full, err := client.Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := client.InspectForCompletion(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if narrow.Identity != full.Identity || narrow.HeadOID != full.HeadOID {
		t.Fatalf("narrow checkout identity: %#v vs %#v", narrow, full)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, result)
	}
}
