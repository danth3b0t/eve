package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"eve/internal/config"
	"eve/internal/git"
	"eve/internal/platform"
	"eve/internal/state"
)

var completionBudget = 150 * time.Millisecond

const (
	maxCompletionCandidates = 200
	maxCompletionBytes      = 64 * 1024
)

type lookupStatus string

const (
	lookupAvailable   lookupStatus = "available"
	lookupAbsent      lookupStatus = "absent"
	lookupUnavailable lookupStatus = "unavailable"
	lookupTruncated   lookupStatus = "truncated"
)

type candidate struct {
	Insert      string
	Description string
	Kind        string
	Priority    int
}

type workspaceCompletionKind string

const (
	completionSelector workspaceCompletionKind = "selector"
	completionResume   workspaceCompletionKind = "resume"
	completionDestroy  workspaceCompletionKind = "destroy"
	completionSync     workspaceCompletionKind = "sync"
	completionCreate   workspaceCompletionKind = "create"
)

type completionMetadata struct {
	client       *git.Client
	cwd          string
	checkout     *git.Checkout
	store        *state.Store
	repositoryID string
	currentPath  string
	status       lookupStatus
}

func lookupContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	base := cmd.Context()
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, completionBudget)
}

func openCompletionMetadata(ctx context.Context) (*completionMetadata, error) {
	metadata := &completionMetadata{status: lookupUnavailable}
	client, err := git.New()
	if err == nil {
		metadata.client = client
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			metadata.cwd = cwd
			if checkout, inspectErr := client.InspectForCompletion(ctx, cwd); inspectErr == nil {
				checkout := checkout
				metadata.checkout = &checkout
				metadata.currentPath = checkout.Identity.Path
			}
		}
	}
	paths, pathErr := platform.DefaultPaths()
	if pathErr != nil {
		return metadata, pathErr
	}
	if info, statErr := os.Lstat(paths.State); statErr != nil || !info.IsDir() {
		metadata.status = lookupAbsent
		return metadata, nil
	}
	store, storeErr := state.OpenReadOnlyWithBusyTimeout(ctx, paths.State, 50)
	if storeErr != nil {
		return metadata, nil // unavailable registry degrades to static/no candidates
	}
	metadata.store = store
	metadata.status = lookupAvailable
	if metadata.checkout != nil {
		if repository, repositoryErr := store.RepositoryForCommon(ctx, metadata.checkout.Identity.CommonDir, metadata.checkout.Identity.CommonIdentity); repositoryErr == nil {
			metadata.repositoryID = repository.ID
		}
	}
	return metadata, nil
}

func (m *completionMetadata) close() {
	if m != nil && m.store != nil {
		_ = m.store.Close()
	}
}

func noFileAdapter(providers func(context.Context) ([]candidate, lookupStatus, error), emptyHint string) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		ctx, cancel := lookupContext(cmd)
		defer cancel()
		values, status, err := providers(ctx)
		if err != nil || ctx.Err() != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out, truncated := encodeCandidateSet(values)
		if truncated && status == lookupAvailable {
			status = lookupTruncated
		}
		_ = status
		if len(out) == 0 && toComplete == "" && emptyHint != "" {
			out = cobra.AppendActiveHelp(out, emptyHint)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

func encodeCandidateSet(values []candidate) ([]string, bool) {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	size := 0
	truncated := false
	for _, value := range values {
		insert, ok := validProtocolInsertion(value.Insert)
		if !ok || seen[insert] {
			continue
		}
		description := sanitizeCompletionDescription(value.Description)
		line := insert
		if description != "" {
			line += "\t" + description
		}
		if size+len(line)+1 > maxCompletionBytes || len(out) >= maxCompletionCandidates {
			truncated = true
			break
		}
		seen[insert] = true
		size += len(line) + 1
		out = append(out, line)
	}
	return out, truncated
}

func validProtocolInsertion(value string) (string, bool) {
	if value == "" || !utf8.ValidString(value) {
		return "", false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return "", false
		}
	}
	return value, true
}

