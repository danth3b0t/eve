// Package cli implements command/output boundaries without launching projects.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/envfile"
	"eve/internal/git"
	"eve/internal/lifecycle"
	"eve/internal/platform"
	"eve/internal/state"
	"github.com/google/uuid"
)

const Version = "0.1.0"

type output struct {
	SchemaVersion   int                             `json:"schema_version"`
	Command         string                          `json:"command"`
	OK              bool                            `json:"ok"`
	Version         string                          `json:"version,omitempty"`
	Workspace       *workspace                      `json:"workspace,omitempty"`
	Services        map[string]service              `json:"services,omitempty"`
	Resources       map[string]resource             `json:"resources,omitempty"`
	Verification    *verification                   `json:"verification,omitempty"`
	Timings         map[string]int64                `json:"timings,omitempty"`
	RestartRequired bool                            `json:"restart_required,omitempty"`
	Existing        bool                            `json:"existing,omitempty"`
	Plan            *lifecycle.PlanPreview          `json:"plan,omitempty"`
	KeysPreview     *lifecycle.InterpolationPreview `json:"keys_preview,omitempty"`
	Doctor          *lifecycle.DoctorResult         `json:"doctor,omitempty"`
	Init            *initPreview                    `json:"init,omitempty"`
	Remote          []lifecycle.DoctorCheck         `json:"remote,omitempty"`
	Repositories    []repositoryGroup               `json:"repositories,omitempty"`
	GC              []gcCandidate                   `json:"gc_candidates,omitempty"`
	Auth            map[string]string               `json:"auth,omitempty"`
	Warnings        []string                        `json:"warnings,omitempty"`
	Error           *commandError                   `json:"error,omitempty"`
	Human           string                          `json:"-"`
}
type workspace struct {
	ID         string `json:"id"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	State      string `json:"state"`
	Phase      string `json:"phase"`
	Generation int    `json:"generation"`
}
type service struct {
	Port int    `json:"port"`
	URL  string `json:"url"`
}
type resource struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	URL       string `json:"url,omitempty"`
	SiteURL   string `json:"site_url,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}
type repositoryGroup struct {
	ID         string            `json:"id"`
	Label      string            `json:"label"`
	Path       string            `json:"path"`
	Workspaces []listedWorkspace `json:"workspaces"`
}
type listedWorkspace struct {
	Workspace workspace           `json:"workspace"`
	Services  map[string]service  `json:"services,omitempty"`
	Resources map[string]resource `json:"resources,omitempty"`
}
type gcCandidate struct {
	Kind      string    `json:"kind"`
	Eligible  bool      `json:"eligible"`
	Reason    string    `json:"reason"`
	Workspace workspace `json:"workspace"`
	Applied   bool      `json:"applied,omitempty"`
}
type initPreview struct {
	Manifest         string        `json:"manifest"`
	Evidence         []string      `json:"evidence"`
	Warnings         []string      `json:"warnings,omitempty"`
	Services         []initService `json:"services"`
	HasConvexBackend bool          `json:"has_convex_backend"`
	Written          bool          `json:"written"`
	Updated          bool          `json:"updated"`
}
type initService struct {
	ID            string   `json:"id"`
	Path          string   `json:"path"`
	Command       string   `json:"command"`
	Config        string   `json:"config"`
	Port          string   `json:"port,omitempty"`
	PublicURL     bool     `json:"public_url"`
	PublicSiteURL bool     `json:"public_site_url"`
	Bindings      []string `json:"bindings,omitempty"`
}
type verification struct {
	Configuration string `json:"configuration"`
	Runtime       string `json:"runtime"`
	Code          string `json:"code"`
	Data          string `json:"data"`
}
type commandError struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details"`
	NextAction string         `json:"next_action"`
}

// Run is return-code oriented. Mutation requires the explicit flags shown in
// usage; EVE never launches applications or edits scripts from this command.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	jsonMode := len(args) > 0 && wantsJSON(args[1:])
	if len(args) == 1 && args[0] == "--version" {
		args = []string{"version"}
	}
	result, err := run(ctx, args)
	if err != nil {
		code := exitCode(err)
		response := errorResult(commandName(args), err)
		if jsonMode {
			_ = json.NewEncoder(stdout).Encode(response)
		} else {
			_, _ = fmt.Fprintf(stderr, "error: %s: %s\n", response.Error.Code, response.Error.Message)
			if response.Error.NextAction != "" {
				_, _ = fmt.Fprintf(stderr, "next: %s\n", response.Error.NextAction)
			}
			if response.Error.Code == "E_USAGE" {
				_, _ = fmt.Fprintln(stderr, "usage: eve <create|plan|keys|path|status|doctor|sync|resume|destroy|gc|list|auth|init> [command options] [workspace]")
			}
		}
		return code
	}
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(result)
	} else {
		_, _ = fmt.Fprint(stdout, result.Human)
	}
	if !result.OK {
		return 3
	}
	return 0
}
func run(ctx context.Context, args []string) (*output, error) {
	if len(args) == 0 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "a command is required"}
	}
	switch args[0] {
	case "version":
		return &output{SchemaVersion: 1, Command: "version", OK: true, Version: Version, Human: "eve " + Version + "\n"}, nil
	case "auth":
		return auth(ctx, args[1:])
	case "init":
		return initialize(ctx, args[1:])
	case "create":
		return create(ctx, args[1:])
	case "plan":
		return plan(ctx, args[1:])
	case "keys":
		return keys(ctx, args[1:])
	case "path":
		return inspect(ctx, args[1:], true)
	case "status":
		return inspect(ctx, args[1:], false)
	case "resume":
		return resume(ctx, args[1:])
	case "sync":
		return sync(ctx, args[1:])
	case "list":
		return list(ctx, args[1:])
	case "doctor":
		return doctor(ctx, args[1:])
	case "gc":
		return gc(ctx, args[1:])
	case "destroy":
		return destroy(ctx, args[1:])
	default:
		return nil, &domain.Error{Code: "E_USAGE", Message: "unknown command"}
	}
}
func newFlags(command string) (*flag.FlagSet, *bool) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, fs.Bool("json", false, "emit the versioned JSON result")
}
func wantsJSON(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

// parseCommandFlags supports the documented grammar in both orders: an
// operation selector before command flags, or flags before the selector.
func parseCommandFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) != 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return positional, nil
}
func commandName(args []string) string {
	if len(args) == 0 {
		return "eve"
	}
	return args[0]
}

