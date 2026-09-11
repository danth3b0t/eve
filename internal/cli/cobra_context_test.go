package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextSectionUsesBoundedLocalEvidence(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", base+"/config")
	t.Setenv("XDG_STATE_HOME", base+"/state")
	section := contextSection(t.Context(), helpOptions{Interactive: true})
	for _, required := range []string{"Here:", "State path:", "Registry evidence: absent", "Convex profiles: not checked", "Live workspace records: unavailable", "not proof"} {
		if !strings.Contains(section, required) {
			t.Fatalf("context missing %q:\n%s", required, section)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "state")); err == nil {
		t.Fatal("context created state")
	}
	if section = contextSection(t.Context(), helpOptions{NoContext: true, Interactive: true}); section != "" {
		t.Fatalf("--no-context did not suppress context: %q", section)
	}
}