func sanitizeCompletionDescription(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r == '\t' || r == '\r' || r == '\n' || r < 32 || r == 127 {
			out.WriteByte(' ')
		} else {
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}

func hasCompletionPrefix(candidate, prefix string) bool {
	return len(prefix) <= len(candidate) && candidate[:len(prefix)] == prefix
}

func flagsOnlyCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	flags := cmd.Flags()
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || flag.Changed {
			return
		}
		name := "--" + flag.Name
		if !hasCompletionPrefix(name, toComplete) {
			return
		}
		out = append(out, name+"\t"+sanitizeCompletionDescription(flag.Usage))
	})
	return out, cobra.ShellCompDirectiveNoFileComp
}

func workspaceCompletion(kind workspaceCompletionKind) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != 0 {
			return flagsOnlyCompletion(cmd, args, toComplete)
		}
		return noFileAdapter(func(ctx context.Context) ([]candidate, lookupStatus, error) {
			metadata, err := openCompletionMetadata(ctx)
			if err != nil {
				return nil, lookupUnavailable, nil
			}
			defer metadata.close()
			if metadata.repositoryID == "" {
				return nil, metadata.status, nil
			}
			rows, err := metadata.store.CompletionWorkspaces(ctx, metadata.repositoryID, toComplete, false, 500)
			if err != nil {
				return nil, lookupUnavailable, nil
			}
			var out []candidate
			for _, row := range rows {
				if !workspaceRowAccepted(row, kind) {
					continue
				}
				insert, description := workspaceCandidateValue(row, toComplete, kind)
				if insert == "" {
					continue
				}
				priority := 0
				if row.Path == metadata.currentPath {
					priority = -100
				}
				out = append(out, candidate{Insert: insert, Description: description, Kind: "workspace", Priority: priority})
			}
			sortCandidates(out)
			return out, lookupStatusFromRows(len(rows), 200), ctx.Err()
		}, "No workspace records in this repository; enter the source checkout or type an exact selector")(cmd, args, toComplete)
	}
}

func lookupStatusFromRows(rows, cap int) lookupStatus {
	if rows > cap {
		return lookupTruncated
	}
	return lookupAvailable
}

func sortCandidates(values []candidate) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Priority != values[j].Priority {
			return values[i].Priority < values[j].Priority
		}
		return values[i].Insert < values[j].Insert
	})
}

func workspaceRowAccepted(row state.CompletionWorkspace, kind workspaceCompletionKind) bool {
	if row.State == "destroyed" {
		return false
	}
	switch kind {
	case completionResume:
		return row.State == "creating" || row.State == "failed" || row.State == "syncing" || row.State == "destroying" || row.State == "cleanup_pending"
	case completionSync:
		return row.State == "prepared" || (row.State == "syncing" && row.OperationCommand == "sync")
	default:
		return true
	}
}

func workspaceCandidateValue(row state.CompletionWorkspace, prefix string, kind workspaceCompletionKind) (string, string) {
	description := workspaceDescription(row, kind)
	if filepath.IsAbs(prefix) && hasCompletionPrefix(row.Path, prefix) {
		return row.Path, description
	}
	if uuidLikePrefix(prefix) && hasCompletionPrefix(row.ID, prefix) {
		return row.ID, description
	}
	if filepath.IsAbs(prefix) || uuidLikePrefix(prefix) {
		return "", description
	}
	if !hasCompletionPrefix(row.Branch, prefix) {
		return "", description
	}
	insert, ok := validProtocolInsertion(row.Branch)
	if ok {
		return insert, description
	}
	return row.ID, description + "; branch cannot cross the shell protocol safely"
}

func uuidLikePrefix(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == '-' {
			continue
		}
		return false
	}
	if !strings.ContainsRune(value, '-') && len(value) < 8 {
		return false
	}
	return true
}