func storeFor(ctx context.Context, writable bool) (*git.Client, *state.Store, error) {
	g, err := git.New()
	if err != nil {
		return nil, nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, &domain.Error{Code: "E_STATE_PATH", Message: "current directory is inaccessible"}
	}
	root, err := platform.DefaultPaths()
	if err != nil {
		return nil, nil, err
	}
	var s *state.Store
	if writable {
		s, err = lifecycle.OpenForGit(ctx, g, cwd, root.State)
	} else {
		s, err = lifecycle.OpenReadOnlyForGit(ctx, g, cwd, root.State)
	}
	if err != nil {
		return nil, nil, err
	}
	return g, s, nil
}
func globalStore(ctx context.Context, writable bool) (*git.Client, *state.Store, error) {
	g, err := git.New()
	if err != nil {
		return nil, nil, err
	}
	root, err := platform.DefaultPaths()
	if err != nil {
		return nil, nil, err
	}
	var s *state.Store
	if writable {
		s, err = state.Open(ctx, root.State)
	} else {
		s, err = state.OpenReadOnly(ctx, root.State)
	}
	if err != nil {
		return nil, nil, err
	}
	return g, s, nil
}
func loadConfig() (config.UserConfig, error) {
	paths, err := platform.DefaultPaths()
	if err != nil {
		return config.UserConfig{}, err
	}
	data, err := os.ReadFile(paths.Config)
	if errors.Is(err, os.ErrNotExist) {
		return config.ParseUser([]byte("version = 1\n"))
	}
	if err != nil {
		return config.UserConfig{}, &domain.Error{Code: "E_CONFIG_READ", Message: "user configuration cannot be read safely", Path: paths.Config}
	}
	return config.ParseUser(data)
}

