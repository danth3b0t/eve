package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runHelpProbe(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestHelpIsReadOnlyOutsideRepositoryAndState(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", base+"/config")
	t.Setenv("XDG_STATE_HOME", base+"/state")
	for _, tc := range [][]string{
		{},
		{"help"},
		{"--help"},
		{"-h"},
		{"help", "destroy"},
		{"destroy", "feature/payments", "--help"},
		{"auth", "convex", "login", "--help"},
		{"help", "keys"},
		{"help", "version"},
		{"help", "completion"},
	} {
		code, stdout, stderr := runHelpProbe(t, tc...)
		if code != 0 || stdout == "" || stderr != "" {
			t.Fatalf("help %v: code=%d stdout=%q stderr=%q", tc, code, stdout, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "state")); err == nil {
		t.Fatal("help created state")
	}

	_, stdout, _ := runHelpProbe(t, "help", "destroy")
	for _, phrase := range []string{"Delete one exact owned", "prunes only an unchanged EVE-created branch", "--discard-changes"} {
		if !strings.Contains(stdout, phrase) {
			t.Fatalf("destroy help missing %q:\n%s", phrase, stdout)
		}
	}
}

func TestHelpTerminatorIsLiteral(t *testing.T) {
	code, _, stderr := runHelpProbe(t, "destroy", "--", "--help")
	if code == 0 || strings.Contains(stderr, "Deletes the exact owned") {
		t.Fatalf("literal terminator became help: code=%d stderr=%s", code, stderr)
	}
	if code := runUsageCode(t, "help", "nonsense"); code != 2 {
		t.Fatalf("unknown topic code=%d", code)
	}
}

func runUsageCode(t *testing.T, args ...string) int {
	t.Helper()
	code, _, _ := runHelpProbe(t, args...)
	return code
}

func TestCompletionGeneratorsUseCommandTreeOnly(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		base := t.TempDir()
		t.Setenv("HOME", base)
		t.Setenv("XDG_CONFIG_HOME", base+"/config")
		t.Setenv("XDG_STATE_HOME", base+"/state")
		code, stdout, stderr := runHelpProbe(t, "completion", shell)
		if code != 0 || stderr != "" || !strings.Contains(stdout, "__complete") {
			t.Fatalf("%s completion: code=%d stderr=%s", shell, code, stderr)
		}
		probeCode, probeOut, _ := runHelpProbe(t, "__complete", "")
		for _, command := range []string{"create", "destroy", "keys", "completion"} {
			if !strings.Contains(probeOut, command) || probeCode != 0 {
				t.Fatalf("%s dynamic command completion missing %s: %d %s", shell, command, probeCode, probeOut)
			}
		}
		if _, err := os.Stat(filepath.Join(base, "state")); err == nil {
			t.Fatalf("%s completion created state", shell)
		}
	}
}