func workspaceDescription(row state.CompletionWorkspace, kind workspaceCompletionKind) string {
	stateDescription := map[string]string{
		"creating":        "continue creation; resume required",
		"failed":          "failed operation; resume required",
		"syncing":         "continue sync",
		"destroying":      "continue deletion",
		"cleanup_pending": "continue cleanup/deletion",
		"prepared":        "prepared configuration",
	}[row.State]
	if stateDescription == "" {
		stateDescription = row.State + " " + row.Phase
	}
	if kind == completionSync && row.State == "prepared" {
		stateDescription = "apply configuration"
	} else if kind == completionSync && row.State == "syncing" {
		stateDescription = "continue sync"
	}
	return row.RepositoryLabel + "; " + stateDescription
}

func globalWorkspaceIDCompletion(command *cobra.Command) {
	_ = command.RegisterFlagCompletionFunc("workspace", workspaceFlagCompletion)
}

func workspaceFlagCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return noFileAdapter(func(ctx context.Context) ([]candidate, lookupStatus, error) {
		metadata, err := openCompletionMetadata(ctx)
		if err != nil {
			return nil, lookupUnavailable, nil
		}
		defer metadata.close()
		if metadata.store == nil {
			return nil, metadata.status, nil
		}
		rows, err := metadata.store.CompletionWorkspaces(ctx, "", toComplete, true, 500)
		if err != nil {
			return nil, lookupUnavailable, nil
		}
		var out []candidate
		for _, row := range rows {
			if toComplete == "" || hasCompletionPrefix(row.ID, toComplete) {
				out = append(out, candidate{Insert: row.ID, Description: row.RepositoryLabel + "; " + row.Branch + "; " + row.State, Kind: "workspace"})
			}
		}
		sortCandidates(out)
		return out, lookupStatusFromRows(len(rows), 200), ctx.Err()
	}, "Use a full workspace ID for targeted cleanup")(cmd, args, toComplete)
}

func profileCompletion(command *cobra.Command, allowNew bool) {
	_ = command.RegisterFlagCompletionFunc("profile", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return noFileAdapter(func(ctx context.Context) ([]candidate, lookupStatus, error) {
			metadata, err := openCompletionMetadata(ctx)
			if err != nil {
				return nil, lookupUnavailable, nil
			}
			defer metadata.close()
			if metadata.store == nil {
				return nil, metadata.status, nil
			}
			profiles, err := metadata.store.ManagementProfiles(ctx, "convex")
			if err != nil {
				return nil, lookupUnavailable, nil
			}
			var out []candidate
			seenDefault := false
			for _, profile := range profiles {
				if profile.Name == "default" {
					seenDefault = true
				}
				if hasCompletionPrefix(profile.Name, toComplete) {
					out = append(out, candidate{Insert: profile.Name, Description: "Convex team credential profile", Kind: "profile"})
				}
			}
			if allowNew && !seenDefault && hasCompletionPrefix("default", toComplete) {
				out = append(out, candidate{Insert: "default", Description: "create/use the conventional profile", Kind: "profile"})
			}
			sortCandidates(out)
			return out, lookupAvailable, ctx.Err()
		}, "Existing profiles; a new valid name may still be typed")(cmd, args, toComplete)
	})
}

func initProfileCompletion(command *cobra.Command) {
	profileCompletion(command, false)
}

