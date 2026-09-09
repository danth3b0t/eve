package state

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUncommittedStateProcess(t *testing.T) {
	root := os.Getenv("EVE_TEST_UNCOMMITTED_STATE")
	if root == "" {
		return
	}
	s, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.transaction(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE repositories SET label='uncommitted-change'`); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "transaction-open")
		<-time.After(time.Minute)
		return context.DeadlineExceeded
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcessDeathRollsBackUncommittedState(t *testing.T) {
	s, r := fixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestUncommittedStateProcess$")
	cmd.Env = append(os.Environ(), "EVE_TEST_UNCOMMITTED_STATE="+s.root)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	marker := make([]byte, len("transaction-open\n"))
	if _, err := io.ReadFull(output, marker); err != nil || string(marker) != "transaction-open\n" {
		t.Fatalf("worker did not reach the open transaction: %q %v", marker, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	reopened, err := Open(t.Context(), s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Repository(t.Context(), r.ID)
	if err != nil || got.Label != "fixture" {
		t.Fatalf("uncommitted write survived process death: %+v %v", got, err)
	}
	// Another write must be possible after the kernel released the dead
	// process's transaction locks. Do not infer recovery only from a read.
	if _, err := reopened.db.Exec(`UPDATE repositories SET label='after-recovery' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledOpenAndStateReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-created")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Open(ctx, path); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cancelled open created state")
	}
	s, r := fixture(t)
	if err := os.Rename(filepath.Join(s.root, "pending"), filepath.Join(s.root, "pending-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.root, "pending"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := s.Repository(t.Context(), r.ID)
	code(t, err, "E_STATE_IDENTITY")
}
