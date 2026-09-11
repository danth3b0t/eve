package cli

import (
	"context"
	"os"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"eve/internal/git"
	"eve/internal/platform"
	"eve/internal/state"
)

const completionBudget = 150 * time.Millisecond

var completionSafeName = regexp.MustCompile(`^[A-Za-z0-9._~@/:+-]+$`)

type workspaceCompletionKind string

const (
	completionSelector workspaceCompletionKind = "selector"
	completionResume   workspaceCompletionKind = "resume"
	completionDestroy  workspaceCompletionKind = "destroy"
	completionSync     workspaceCompletionKind = "sync"
	completionCleanup  workspaceCompletionKind = "cleanup"
)

func workspaceCompletion(kind workspaceCompletionKind) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		baseCtx := command.Context()
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(baseCtx, completionBudget)
		defer cancel()
		candidates, err := workspaceCompletionCandidates(ctx, kind, toComplete)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return candidates, cobra.ShellCompDirectiveNoFileComp
	}
}

// workspaceCompletionCandidates uses a timeout and read-only metadata. Missing,
// busy, newer, or unavailable contexts always degrade to no dynamic candidates.
func workspaceCompletionCandidates(ctx context.Context, kind workspaceCompletionKind, prefix string) ([]string, error) {
	client, err := git.New()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	checkout, err := client.Inspect(ctx, cwd)
	if err != nil {
		return nil, err
	}
	paths, err := platform.DefaultPaths()
	if err != nil {
		return nil, err
	}
	store, err := state.OpenReadOnly(ctx, paths.State)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	repository, err := store.RepositoryForCommon(ctx, checkout.Identity.CommonDir, checkout.Identity.CommonIdentity)
	if err != nil {
		return nil, err
	}
	workspaces, err := store.ListWorkspaces(ctx, repository.ID, false)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, workspace := range workspaces {
		description := workspace.Branch + " " + workspace.State
		var name string
		switch {
		case completionSafeName.MatchString(workspace.Branch):
			name = workspace.Branch
		default:
			name = workspace.ID
			description = workspace.ID
		}
		switch kind {
		case completionResume:
			if workspace.State == "prepared" || workspace.State == "destroyed" {
				continue
			}
		case completionSync:
			if workspace.State != "prepared" {
				continue
			}
		case completionDestroy, completionCleanup:
			if workspace.State == "destroyed" {
				continue
			}
		}
		if name < prefix && len(prefix) != 0 {
			continue
		}
		if len(prefix) != 0 && !hasCompletionPrefix(name, prefix) {
			continue
		}
		out = append(out, sanitizeCompletionValue(name)+"\t"+sanitizeCompletionValue(description))
	}
	return out, nil
}

func hasCompletionPrefix(candidate, prefix string) bool {
	return len(prefix) <= len(candidate) && candidate[:len(prefix)] == prefix
}

func sanitizeCompletionValue(value string) string {
	for _, r := range value {
		if r < 32 || r == 127 {
			return "-"
		}
	}
	return value
}