func projectCompletion(command *cobra.Command) {
	_ = command.RegisterFlagCompletionFunc("project", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return noFileAdapter(func(ctx context.Context) ([]candidate, lookupStatus, error) {
			metadata, err := openCompletionMetadata(ctx)
			if err != nil {
				return nil, lookupUnavailable, nil
			}
			defer metadata.close()
			var projects []string
			if metadata.checkout != nil {
				if manifest := committedManifest(ctx, metadata); manifest != nil {
					for _, resource := range manifest.Resources {
						if resource.Project != "" {
							projects = append(projects, resource.Project)
						}
					}
				}
			}
			if metadata.store != nil {
				if recorded, recordErr := metadata.store.CompletionProjects(ctx, 200); recordErr == nil {
					projects = append(projects, recorded...)
				}
			}
			var out []candidate
			for _, project := range projects {
				if hasCompletionPrefix(project, toComplete) {
					out = append(out, candidate{Insert: project, Description: "recorded/public project binding", Kind: "project"})
				}
			}
			sortCandidates(out)
			return out, lookupAvailable, ctx.Err()
		}, "Type a lower-case team:project binding; no cloud listing is performed")(cmd, args, toComplete)
	})
}

func committedManifest(ctx context.Context, metadata *completionMetadata) *config.Manifest {
	if metadata.client == nil || metadata.cwd == "" || metadata.checkout == nil || metadata.checkout.HeadOID == "" {
		return nil
	}
	data, err := metadata.client.Manifest(ctx, metadata.checkout.Identity.Path, metadata.checkout.HeadOID)
	if err != nil {
		return nil
	}
	manifest, err := config.Parse(data)
	if err != nil {
		return nil
	}
	return manifest
}

func localRefCompletion(command *cobra.Command) {
	_ = command.RegisterFlagCompletionFunc("from", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return noFileAdapter(func(ctx context.Context) ([]candidate, lookupStatus, error) {
			metadata, err := openCompletionMetadata(ctx)
			if err != nil || metadata.checkout == nil {
				return nil, lookupUnavailable, nil
			}
			defer metadata.close()
			refs, err := metadata.client.CompletionRefs(ctx, metadata.checkout.Identity.Path, toComplete)
			if err != nil {
				return nil, lookupUnavailable, nil
			}
			var out []candidate
			for _, ref := range refs {
				description := map[string]string{"head": "local branch", "tag": "local tag", "remote": "locally cached remote-tracking ref", "HEAD": "current resolved HEAD", "ref": "local ref"}[ref.Kind]
				out = append(out, candidate{Insert: ref.Insert, Description: description, Kind: "ref"})
			}
			sortCandidates(out)
			return out, lookupAvailable, ctx.Err()
		}, "Enter an existing local branch, tag, cached remote ref, or HEAD")(cmd, args, toComplete)
	})
}

func createTargetCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return flagsOnlyCompletion(cmd, args, toComplete)
	}
	return noFileAdapter(func(ctx context.Context) ([]candidate, lookupStatus, error) {
		metadata, err := openCompletionMetadata(ctx)
		if err != nil || metadata.checkout == nil {
			return nil, lookupUnavailable, nil
		}
		defer metadata.close()
		managedRows := []state.CompletionWorkspace{}
		if metadata.store != nil && metadata.repositoryID != "" {
			if rows, rowErr := metadata.store.CompletionWorkspaces(ctx, metadata.repositoryID, toComplete, false, 500); rowErr == nil {
				managedRows = rows
			}
		}
		checked, workErr := metadata.client.WorktreeBranches(ctx, metadata.checkout.Identity.Path)
		if workErr != nil {
			checked = map[string]string{}
		}
		var out []candidate
		managedBranches := map[string]state.CompletionWorkspace{}
		for _, row := range managedRows {
			managedBranches[row.Branch] = row
			_, description := workspaceCandidateValue(row, toComplete, completionSelector)
			if hasCompletionPrefix(row.Branch, toComplete) {
				out = append(out, candidate{Insert: row.Branch, Description: description, Kind: "workspace", Priority: -10})
			}
		}
		branches, branchErr := metadata.client.LocalBranches(ctx, metadata.checkout.Identity.Path)
		if branchErr == nil {
			for _, branch := range branches {
				if !hasCompletionPrefix(branch, toComplete) || managedBranches[branch].ID != "" {
					continue
				}
				_, checkedOut := checked[branch]
				if checkedOut {
					continue
				}
				out = append(out, candidate{Insert: branch, Description: "local branch target", Kind: "branch"})
			}
		}
		sortCandidates(out)
		return out, lookupAvailable, ctx.Err()
	}, "Enter a new branch name; existing managed branches return or resume as recorded")(cmd, args, toComplete)
}