func create(ctx context.Context, args []string) (*output, error) {
	fs, jsonOut := newFlags("create")
	yes := fs.Bool("yes", false, "approve source registration, allocation and this workspace creation")
	from := fs.String("from", "", "existing commit/ref for a new branch")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid create options"}
	}
	if len(positional) != 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "create requires one branch"}
	}
	branch := positional[0]
	registered := false
	approved := *yes
	reviewed := lifecycle.PlanPreview{}
	if !approved && !*jsonOut {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, &domain.Error{Code: "E_STATE_PATH", Message: "current directory is inaccessible"}
		}
		// Existing state owns this command's identity before a fresh plan is shown.
		// Read-only state may be unavailable while the source is not registrable.
		g, s, err := storeFor(ctx, false)
		if err == nil {
			if existing, existingErr := resolveWorkspace(ctx, g, s, branch); existingErr == nil {
				if existing.State != "prepared" {
					closeErr := s.Close()
					if closeErr != nil {
						return nil, closeErr
					}
					return nil, &domain.Error{Code: "E_RESUME_REQUIRED", Message: "an active or failed operation already owns that branch; resume it rather than replacing identity", Path: existing.ID}
				}
				response, outErr := existingCreate(ctx, s, existing, registered)
				closeErr := s.Close()
				if outErr != nil {
					return nil, outErr
				}
				if closeErr != nil {
					return nil, closeErr
				}
				return response, nil
			}
			if closeErr := s.Close(); closeErr != nil {
				return nil, closeErr
			}
		} else {
			if codeOf(err) != "E_STATE_PATH" {
				return nil, err
			}
			if g, err = git.New(); err != nil {
				return nil, err
			}
		}
		preview, err := lifecycle.PlanPreviewForBranch(ctx, g, cwd, branch, *from)
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(os.Stderr, "Create workspace for %s\nsource: %s\ntarget commit: %s\nmanifest destinations: %d\nNew worktree and provider effects require typed approval.\n", quote(preview.Branch), preview.Source, preview.HeadOID, len(preview.Files.Files))
		confirmed, err := interactiveYes(ctx, "Create this workspace?")
		if err != nil {
			return nil, err
		}
		if !confirmed {
			return nil, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "create was not approved; no state or workspace was changed"}
		}
		reviewed = preview
		approved = true
	}
	g, s, err := storeFor(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return nil, &domain.Error{Code: "E_STATE_PATH", Message: "current directory is inaccessible"}
	}
	if existing, existingErr := resolveWorkspace(ctx, g, s, branch); existingErr == nil {
		if existing.State == "prepared" {
			if *from != "" {
				return nil, &domain.Error{Code: "E_CREATE_EXISTS", Message: "workspace already exists; --from cannot retarget it"}
			}
			return existingCreate(ctx, s, existing, registered)
		}
		return nil, &domain.Error{Code: "E_RESUME_REQUIRED", Message: "an active or failed operation already owns that branch; resume it rather than replacing identity", Path: existing.ID}
	}
	planStart := time.Now()
	plan, planErr := lifecycle.PlanGit(ctx, s, g, cwd, branch, *from)
	if codeOf(planErr) == "E_SOURCE_UNREGISTERED" {
		if !approved {
			return nil, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "first creation would register this canonical source checkout; rerun with --yes after review"}
		}
		if _, err := lifecycle.RegisterSource(ctx, s, g, cwd); err != nil {
			return nil, err
		}
		registered = true
		plan, planErr = lifecycle.PlanGit(ctx, s, g, cwd, branch, *from)
	}
	if planErr != nil {
		return nil, planErr
	}
	if !approved {
		return nil, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "create would allocate and prepare a workspace; rerun with --yes after review"}
	}
	if !*yes {
		manifestDigest := sha256.Sum256(plan.Target.Manifest)
		if reviewed.Branch != plan.Target.Branch || reviewed.HeadOID != plan.Target.HeadOID || reviewed.ManifestSHA256 != fmt.Sprintf("%x", manifestDigest) {
			return nil, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "target changed after interactive approval; rerun create and review the updated plan"}
		}
	}
	user, err := loadConfig()
	if err != nil {
		return nil, err
	}
	created, err := lifecycle.CreateLocal(ctx, s, g, plan, user)
	if err != nil {
		return nil, err
	}
	response := success("create", created.Workspace)
	response.setServices(created.Allocation)
	resourceRows, err := s.Resources(ctx, created.Workspace.ID)
	if err != nil {
		return nil, err
	}
	response.setResources(resourceRows)
	data := "no_workspace_data_by_eve"
	if len(resourceRows) != 0 {
		data = "provider_empty_initial_state"
	}
	response.Verification = &verification{Configuration: "verified", Runtime: "not_checked", Code: "not_verified_by_eve", Data: data}
	response.Timings = map[string]int64{
		"plan_ms":         time.Since(planStart).Milliseconds(),
		"intent_ms":       created.Timings.IntentMS,
		"reservation_ms":  created.Timings.ReservationMS,
		"git_ms":          created.Timings.GitMS,
		"provider_ms":     created.Timings.ProviderMS,
		"file_staging_ms": created.Timings.FileStagingMS,
		"publication_ms":  created.Timings.PublicationMS,
		"create_total_ms": created.Timings.CreateTotalMS,
	}
	if registered {
		response.Warnings = append(response.Warnings, "Registered this canonical source checkout after explicit approval.")
	}
	response.Human = fmt.Sprintf("created %s\npath: %s\nUse the project's existing setup/start procedure; stop it before sync or destroy.\n", strconv.Quote(branch), created.Workspace.Path)
	return response, nil
}
func plan(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("plan")
	from := fs.String("from", "", "existing commit/ref for a new branch")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid plan options"}
	}
	if len(positional) != 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "plan requires one branch"}
	}
	g, err := git.New()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, &domain.Error{Code: "E_STATE_PATH", Message: "current directory is inaccessible"}
	}
	preview, err := lifecycle.PlanPreviewForBranch(ctx, g, cwd, positional[0], *from)
	if err != nil {
		return nil, err
	}
	response := &output{SchemaVersion: 1, Command: "plan", OK: true, Plan: &preview}
	human := fmt.Sprintf("plan for %s\nsource: %s\ntarget: %s\n", quote(preview.Branch), preview.Source, preview.HeadOID)
	if len(preview.Endpoints) != 0 {
		human += fmt.Sprintf("prospective endpoints: %d\n", len(preview.Endpoints))
	}
	if len(preview.Files.Files) != 0 {
		human += fmt.Sprintf("native/copy destinations: %d\n", len(preview.Files.Files))
	}
	human += "no state, workspaces, ports or provider resources created\n"
	response.Human = human
	return response, nil
}
func keys(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("keys")
	positional, err := parseCommandFlags(fs, args)
	if err != nil || len(positional) != 0 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "keys lists variables for the current committed eve.toml only"}
	}
	g, err := git.New()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, &domain.Error{Code: "E_STATE_PATH", Message: "current directory is inaccessible"}
	}
	preview, err := lifecycle.InterpolationPreviewForCheckout(ctx, g, cwd)
	if err != nil {
		return nil, err
	}
	response := &output{SchemaVersion: 1, Command: "keys", OK: true, KeysPreview: &preview}
	human := fmt.Sprintf("interpolation variables for committed eve.toml\nsource: %s\ntarget: %s\n", preview.Source, preview.HeadOID)
	for _, variable := range preview.Keys {
		human += fmt.Sprintf("%-42s %-15s %s\n", variable.Variable, "["+variable.Source+"]", variable.Description)
	}
	human += "manager/provider values are not shown; these names always resolve from creation output, not free text\n"
	response.Human = human
	return response, nil
}
func initialize(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("init")
	dry := fs.Bool("dry-run", false, "show the proposal without mutation")
	write := fs.Bool("write", false, "create eve.toml after review flags")
	yes := fs.Bool("yes", false, "approve creating this exact manifest")
	project := fs.String("project", "", "explicit team:project binding when Convex is discovered")
	convexMode := fs.Bool("convex", false, "initialize or update a Convex resource workflow")
	backendPath := fs.String("backend-path", "", "explicit discovered Convex package path when candidates are ambiguous")
	profile := fs.String("profile", "", "Convex credential profile to bind once")
	siteURLService := fs.String("site-url-service", "", "discovered service whose allocated URL should be the backend SITE_URL override")
	update := fs.Bool("update", false, "review and update an existing committed eve.toml")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid init options"}
	}
	if len(positional) != 0 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "init runs inside the source checkout"}
	}
	if *profile != "" && !profileName.MatchString(*profile) {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid credential profile name"}
	}
	if (*backendPath != "" || *siteURLService != "") && !*convexMode {
		convexValue := true
		convexMode = &convexValue
	}
	g, err := git.New()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, &domain.Error{Code: "E_STATE_PATH", Message: "current checkout is inaccessible"}
	}
	checkout, err := g.Inspect(ctx, cwd)
	if err != nil {
		return nil, err
	}
	result, err := lifecycle.ProposeInit(ctx, g, checkout.Identity.Path, lifecycle.InitOptions{Project: *project, CredentialProfile: *profile, Convex: *convexMode, BackendPath: *backendPath, SiteURLService: *siteURLService, Update: *update})
	if err != nil {
		return nil, err
	}
	if result.HasConvexBackend {
		credentialName := *profile
		if credentialName == "" {
			credentialName = "default"
		}
		result.Warnings = append(result.Warnings, "Convex credential profile "+strconv.Quote(credentialName)+" is not stored or validated by init; log in once before create when it is not already configured.")
	}
	preview := &initPreview{Manifest: result.ManifestText(), Evidence: result.Evidence, Warnings: result.Warnings, HasConvexBackend: result.HasConvexBackend}
	for _, service := range result.Services {
		row := initService{ID: service.ID, Path: service.Path, Command: service.Dev, Config: service.ViteConfig, Port: service.Port, PublicURL: service.PublicURL, PublicSiteURL: service.PublicSiteURL}
		for _, binding := range service.Bindings {
			row.Bindings = append(row.Bindings, binding.Key+"->resources.backend."+binding.Output)
		}
		preview.Services = append(preview.Services, row)
	}
	written := false
	if *write && !*dry {
		if !*yes {
			return nil, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "review the proposed manifest and rerun init --write --yes"}
		}
		if _, err := config.Parse(result.Manifest); err != nil {
			return nil, err
		}
		if *update {
			if err := updateInitManifest(checkout.Identity.Path, result.Manifest); err != nil {
				return nil, err
			}
			preview.Updated = true
		} else if err := writeInitManifest(checkout.Identity.Path, result.Manifest); err != nil {
			return nil, err
		}
		written = true
	}
	preview.Written = written
	response := &output{SchemaVersion: 1, Command: "init", OK: true, Init: preview}
	human := result.ManifestText() + "\n"
	if !written && !*dry {
		human += "dry-run: no eve.toml written\n"
	}
	if written {
		human += "wrote eve.toml (0600); review/commit it before create\n"
		if preview.Updated {
			human += "updated eve.toml (0600); review/commit it before create\n"
		}
	}
	response.Human = human
	return response, nil
}
func writeInitManifest(root string, data []byte) error {
	owner, err := os.OpenRoot(root)
	if err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot open source checkout safely", Path: root}
	}
	defer owner.Close()
	if _, err := owner.Stat("eve.toml"); err == nil {
		return &domain.Error{Code: "E_INIT_EXISTS", Message: "eve.toml already exists", Path: root}
	}
	file, err := owner.OpenFile("eve.toml", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot initialize eve.toml without overwriting", Path: root}
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot write eve.toml", Path: root}
	}
	if err := file.Sync(); err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot sync eve.toml", Path: root}
	}
	if err := file.Close(); err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot close eve.toml", Path: root}
	}
	return platform.SyncDirectory(root)
}
func updateInitManifest(root string, data []byte) error {
	owner, err := os.OpenRoot(root)
	if err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot open source checkout safely", Path: root}
	}
	defer owner.Close()
	if _, err := owner.Stat("eve.toml"); err != nil {
		return &domain.Error{Code: "E_INIT_UPDATE", Message: "eve.toml must exist for the reviewed update", Path: root}
	}
	temporary := ".eve-init-" + uuid.NewString() + ".tmp"
	file, err := owner.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot reserve reviewed manifest update safely", Path: temporary}
	}
	completed := false
	defer func() {
		if !completed {
			file.Close()
			_ = owner.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot write reviewed manifest update", Path: temporary}
	}
	if err := file.Sync(); err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot sync reviewed manifest update", Path: temporary}
	}
	if err := file.Close(); err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot close reviewed manifest update", Path: temporary}
	}
	if err := owner.Rename(temporary, "eve.toml"); err != nil {
		return &domain.Error{Code: "E_FILE_IO", Message: "cannot publish reviewed manifest update", Path: "eve.toml"}
	}
	completed = true
	return platform.SyncDirectory(root)
}
func existingCreate(ctx context.Context, s *state.Store, workspaceInfo state.Workspace, registered bool) (*output, error) {
	allocation, err := s.Allocation(ctx, workspaceInfo.ID)
	if err != nil {
		return nil, err
	}
	resourceRows, err := s.Resources(ctx, workspaceInfo.ID)
	if err != nil {
		return nil, err
	}
	response := success("create", workspaceInfo)
	response.setServices(allocation)
	response.setResources(resourceRows)
	response.Existing = true
	response.Verification = &verification{Configuration: "verified", Runtime: "not_checked", Code: "not_verified_by_eve", Data: "existing_workspace"}
	if registered {
		response.Warnings = append(response.Warnings, "Registered this canonical source checkout after explicit approval.")
	}
	response.Human = fmt.Sprintf("already prepared %s\npath: %s\n", quote(workspaceInfo.Branch), workspaceInfo.Path)
	return response, nil
}
func resolveWorkspace(ctx context.Context, g *git.Client, s *state.Store, selector string) (state.Workspace, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return state.Workspace{}, &domain.Error{Code: "E_STATE_PATH", Message: "current directory is inaccessible"}
	}
	checkout, err := g.Inspect(ctx, cwd)
	if err != nil {
		return state.Workspace{}, err
	}
	repository, err := s.RepositoryForCommon(ctx, checkout.Identity.CommonDir, checkout.Identity.CommonIdentity)
	if err != nil {
		return state.Workspace{}, err
	}
	if selector == "" {
		return s.WorkspaceByPath(ctx, repository.ID, checkout.Identity.Path)
	}
	if filepath.IsAbs(selector) {
		return s.WorkspaceByPath(ctx, repository.ID, selector)
	}
	if id, parseErr := uuid.Parse(selector); parseErr == nil && id.String() == selector {
		if workspace, err := s.Workspace(ctx, selector); err == nil {
			return workspace, nil
		}
	}
	return s.WorkspaceByBranch(ctx, repository.ID, selector)
}
func inspect(ctx context.Context, args []string, pathOnly bool) (*output, error) {
	command := "status"
	if pathOnly {
		command = "path"
	}
	fs, _ := newFlags(command)
	refresh := fs.Bool("refresh", false, "add read-only provider identity checks")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid " + command + " options"}
	}
	if *refresh && pathOnly {
		return nil, &domain.Error{Code: "E_USAGE", Message: "--refresh is valid only for status"}
	}
	if len(positional) > 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: command + " accepts at most one workspace"}
	}
	selector := ""
	if len(positional) == 1 {
		selector = positional[0]
	}
	g, s, err := storeFor(ctx, *refresh)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workspaceInfo, err := resolveWorkspace(ctx, g, s, selector)
	if err != nil {
		return nil, err
	}
	if workspaceInfo.State == "destroyed" {
		return nil, &domain.Error{Code: "E_WORKSPACE_NOT_FOUND", Message: "workspace was destroyed"}
	}
	response := success(command, workspaceInfo)
	if pathOnly {
		response.Human = workspaceInfo.Path + "\n"
		return response, nil
	}
	allocation, err := s.Allocation(ctx, workspaceInfo.ID)
	if err != nil {
		return nil, err
	}
	response.setServices(allocation)
	resourceRows, err := s.Resources(ctx, workspaceInfo.ID)
	if err != nil {
		return nil, err
	}
	response.setResources(resourceRows)
	configuration := "incomplete"
	if workspaceInfo.State == "prepared" && workspaceInfo.Generation >= 1 {
		configuration = "verified"
	}
	data := "no_workspace_data_by_eve"
	if len(resourceRows) != 0 {
		data = "provider_empty_initial_state"
	}
	if *refresh {
		lock, err := s.LockWorkspace(workspaceInfo.ID)
		if err != nil {
			return nil, err
		}
		remoteChecks, remoteErr := lifecycle.DoctorRemote(ctx, s, lock, workspaceInfo, nil)
		closeErr := lock.Close()
		if closeErr != nil && remoteErr == nil {
			remoteErr = closeErr
		}
		if remoteErr != nil {
			return nil, remoteErr
		}
		response.Remote = remoteChecks
	}
	response.Verification = &verification{Configuration: configuration, Runtime: "not_checked", Code: "not_verified_by_eve", Data: data}
	response.Human = fmt.Sprintf("workspace %s\nbranch: %s\nstate: %s phase=%s generation=%d\npath: %s\n%s%s%s", workspaceInfo.ID, quote(workspaceInfo.Branch), workspaceInfo.State, workspaceInfo.Phase, workspaceInfo.Generation, workspaceInfo.Path, servicesHuman(allocation), resourcesHuman(resourceRows), remoteHuman(response.Remote))
	return response, nil
}
func list(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("list")
	all := fs.Bool("all", false, "list every registered repository")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid list options"}
	}
	if len(positional) != 0 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "list does not accept a workspace selector"}
	}
	var g *git.Client
	var s *state.Store
	var repos []state.Repository
	if *all {
		g, s, err = globalStore(ctx, false)
	} else {
		g, s, err = storeFor(ctx, false)
	}
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if *all {
		repos, err = s.Repositories(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, &domain.Error{Code: "E_STATE_PATH", Message: "current directory is inaccessible"}
		}
		checkout, err := g.Inspect(ctx, cwd)
		if err != nil {
			return nil, err
		}
		current, err := s.RepositoryForCommon(ctx, checkout.Identity.CommonDir, checkout.Identity.CommonIdentity)
		if err != nil {
			return nil, err
		}
		repos = []state.Repository{current}
	}
	response := &output{SchemaVersion: 1, Command: "list", OK: true}
	var human strings.Builder
	for _, repository := range repos {
		group := repositoryGroup{ID: repository.ID, Label: repository.Label, Path: repository.SourcePath, Workspaces: []listedWorkspace{}}
		workspaces, err := s.ListWorkspaces(ctx, repository.ID, false)
		if err != nil {
			return nil, err
		}
		human.WriteString(quote(repository.Label) + "\n")
		for _, workspaceInfo := range workspaces {
			entry := listedWorkspace{Workspace: workspace{ID: workspaceInfo.ID, Branch: workspaceInfo.Branch, Path: workspaceInfo.Path, State: workspaceInfo.State, Phase: workspaceInfo.Phase, Generation: workspaceInfo.Generation}}
			if allocation, err := s.Allocation(ctx, workspaceInfo.ID); err != nil {
				return nil, err
			} else {
				entry.Services = servicesMap(allocation)
			}
			if resources, err := s.Resources(ctx, workspaceInfo.ID); err != nil {
				return nil, err
			} else {
				entry.Resources = resourcesMap(resources)
			}
			group.Workspaces = append(group.Workspaces, entry)
			fmt.Fprintf(&human, "  %s %s gen=%d %s\n", quote(workspaceInfo.Branch), workspaceInfo.State, workspaceInfo.Generation, workspaceInfo.Path)
		}
		if len(workspaces) == 0 {
			human.WriteString("  no live workspaces\n")
		}
		response.Repositories = append(response.Repositories, group)
	}
	response.Human = human.String()
	return response, nil
}
func gc(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("gc")
	apply := fs.Bool("apply", false, "retry the exact eligible cleanup operations shown")
	positional, parseErr := parseCommandFlags(fs, args)
	if parseErr != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid gc options"}
	}
	if len(positional) != 0 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "gc does not accept a workspace selector"}
	}
	g, s, err := globalStore(ctx, *apply)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	repositories, err := s.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	response := &output{SchemaVersion: 1, Command: "gc", OK: true}
	var human strings.Builder
	now := time.Now().UnixMilli()
	for _, repo := range repositories {
		workspaces, err := s.ListWorkspaces(ctx, repo.ID, false)
		if err != nil {
			return nil, err
		}
		for _, w := range workspaces {
			candidate, include, err := gcCandidateFor(ctx, s, w, now)
			if err != nil {
				return nil, err
			}
			if !include {
				continue
			}
			if *apply && candidate.Eligible {
				result, err := lifecycle.ApplyGC(ctx, s, g, w.ID, lifecycle.GCOptions{})
				if err != nil {
					return nil, err
				}
				candidate.Applied = true
				candidate.Workspace = workspace{ID: result.Workspace.ID, Branch: result.Workspace.Branch, Path: result.Workspace.Path, State: result.Workspace.State, Phase: result.Workspace.Phase, Generation: result.Workspace.Generation}
			}
			response.GC = append(response.GC, candidate)
			fmt.Fprintf(&human, "%s %s eligible=%t path=%s\n", candidate.Kind, candidate.Reason, candidate.Eligible, quote(candidate.Workspace.Path))
		}
	}
	if len(response.GC) == 0 {
		human.WriteString("no recorded cleanup candidates\n")
	}
	response.Human = human.String()
	return response, nil
}
func gcCandidateFor(ctx context.Context, s *state.Store, w state.Workspace, now int64) (gcCandidate, bool, error) {
	candidate := gcCandidate{Workspace: workspace{ID: w.ID, Branch: w.Branch, Path: w.Path, State: w.State, Phase: w.Phase, Generation: w.Generation}}
	resources, err := s.Resources(ctx, w.ID)
	if err != nil {
		return candidate, false, err
	}
	for _, resource := range resources {
		if resource.ExpiresAtMS > 0 && resource.ExpiresAtMS <= now && resource.State != "deleted" {
			candidate.Kind = "expired_resource"
			candidate.Reason = "deployment expired; local worktree is preserved and no automatic deletion follows"
			return candidate, true, nil
		}
		if resource.State == "unknown" || resource.State == "cleanup_pending" || resource.State == "deleting" {
			candidate.Kind = "remote_cleanup_pending"
			candidate.Reason = "recorded remote operation must be reconciled, not replaced"
			if w.State == "destroying" || w.State == "cleanup_pending" {
				candidate.Eligible = true
			}
			return candidate, true, nil
		}
	}
	switch w.State {
	case "destroying", "cleanup_pending":
		candidate.Kind = "cleanup_pending"
		candidate.Reason = "exact local/remote cleanup can be retried"
		candidate.Eligible = true
		return candidate, true, nil
	case "creating", "failed":
		candidate.Kind = "unfinished_create"
		candidate.Reason = "resume the operation or review its durable intent"
		return candidate, true, nil
	case "prepared":
		if _, err := os.Lstat(w.Path); err == nil {
			return candidate, false, nil
		} else if errors.Is(err, os.ErrNotExist) {
			candidate.Kind = "orphaned_worktree"
			candidate.Reason = "recorded workspace path is absent; exact remote/local cleanup can be completed"
			candidate.Eligible = true
			return candidate, true, nil
		}
		candidate.Kind = "unverifiable_workspace"
		candidate.Reason = "path is inaccessible; absence is not proven"
		return candidate, true, nil
	}
	return candidate, false, nil
}
func sync(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("sync")
	overwrite := fs.Bool("overwrite-managed", false, "replace an externally edited EVE-managed value")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid sync options"}
	}
	if len(positional) > 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "sync accepts at most one workspace"}
	}
	selector := ""
	if len(positional) == 1 {
		selector = positional[0]
	}
	g, s, err := storeFor(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workspaceInfo, err := resolveWorkspace(ctx, g, s, selector)
	if err != nil {
		return nil, err
	}
	result, err := lifecycle.SyncWorkspace(ctx, s, g, workspaceInfo.ID, lifecycle.SyncOptions{OverwriteManaged: *overwrite})
	if err != nil {
		return nil, err
	}
	response := success("sync", result.Workspace)
	response.RestartRequired = result.RestartRequired
	allocation, err := s.Allocation(ctx, result.Workspace.ID)
	if err != nil {
		return nil, err
	}
	response.setServices(allocation)
	resources, err := s.Resources(ctx, result.Workspace.ID)
	if err != nil {
		return nil, err
	}
	response.setResources(resources)
	if result.RestartRequired {
		response.Human = fmt.Sprintf("synced generation %d for workspace %s\nrestart the project with its ordinary command\n", result.Workspace.Generation, result.Workspace.ID)
	} else {
		response.Human = fmt.Sprintf("workspace %s already matches generation %d\n", result.Workspace.ID, result.Workspace.Generation)
	}
	return response, nil
}
func doctor(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("doctor")
	remote := fs.Bool("remote", false, "add read-only provider identity checks")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid doctor options"}
	}
	if len(positional) > 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "doctor accepts at most one workspace"}
	}
	selector := ""
	if len(positional) == 1 {
		selector = positional[0]
	}
	g, s, err := storeFor(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workspaceInfo, err := resolveWorkspace(ctx, g, s, selector)
	if err != nil {
		return nil, err
	}
	result, err := lifecycle.DoctorWorkspace(ctx, s, g, workspaceInfo.ID)
	if err != nil {
		return nil, err
	}
	if *remote {
		lock, err := s.LockWorkspace(workspaceInfo.ID)
		if err != nil {
			return nil, err
		}
		remoteChecks, remoteErr := lifecycle.DoctorRemote(ctx, s, lock, result.Workspace, nil)
		closeErr := lock.Close()
		if closeErr != nil && remoteErr == nil {
			remoteErr = closeErr
		}
		if remoteErr != nil {
			return nil, remoteErr
		}
		kept := result.Checks[:0]
		for _, check := range result.Checks {
			if check.ID != "provider_remote" {
				kept = append(kept, check)
			}
		}
		result.Checks = append(kept, remoteChecks...)
	}
	failed := false
	for _, check := range result.Checks {
		if check.Status == "fail" {
			failed = true
		}
	}
	response := success("doctor", result.Workspace)
	response.Doctor = &result
	response.OK = !failed
	response.Human = doctorHuman(result)
	return response, nil
}
func doctorHuman(result lifecycle.DoctorResult) string {
	var human strings.Builder
	fmt.Fprintf(&human, "doctor for workspace %s\n", result.Workspace.ID)
	for _, check := range result.Checks {
		fmt.Fprintf(&human, "  %-8s %s: %s\n", check.Status, check.ID, check.Evidence)
	}
	return human.String()
}
func resume(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("resume")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid resume options"}
	}
	if len(positional) > 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "resume accepts at most one workspace"}
	}
	selector := ""
	if len(positional) == 1 {
		selector = positional[0]
	}
	g, s, err := storeFor(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workspaceInfo, err := resolveWorkspace(ctx, g, s, selector)
	if err != nil {
		return nil, err
	}
	resumed, err := lifecycle.ResumeWorkspace(ctx, s, g, workspaceInfo.ID, lifecycle.ResumeOptions{})
	if err != nil {
		return nil, err
	}
	response := success("resume", resumed)
	allocation, err := s.Allocation(ctx, resumed.ID)
	if err != nil {
		return nil, err
	}
	response.setServices(allocation)
	resourceRows, err := s.Resources(ctx, resumed.ID)
	if err != nil {
		return nil, err
	}
	response.setResources(resourceRows)
	if resumed.State == "prepared" {
		response.Human = fmt.Sprintf("resumed workspace %s\npath: %s\n", resumed.ID, resumed.Path)
	} else {
		response.Human = fmt.Sprintf("resumed %s for workspace %s\n", resumed.State, resumed.ID)
	}
	return response, nil
}
func destroy(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("destroy")
	yes := fs.Bool("yes", false, "approve removal of the exact workspace")
	discard := fs.Bool("discard-changes", false, "discard reviewed user work in the worktree")
	assume := fs.Bool("assume-stopped", false, "assert the ordinary project launcher has been stopped/assessed")
	positional, err := parseCommandFlags(fs, args)
	if err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid destroy options"}
	}
	if len(positional) > 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "destroy accepts at most one workspace"}
	}
	selector := ""
	if len(positional) == 1 {
		selector = positional[0]
	}
	g, s, err := storeFor(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workspaceInfo, err := resolveWorkspace(ctx, g, s, selector)
	if err != nil {
		return nil, err
	}
	if !*yes {
		return nil, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "destroy removes the exact worktree and any ignored local files; rerun with --yes after assessing them"}
	}
	lock, err := s.LockWorkspace(workspaceInfo.ID)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	result, err := lifecycle.DestroyLocal(ctx, s, g, lock, lifecycle.DestroyOptions{Approved: true, DiscardChanges: *discard, AssumeStopped: *assume})
	if err != nil {
		return nil, err
	}
	response := success("destroy", result.Workspace)
	if result.Warning != "" {
		response.Warnings = []string{result.Warning}
	}
	if result.Workspace.State == "cleanup_pending" {
		response.Human = "cleanup pending: local worktree is gone, but a claimed port still has a listener; assess it and rerun destroy.\n"
	} else {
		response.Human = fmt.Sprintf("destroyed %s\nbranch retained: %s\n", workspaceInfo.ID, quote(result.Workspace.Branch))
	}
	return response, nil
}

