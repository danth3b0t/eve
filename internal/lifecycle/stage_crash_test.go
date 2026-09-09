package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"eve/internal/files"
	"eve/internal/git"
	"eve/internal/state"
)

// The helper stops between durable object writes and SQL acknowledgment, with
// its real workspace OS lock still held. Only non-secret metadata crosses stdout.
func TestStageObjectsProcess(t *testing.T) {
	root := os.Getenv("EVE_TEST_STAGE_SOURCE")
	if root == "" {
		return
	}
	base := filepath.Dir(root)
	g, err := git.New()
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenForGit(t.Context(), g, root, filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := s.LockWorkspace(os.Getenv("EVE_TEST_STAGE_ID"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	step, err := w.FileStep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	source, err := g.Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := g.Manifest(t.Context(), root, step.Workspace.HeadOID)
	if err != nil {
		t.Fatal(err)
	}
	target := git.Target{Branch: step.Workspace.Branch, HeadOID: step.Workspace.HeadOID, Manifest: raw}
	plan, err := files.Snapshot(t.Context(), g, source.Identity, target)
	if err != nil {
		t.Fatal(err)
	}
	p := GitPlan{WorkspaceID: step.Workspace.ID, Path: step.Workspace.Path, Target: target, Files: plan}
	in := interruptedStage(t, repository{root, base, g, s}, p, w, os.Getenv("EVE_TEST_STAGE_COMPLETE") == "true")
	if err := json.NewEncoder(os.Stdout).Encode(in); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}
func killStagingProcess(t *testing.T, r repository, p GitPlan, complete bool) state.FileIntent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStageObjectsProcess$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "EVE_TEST_STAGE_SOURCE="+r.root, "EVE_TEST_STAGE_ID="+p.WorkspaceID, "EVE_TEST_STAGE_COMPLETE="+fmt.Sprint(complete))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	var in state.FileIntent
	ready := make(chan error, 1)
	go func() { ready <- json.NewDecoder(io.LimitReader(stdout, 1<<20)).Decode(&in) }()
	select {
	case err := <-ready:
		if err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("staging helper failed: %v %s", err, stderr.String())
		}
	case <-ctx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("staging helper timed out: %s", stderr.String())
	}
	// SIGKILL executes no Go finalizers or lock.Close calls in the helper.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper exited without being killed")
	}
	return in
}
