package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIncompleteCheckoutCannotBeAdoptedAndCancellationKillsFilter(t *testing.T) {
	f := newRepo(t)
	f.put(".gitattributes", "app.txt filter=probe\n")
	f.git(f.root, "add", ".gitattributes")
	f.git(f.root, "commit", "-qm", "ordinary checkout filter")
	filter := filepath.Join(f.scratch, "filter")
	started, release, survived := filepath.Join(f.scratch, "started"), filepath.Join(f.scratch, "release"), filepath.Join(f.scratch, "survived")
	script := "#!/bin/sh\nprintf started > \"$1\"\nwhile [ ! -e \"$2\" ]; do sleep 0.02; done\nprintf survived > \"$3\"\ncat\n"
	if err := os.WriteFile(filter, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	f.git(f.root, "config", "filter.probe.smudge", quote(filter)+" "+quote(started)+" "+quote(release)+" "+quote(survived))
	f.git(f.root, "config", "filter.probe.required", "true")
	request := f.request("slow-checkout")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.client.Add(ctx, request); done <- err }()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Git did not reach filter: %v", err)
		case <-ctx.Done():
			t.Fatal("filter did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	_, observedErr := f.client.ObserveCreation(t.Context(), request)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Git did not stop on cancellation")
	}
	if observedErr == nil {
		t.Fatal("incomplete checkout was accepted as a completed creation receipt")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// A surviving shell/filter would now finish and write its marker. The test
	// never relies on PID-file existence to authorize production cleanup.
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(survived); !os.IsNotExist(err) {
		t.Fatal("checkout filter survived process-group cancellation")
	}
	if _, err := f.client.ObserveCreation(t.Context(), request); err == nil {
		t.Fatal("interrupted checkout was adopted")
	}
}