func success(command string, w state.Workspace) *output {
	return (&output{SchemaVersion: 1, Command: command, OK: true}).setWorkspace(w)
}
func (o *output) setWorkspace(w state.Workspace) *output {
	o.Workspace = &workspace{w.ID, w.Branch, w.Path, w.State, w.Phase, w.Generation}
	return o
}
func servicesMap(a state.Allocation) map[string]service {
	out := map[string]service{}
	for _, endpoint := range a.Endpoints {
		name := endpoint.Service
		if endpoint.Name != "primary" {
			name += "/" + endpoint.Name
		}
		out[name] = service{Port: endpoint.Port, URL: endpoint.Scheme + "://" + net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))}
	}
	return out
}
func resourcesMap(rows []state.Resource) map[string]resource {
	out := map[string]resource{}
	for _, row := range rows {
		out[row.ResourceKey] = resource{Provider: row.Provider, Name: row.RemoteName, URL: row.Outputs["cloud_url"], SiteURL: row.Outputs["site_url"], ExpiresAt: row.Outputs["expires_at"]}
	}
	return out
}
func (o *output) setServices(a state.Allocation) {
	o.Services = map[string]service{}
	for _, endpoint := range a.Endpoints {
		name := endpoint.Service
		if endpoint.Name != "primary" {
			name += "/" + endpoint.Name
		}
		o.Services[name] = service{Port: endpoint.Port, URL: endpoint.Scheme + "://" + net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))}
	}
}
func (o *output) setResources(rows []state.Resource) {
	if len(rows) == 0 {
		return
	}
	o.Resources = map[string]resource{}
	for _, row := range rows {
		o.Resources[row.ResourceKey] = resource{Provider: row.Provider, Name: row.RemoteName, URL: row.Outputs["cloud_url"], SiteURL: row.Outputs["site_url"], ExpiresAt: row.Outputs["expires_at"]}
	}
}
func resourcesHuman(rows []state.Resource) string {
	if len(rows) == 0 {
		return ""
	}
	lines := []string{"resources:\n"}
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("  %s: %s %s expires=%s\n", row.ResourceKey, row.Provider, row.RemoteName, row.Outputs["expires_at"]))
	}
	return strings.Join(lines, "")
}

