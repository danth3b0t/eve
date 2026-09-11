package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncDryRunResolvesWithoutJournalOrWrite(t *testing.T) {
	r, plan := syncLocalFixture(t)
	upgradeManifest(plan, "version = 1\n[services.api]\npath = \".\"\nenv_file = \".env.local\"\n[services.api.env]\nPUBLIC_NAME = \"new\"\nADDED = \"value\"\n", t)
	result, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RestartRequired || result.Workspace.Generation != 1 || result.Workspace.State != "prepared" {
		t.Fatalf("dry-run generated workspace instead of previewing: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(plan.Path, ".env.local"))
	if err != nil || !strings.Contains(string(data), "PUBLIC_NAME=old\n") || strings.Contains(string(data), "ADDED=value") {
		t.Fatalf("dry-run changed managed content:\n%s", data)
	}
	after, err := SyncWorkspace(t.Context(), r.store, r.client, plan.WorkspaceID, SyncOptions{})
	if err != nil || after.Workspace.Generation != 2 || !after.RestartRequired {
		t.Fatalf("dry-run left an incompatible sync journal: %+v %v", after, err)
	}
}
