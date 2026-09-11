package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"eve/internal/git"
	"eve/internal/platform"
	"eve/internal/state"
)

const helpContextBudget = 200 * time.Millisecond

type helpOptions struct {
	NoContext   bool
	Interactive bool
}

type helpContextSnapshot struct {
	StatePath       string
	StateSource     string
	Registry        string
	Repository      string
	Manifest        string
	Profiles        []string
	Workspace       string
	WorkspaceCount  int
	UnfinishedCount int
	Unavailable     []string
}

func terminalHuman(stdout io.Writer) bool {
	file, ok := stdout.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func collectHelpContext(ctx context.Context) helpContextSnapshot {
	ctx, cancel := context.WithTimeout(ctx, helpContextBudget)
	defer cancel()
	snapshot := helpContextSnapshot{Registry: "not_checked", Repository: "outside repository", Manifest: "not_checked"}
	paths, err := platform.DefaultPaths()
	if err != nil {
		snapshot.Unavailable = append(snapshot.Unavailable, "state path")
		return snapshot
	}
	snapshot.StatePath = paths.State
	if os.Getenv("EVE_STATE_DIR") != "" {
		snapshot.StateSource = "EVE_STATE_DIR"
	} else {
		snapshot.StateSource = "platform default"
	}

	currentRoot := ""
	var client *git.Client
	client, err = git.New()
	if err == nil {
		cwd, cwdErr := os.Getwd()
		if cwdErr == nil {
			checkout, inspectErr := client.Inspect(ctx, cwd)
			if inspectErr == nil {
				snapshot.Repository = checkout.Identity.Path
				currentRoot = checkout.Identity.Path
				snapshot.Manifest = "absent at working copy"
				if _, statErr := os.Stat(filepath.Join(checkout.Identity.Path, "eve.toml")); statErr == nil {
					if _, manifestErr := manifestForContext(ctx, client, checkout); manifestErr == nil {
						snapshot.Manifest = "committed in current target"
					}
				}
			}
		}
	}

	info, err := os.Lstat(snapshot.StatePath)
	if err != nil || !info.IsDir() {
		snapshot.Registry = "absent"
		return snapshot
	}
	store, err := state.OpenReadOnly(ctx, snapshot.StatePath)
	if err != nil {
		snapshot.Registry = "unavailable"
		return snapshot
	}
	defer store.Close()
	snapshot.Registry = "readable"
	profiles, err := store.ManagementProfiles(ctx, "convex")
	if err == nil {
		for _, profile := range profiles {
			snapshot.Profiles = append(snapshot.Profiles, profile.Name)
		}
	}
	if currentRoot != "" {
		if checkout, checkoutErr := client.Inspect(ctx, currentRoot); checkoutErr == nil {
			snapshot.Repository = "unregistered"
			if repo, repoErr := store.RepositoryForCommon(ctx, checkout.Identity.CommonDir, checkout.Identity.CommonIdentity); repoErr == nil {
				snapshot.Repository = repo.SourcePath
				workspaces, listErr := store.ListWorkspaces(ctx, repo.ID, false)
				if listErr == nil {
					snapshot.WorkspaceCount = len(workspaces)
					for _, workspace := range workspaces {
						if workspace.State != "prepared" && workspace.State != "destroyed" {
							snapshot.UnfinishedCount++
						}
						if workspace.Path == currentRoot {
							snapshot.Workspace = fmt.Sprintf("%s (%s generation %d)", workspace.Branch, workspace.State, workspace.Generation)
						}
					}
				}
			}
		}
	}
	return snapshot
}

func manifestForContext(ctx context.Context, client *git.Client, checkout git.Checkout) ([]byte, error) {
	return client.Manifest(ctx, checkout.Identity.Path, checkout.HeadOID)
}

func contextSection(ctx context.Context, options helpOptions) string {
	if options.NoContext || (!options.Interactive && os.Getenv("EVE_HELP_CONTEXT") != "1") {
		return ""
	}
	snapshot := collectHelpContext(ctx)
	var text strings.Builder
	text.WriteString("\nHere:\n")
	fmt.Fprintf(&text, "  State path: %s (%s)\n", snapshot.StatePath, snapshot.StateSource)
	fmt.Fprintf(&text, "  Registry evidence: %s\n", snapshot.Registry)
	fmt.Fprintf(&text, "  Repository: %s\n", snapshot.Repository)
	fmt.Fprintf(&text, "  Manifest: %s\n", snapshot.Manifest)
	if len(snapshot.Profiles) == 0 {
		text.WriteString("  Convex profiles: none recorded\n")
	} else {
		fmt.Fprintf(&text, "  Convex profiles: %s recorded; authorization not checked\n", strings.Join(snapshot.Profiles, ", "))
	}
	if snapshot.Workspace != "" {
		fmt.Fprintf(&text, "  Current recorded workspace: %s\n", snapshot.Workspace)
	}
	fmt.Fprintf(&text, "  Live workspace records: %d; unfinished operations: %d\n", snapshot.WorkspaceCount, snapshot.UnfinishedCount)
	text.WriteString("  Saved records are not proof of current files, credentials, processes, or remote deployment state.\n")
	return text.String()
}
