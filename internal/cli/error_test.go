package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"eve/internal/domain"
	"eve/internal/envfile"
)

func TestApprovalAndResumeErrorsKeepDistinctNextActions(t *testing.T) {
	for _, tc := range []struct {
		code string
		want string
	}{
		{"E_APPROVAL_REQUIRED", "review the planned local effects and rerun with the listed safety flags"},
		{"E_RESUME_REQUIRED", "run eve resume with the same branch or workspace ID"},
	} {
		got := errorResult("create", &domain.Error{Code: tc.code, Message: "probe"})
		if got.Error.NextAction != tc.want {
			t.Fatalf("%s next action %q", tc.code, got.Error.NextAction)
		}
	}
}

func TestSafeEnvfileErrorsRetainClassificationAndDetails(t *testing.T) {
	err := &envfile.Error{Code: "E_ENV_SYNTAX", Key: "PORT", Lines: []int{3}, Reason: "quoted value is unterminated"}
	got := errorResult("sync", err)
	if got.Error.Code != "E_ENV_SYNTAX" || got.Error.Message != "quoted value is unterminated" || exitCode(err) != 2 {
		t.Fatalf("env error normalized incorrectly: %#v exit=%d", got.Error, exitCode(err))
	}
	if got.Error.Details["key"] != "PORT" {
		t.Fatal("env key detail lost")
	}
	lines, ok := got.Error.Details["lines"].([]int)
	if !ok || len(lines) != 1 || lines[0] != 3 {
		t.Fatalf("env line detail lost: %#v", got.Error.Details["lines"])
	}
}

func TestMissingProviderObjectGetsExplicitClosedWorldRouting(t *testing.T) {
	err := &domain.Error{Code: "E_PROVIDER_NOT_FOUND", Message: "recorded deployment no longer exists"}
	got := errorResult("resume", err)
	if exitCode(err) != 7 || !strings.Contains(got.Error.NextAction, "doctor --remote") {
		t.Fatalf("missing provider object routing: %#v exit=%d", got.Error, exitCode(err))
	}
}

func TestCommandFlagsUseDocumentedMixedOrder(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", base+"/config")
	t.Setenv("EVE_ACTIVE_HELP", "0")
	cases := [][]string{
		{"create", "feature/test", "--yes", "--from", "main", "--dry-run", "--json", "--help"},
		{"create", "--from=main", "--yes", "feature/test", "--dry-run", "--json", "--help"},
		{"status", "feature/test", "--refresh", "--json", "--help"},
		{"status", "--refresh", "feature/test", "--json", "--help"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), args, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
			t.Fatalf("%v: code=%d stderr=%s", args, code, stderr.String())
		}
		var parsed struct {
			OK   bool `json:"ok"`
			Help struct {
				Path    string     `json:"path"`
				Options []helpFlag `json:"options"`
			} `json:"help"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil || !parsed.OK || parsed.Help.Path != commandPath(args[0]) {
			t.Fatalf("help did not use the selected command/flags %v: %v %s", args, err, stdout.String())
		}
		jsonOption := false
		for _, option := range parsed.Help.Options {
			jsonOption = jsonOption || option.Name == "json"
		}
		if !jsonOption {
			t.Fatalf("actual JSON option absent from help %v: %#v", args, parsed.Help.Options)
		}
	}
}

func commandPath(command string) string { return "eve " + command }
