package cli

import (
	"testing"

	"eve/internal/domain"
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