func backendPathCompletion(command *cobra.Command) {
	_ = command.RegisterFlagCompletionFunc("backend-path", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return noFileAdapter(func(ctx context.Context) ([]candidate, lookupStatus, error) {
			metadata, err := openCompletionMetadata(ctx)
			if err != nil || metadata.checkout == nil {
				return nil, lookupUnavailable, nil
			}
			defer metadata.close()
			var out []candidate
			if manifest := committedManifest(ctx, metadata); manifest != nil {
				for _, resource := range manifest.Resources {
					if resource.Provider == "convex" && resource.Path != "" && hasCompletionPrefix(resource.Path, toComplete) {
						out = append(out, candidate{Insert: resource.Path, Description: "declared backend resource path", Kind: "path"})
					}
				}
			}
			if len(out) == 0 {
				paths := discoveredBackendPaths(ctx, metadata)
				for _, path := range paths {
					if hasCompletionPrefix(path, toComplete) {
						out = append(out, candidate{Insert: path, Description: "committed Convex package evidence", Kind: "path"})
					}
				}
			}
			sortCandidates(out)
			return out, lookupAvailable, ctx.Err()
		}, "Use an exact committed Convex package path")(cmd, args, toComplete)
	})
}

func discoveredBackendPaths(ctx context.Context, metadata *completionMetadata) []string {
	if metadata.checkout == nil || metadata.checkout.HeadOID == "" {
		return nil
	}
	tree, err := metadata.client.Tree(ctx, metadata.checkout.Identity.Path, metadata.checkout.HeadOID)
	if err != nil {
		return nil
	}
	var names []string
	for name := range tree {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		if filepath.Base(name) != "convex.json" || strings.Contains(name, "node_modules/") {
			continue
		}
		packagePath := filepath.Join(filepath.Dir(name), "package.json")
		entry, ok := tree[packagePath]
		if !ok {
			continue
		}
		data, blobErr := metadata.client.Blob(ctx, metadata.checkout.Identity.Path, entry, 1<<20)
		if blobErr != nil {
			return out
		}
		var metadataJSON struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &metadataJSON) != nil || (metadataJSON.Dependencies["convex"] == "" && metadataJSON.DevDependencies["convex"] == "") {
			continue
		}
		out = append(out, filepath.Dir(name))
		if len(out) >= 20 {
			break
		}
	}
	return out
}

func serviceIDCompletion(command *cobra.Command) {
	_ = command.RegisterFlagCompletionFunc("site-url-service", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return noFileAdapter(func(ctx context.Context) ([]candidate, lookupStatus, error) {
			metadata, err := openCompletionMetadata(ctx)
			if err != nil || metadata.checkout == nil {
				return nil, lookupUnavailable, nil
			}
			defer metadata.close()
			manifest := committedManifest(ctx, metadata)
			if manifest == nil {
				return nil, lookupUnavailable, nil
			}
			endpointServices := map[string]bool{}
			for _, endpoint := range manifest.Endpoints() {
				endpointServices[endpoint.Service] = true
			}
			var out []candidate
			for serviceID := range endpointServices {
				if hasCompletionPrefix(serviceID, toComplete) {
					out = append(out, candidate{Insert: serviceID, Description: "declared service with an allocated endpoint", Kind: "service"})
				}
			}
			sortCandidates(out)
			return out, lookupAvailable, ctx.Err()
		}, "Use a declared service ID; onboarding discovery is not run by Tab")(cmd, args, toComplete)
	})
}
