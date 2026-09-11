package cli

import (
	"bytes"
	"encoding/json"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLIForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestCompletionFamilyOverviewAndUsageErrors(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("EVE_STATE_DIR", filepath.Join(base, "eve-state"))

	for _, args := range [][]string{{"completion"}, {"completion", "--help"}, {"help", "completion"}} {
		code, stdout, stderr := runCLIForTest(t, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: code=%d stderr=%s", args, code, stderr)
		}
		for _, expected := range []string{"bash", "zsh", "setup", "eve completion setup", "No files or EVE state", "shell startup files"} {
			if !strings.Contains(stdout, expected) {
				t.Fatalf("%v overview missing %q:\n%s", args, expected, stdout)
			}
		}
	}

	code, _, stderr := runCLIForTest(t, "completion", "fish")
	if code != 2 || !strings.Contains(stderr, `unsupported completion target "fish"`) || strings.Contains(stderr, "state.sqlite") {
		t.Fatalf("unsupported shell: code=%d stderr=%s", code, stderr)
	}
	code, stdout, stderr := runCLIForTest(t, "completion", "bash", "--json")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "unknown flag: --json") {
		t.Fatalf("generator JSON: code=%d stdout=%q stderr=%s", code, stdout, stderr)
		code, stdout, stderr = runCLIForTest(t, "completion", "setup", "--shell", "fish")
		if code != 2 || stdout != "" || !strings.Contains(stderr, `unsupported setup shell "fish"`) {
			t.Fatalf("unsupported setup shell: code=%d stdout=%q stderr=%s", code, stdout, stderr)
		}
		code, stdout, stderr = runCLIForTest(t, "completion", "setup", "--shell")
		if code != 2 || stdout != "" || !strings.Contains(stderr, "flag needs an argument: --shell") {
			t.Fatalf("missing setup shell value: code=%d stdout=%q stderr=%s", code, stdout, stderr)
		}

	}
	code, stdout, stderr = runCLIForTest(t, "completion", "bash", "zsh")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "no extra operand") {
	}
	if _, err := os.Stat(filepath.Join(base, "eve-state")); err == nil {
		t.Fatal("informational completion created EVE state")
	}
}

func TestCompletionSetupSelectionAndJSON(t *testing.T) {
	base := t.TempDir()
	for _, tc := range []struct {
		name, explicit, shellEnv, selected, source string
	}{
		{"explicit wins", "bash", "/bin/zsh", "bash", "explicit --shell"},
		{"auto hint", "auto", "/my shell/zsh", "zsh", "configured-shell hint"},
		{"malicious unsupported", "auto", "$(touch /tmp/eve-shell-marker)", "unspecified", "no usable configured-shell hint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", base)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
			t.Setenv("SHELL", tc.shellEnv)
			args := []string{"completion", "setup", "--shell", tc.explicit}
			code, stdout, stderr := runCLIForTest(t, args...)
			if code != 0 || stderr != "" || !strings.Contains(stdout, "Selected guide: "+tc.selected) || !strings.Contains(stdout, tc.source) {
				t.Fatalf("setup: code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, "#compdef") || strings.Contains(stdout, "# bash completion") {
				t.Fatal("setup emitted raw script")
			}
			if tc.name == "malicious unsupported" {
				if shell := recognizedShellHint(tc.shellEnv); shell != "" {
					t.Fatalf("malicious shell hint was accepted as %q", shell)
				}
				if _, err := os.Stat("/tmp/eve-shell-marker"); err == nil {
					t.Fatal("setup evaluated the unverified SHELL hint")
				}
			}
			if tc.name == "explicit wins" {
				for _, expected := range []string{"_get_comp_words_by_ref", "source <(eve completion bash)", "complete -p eve", "Current session", "Persistent", "Removal"} {
					if !strings.Contains(stdout, expected) {
						t.Fatalf("Bash guide missing %q", expected)
					}
				}
			}
		})
	}
	t.Setenv("SHELL", "/bin/bash")
	code, stdout, stderr := runCLIForTest(t, "completion", "setup", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("setup JSON: %d %s", code, stderr)
	}
	var envelope struct {
		OK    bool
		Setup completionSetupGuide `json:"setup"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || !envelope.OK || envelope.Setup.SelectedShell != "bash" || envelope.Setup.ParentShellActivation != "not_checked" || envelope.Setup.NoFilesChanged != true {
		t.Fatalf("structured setup: %v %#v", err, envelope)
	}
	if strings.Contains(stdout, "#compdef") || strings.Contains(stdout, "complete -o") {
		t.Fatal("setup JSON embedded raw script")
	}
}

func TestNestedHelpUsesRealCommandAncestry(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	cases := []struct {
		args     []string
		path     string
		required []string
	}{
		{[]string{"help", "completion", "zsh"}, "eve completion zsh", []string{"--no-descriptions", "compinit", "eve completion zsh >"}},
		{[]string{"completion", "bash", "--help"}, "eve completion bash", []string{"--no-descriptions", "_get_comp_words_by_ref"}},
		{[]string{"auth", "convex", "login", "--profile", "work", "--help"}, "eve auth convex login", []string{"--project", "--profile", `"default"`, "--token-stdin"}},
		{[]string{"help", "auth", "convex", "logout"}, "eve auth convex logout", []string{"--profile", "does not revoke"}},
		{[]string{"help", "state"}, "eve state", []string{"recorded versus observed"}},
		{[]string{"help", "cleanup"}, "eve cleanup", []string{"exact cleanup"}},
	}
	for _, tc := range cases {
		code, stdout, stderr := runCLIForTest(t, tc.args...)
		if code != 0 || stderr != "" || !strings.Contains(stdout, tc.path) {
			t.Fatalf("%v: code=%d stderr=%s stdout=%s", tc.args, code, stderr, stdout)
		}
		for _, expected := range tc.required {
			if !strings.Contains(stdout, expected) {
				t.Fatalf("%v missing %q:\n%s", tc.args, expected, stdout)
			}
		}
	}
	code, stdout, stderr := runCLIForTest(t, "help", "completion", "bash", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("JSON nested help: %d %s", code, stderr)
	}
	var envelope struct {
		OK   bool
		Help helpReference `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || envelope.Help.Path != "eve completion bash" {
		t.Fatalf("structured nested help: %v %#v", err, envelope)
	}
}