func remoteHuman(checks []lifecycle.DoctorCheck) string {
	if len(checks) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("provider remote:\n")
	for _, check := range checks {
		fmt.Fprintf(&out, "  %-8s %s: %s\n", check.Status, check.ID, check.Evidence)
	}
	return out.String()
}
func servicesHuman(a state.Allocation) string {
	if len(a.Endpoints) == 0 {
		return "endpoints: none\n"
	}
	var lines []string
	for _, ep := range a.Endpoints {
		lines = append(lines, fmt.Sprintf("%s: %s\n", ep.Service+"/"+ep.Name, ep.Scheme+"://"+net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port))))
	}
	return "endpoints:\n" + strings.Join(lines, "")
}
func quote(value string) string { return strconv.Quote(value) }
func codeOf(err error) string {
	var d *domain.Error
	if errors.As(err, &d) {
		return d.Code
	}
	var envErr *envfile.Error
	if errors.As(err, &envErr) {
		return envErr.Code
	}
	return ""
}
func exitCode(err error) int {
	if errors.Is(err, context.Canceled) {
		return 130
	}
	switch codeOf(err) {
	case "E_PROVIDER_AUTH", "E_PROVIDER_IDENTITY", "E_CREDENTIAL_PROFILE", "E_PROVIDER_FORBIDDEN":
		return 4
	case "E_PROVIDER_RESOURCE", "E_PROVIDER_SERVER", "E_PROVIDER_TRANSPORT", "E_PROVIDER_AMBIGUOUS", "E_PROVIDER_CONTRACT", "E_PROVIDER_THROTTLED", "E_PROVIDER_CONFLICT":
		return 5
	case "E_PROVIDER_NOT_FOUND":
		return 7
	case "E_CLEANUP_PENDING", "E_GIT_RECONCILE", "E_PUBLICATION_RECONCILE":
		return 6
	case "E_POSSIBLY_RUNNING", "E_WORKTREE_DIRTY", "E_APPROVAL_REQUIRED", "E_WORKSPACE_BUSY", "E_WORKSPACE_NOT_FOUND", "E_TRACKED_CREDENTIAL_FILE", "E_MANAGED_VALUE_CHANGED", "E_CREATION_ONLY_CHANGE", "E_SYNC_STATE", "E_PORT_OCCUPIED", "E_GIT_OWNERSHIP", "E_GIT_LOCKED", "E_GIT_REFERENCE_CHANGED", "E_SOURCE_UNREGISTERED", "E_CREATE_EXISTS", "E_RESUME_REQUIRED", "E_INIT_EXISTS", "E_INIT_DISCOVERY":
		return 3
	case "E_USAGE", "E_MANIFEST_INVALID", "E_ENV_SYNTAX", "E_ENV_SERIALIZATION", "E_ENV_KEY", "E_ENV_DUPLICATE", "E_ENV_VALUE", "E_ENV_SIZE", "E_PATH_ESCAPE", "E_CONFIG_INVALID":
		return 2
	default:
		return 1
	}
}
func errorResult(command string, err error) *output {
	code := codeOf(err)
	if code == "" {
		code = "E_INTERNAL"
	}
	message := "operation failed"
	var d *domain.Error
	if errors.As(err, &d) && d.Message != "" {
		message = d.Message
	}
	var envErr *envfile.Error
	if errors.As(err, &envErr) && envErr.Reason != "" {
		message = envErr.Reason
	}
	response := &output{SchemaVersion: 1, Command: command, OK: false, Error: &commandError{Code: code, Message: message, Details: map[string]any{}}}
	if errors.As(err, &d) {
		if d.Path != "" {
			response.Error.Details["path"] = d.Path
		}
		if d.Port != 0 {
			response.Error.Details["port"] = d.Port
		}
	}
	if errors.As(err, &envErr) {
		if envErr.Key != "" {
			response.Error.Details["key"] = envErr.Key
		}
		if len(envErr.Lines) != 0 {
			response.Error.Details["lines"] = envErr.Lines
		}
	}
	switch code {
	case "E_CLEANUP_PENDING":
		response.Error.NextAction = "stop or assess the listener, then rerun the same destroy command"
	case "E_APPROVAL_REQUIRED":
		response.Error.NextAction = "review the planned local effects and rerun with the listed safety flags"
	case "E_RESUME_REQUIRED":
		response.Error.NextAction = "run eve resume with the same branch or workspace ID"
	case "E_PROVIDER_NOT_FOUND":
		response.Error.NextAction = "run doctor --remote for this workspace, then rerun the exact recovery/destroy command"
	case "E_GIT_REFERENCE_CHANGED":
		response.Error.NextAction = "review the changed target branch, restore its recorded EVE intent or create a new tracked target"
	case "E_WORKTREE_DIRTY":
		response.Error.NextAction = "review changes or use --discard-changes to discard that work"
	case "E_POSSIBLY_RUNNING":
		response.Error.NextAction = "stop the ordinary project launcher first; --assume-stopped is an assessment assertion only"
	case "E_MANAGED_VALUE_CHANGED":
		response.Error.NextAction = "review the value, restore EVE's recorded setting or rerun sync with --overwrite-managed"
	}
	return response
}
