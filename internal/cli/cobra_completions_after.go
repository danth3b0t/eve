package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"eve/internal/git"
	"eve/internal/platform"
	"eve/internal/state"
)

// Register profile name completion without opening token objects. A missing
// profile registry produces no dynamic candidate; static command text remains.
func profileCompletion(command *cobra.Command) {
	_ = command.RegisterFlagCompletionFunc("profile", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, completionBudget)
		defer cancel()
		out, err := profileCompletionCandidates(ctx, toComplete)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
}

func profileCompletionCandidates(ctx context.Context, prefix string) ([]string, error) {
	paths, err := platform.DefaultPaths()
	if err != nil {
		return nil, err
	}
	store, err := state.OpenReadOnly(ctx, paths.State)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	profiles, err := store.ManagementProfiles(ctx, "convex")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, profile := range profiles {
		if hasCompletionPrefix(profile.Name, prefix) && completionSafeName.MatchString(profile.Name) {
			out = append(out, profile.Name+"\tConvex team credential profile")
		}
	}
	return out, nil
}

func localRefCompletion(command *cobra.Command) {
	_ = command.RegisterFlagCompletionFunc("from", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, completionBudget)
		defer cancel()
		out, err := localRefCompletionCandidates(ctx, toComplete)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
}

func localRefCompletionCandidates(ctx context.Context, prefix string) ([]string, error) {
	client, err := git.New()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	refs, err := client.LocalBranches(ctx, cwd)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ref := range refs {
		if hasCompletionPrefix(ref, prefix) && completionSafeName.MatchString(ref) {
			out = append(out, ref+"\tlocal Git branch")
		}
	}
	return out, nil
}

// writeFlagCompletion handles --from/--profile explicitly because Cobra's
// flag-completion hook is unavailable when DisableFlagParsing preserves EVE's
// mixed argument grammar.
func writeFlagCompletion(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) < 3 || args[0] != "__complete" {
		return 0, nil
	}
	flag := args[len(args)-2]
	target := args[len(args)-1]
	var candidates []string
	var err error
	switch flag {
	case "--from":
		ctx, cancel := context.WithTimeout(ctx, completionBudget)
		defer cancel()
		candidates, err = localRefCompletionCandidates(ctx, target)
	case "--profile":
		ctx, cancel := context.WithTimeout(ctx, completionBudget)
		defer cancel()
		candidates, err = profileCompletionCandidates(ctx, target)
	case "--workspace":
		ctx, cancel := context.WithTimeout(ctx, completionBudget)
		defer cancel()
		candidates, err = workspaceCompletionCandidates(ctx, completionCleanup, target)
	default:
		return 0, nil
	}
	if err != nil {
		candidates = nil
	}
	for _, candidate := range candidates {
		if _, err := fmt.Fprintln(stdout, candidate); err != nil {
			return 0, err
		}
	}
	if _, err := fmt.Fprintln(stdout, ":4"); err != nil {
		return 0, err
	}
	_, err = fmt.Fprintln(stderr, "Completion ended with directive: ShellCompDirectiveNoFileComp")
	return 0, err
}