func TestHelpTerminatorDoesNotBecomeMode(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("EVE_STATE_DIR", filepath.Join(base, "state"))
	code, stdout, stderr := runCLIForTest(t, "destroy", "--", "--help")
	if code == 0 || strings.Contains(stdout+stderr, "Delete one exact owned") {
		t.Fatalf("-- after terminator entered help: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "schema_version") {
		t.Fatalf("literal --json entered JSON output: %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(base, "state")); err == nil {
		t.Fatal("terminator path created state")
	}
}

func TestJSONCapableParseErrorUsesEnvelope(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	code, stdout, stderr := runCLIForTest(t, "create", "feature/x", "--unknown", "--json")
	if code != 2 || stderr != "" {
		t.Fatalf("JSON usage error: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var envelope struct {
		OK    bool           `json:"ok"`
		Error commandError   `json:"error"`
		Help  *helpReference `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || envelope.OK || envelope.Error.Code != "E_USAGE" || envelope.Help != nil {
		t.Fatalf("parse envelope: %v %#v", err, envelope)
	}
	if envelope.Error.Details["usage"] == nil || envelope.Error.Details["help_command"] == nil {
		t.Fatalf("missing scoped parse guidance: %#v", envelope.Error.Details)
	}
}

func TestCommandMetadataMatchesActualFlagsAndExamples(t *testing.T) {
	options := newCommandOptions(&bytes.Buffer{})
	root := newCommandTree(t.Context(), options)
	var errors []string
	var walk func(command *cobra.Command) error
	walk = func(command *cobra.Command) error {
		if command.Hidden || strings.HasPrefix(command.Name(), "__complete") {
			return nil
		}
		path := command.CommandPath()
		meta := metadataForPath(path)
		if meta.Purpose == "" || meta.Purpose == "Manage EVE." {
			errors = append(errors, "missing metadata: "+path)
		}
		options := registeredHelpFlags(command)
		seen := map[string]bool{}
		for _, option := range options {
			seen[option.Name] = true
		}
		command.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
			if !seen[flag.Name] {
				errors = append(errors, "flag absent from help: "+path+" --"+flag.Name)
			}
			if _, hasCompletion := command.GetFlagCompletionFunc(flag.Name); !hasCompletion && flag.Name != "help" && flag.Name != "no-context" {
				errors = append(errors, "flag lacks completion policy: "+path+" --"+flag.Name)
			}
		})
		for _, example := range meta.Examples {
			if !strings.HasPrefix(example, "eve ") && !strings.HasPrefix(example, "source <(") {
				errors = append(errors, "example has no eve command: "+example)
			}
		}
		for _, child := range command.Commands() {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		t.Fatal(err)
	}
	if len(errors) != 0 {
		t.Fatalf("grammar/help drift:\n%s", strings.Join(errors, "\n"))
	}
}

func TestCandidateEncodingBoundsAndControls(t *testing.T) {
	var values []candidate
	for index := 0; index < maxCompletionCandidates+20; index++ {
		values = append(values, candidate{Insert: strings.Repeat("x", 3+index%20) + string(rune('a'+index%26)), Description: "state\twith\ncontrols", Kind: "workspace"})
	}
	out, truncated := encodeCandidateSet(values)
	if len(out) != maxCompletionCandidates || !truncated {
		t.Fatalf("candidate count cap: %d %t", len(out), truncated)
	}
	if strings.Contains(strings.Join(out, ""), "\nstate") {
		t.Fatal("description control leaked into a new candidate")
	}
	if _, ok := validProtocolInsertion("feature\nunsafe"); ok {
		t.Fatal("control-containing insertion was accepted")
	}
}
