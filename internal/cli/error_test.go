package cli

import (
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
	fs, jsonOut := newFlags("probe")
	yes := fs.Bool("yes", false, "")
	refresh := fs.Bool("refresh", false, "")
	from := fs.String("from", "", "")
	positionals, err := parseCommandFlags(fs, []string{"feature/test", "--yes", "--refresh", "--from", "main", "--json", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if !*yes || !*refresh || *from != "main" || len(positionals) != 2 || positionals[0] != "feature/test" || positionals[1] != "other" {
		t.Fatalf("mixed argument order lost: %v", positionals)
	}
	if !*jsonOut {
		t.Fatal("JSON flag was lost")
	}
}
