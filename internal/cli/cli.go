// Package cli implements command/output boundaries without launching projects.
package cli

import (
	"context"
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

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/git"
	"eve/internal/lifecycle"
	"eve/internal/platform"
	"eve/internal/state"
	"github.com/google/uuid"
)

type output struct {
	SchemaVersion int                 `json:"schema_version"`
	Command       string              `json:"command"`
	OK            bool                `json:"ok"`
	Workspace     *workspace          `json:"workspace,omitempty"`
	Services      map[string]service  `json:"services,omitempty"`
	Resources     map[string]resource `json:"resources,omitempty"`
	Verification  *verification       `json:"verification,omitempty"`
	Auth          map[string]string   `json:"auth,omitempty"`
	Warnings      []string            `json:"warnings,omitempty"`
	Error         *commandError       `json:"error,omitempty"`
	Human         string              `json:"-"`
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
				_, _ = fmt.Fprintln(stderr, "usage: eve <create|path|status|destroy> [command options]")
			}
		}
		return code
	}
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(result)
	} else {
		_, _ = fmt.Fprint(stdout, result.Human)
	}
	return 0
}
func run(ctx context.Context, args []string) (*output, error) {
	if len(args) == 0 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "a command is required"}
	}
	switch args[0] {
	case "auth":
		return auth(ctx, args[1:])
	case "create":
		return create(ctx, args[1:])
	case "path":
		return inspect(ctx, args[1:], true)
	case "status":
		return inspect(ctx, args[1:], false)
	case "resume":
		return resume(ctx, args[1:])
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
	fs, _ := newFlags("create")
	yes := fs.Bool("yes", false, "approve source registration, allocation and this workspace creation")
	from := fs.String("from", "", "existing commit/ref for a new branch")
	if err := fs.Parse(args); err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid create options"}
	}
	if fs.NArg() != 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "create requires one branch"}
	}
	branch := fs.Arg(0)
	g, s, err := storeFor(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return nil, &domain.Error{Code: "E_STATE_PATH", Message: "current directory is inaccessible"}
	}
	plan, planErr := lifecycle.PlanGit(ctx, s, g, cwd, branch, *from)
	registered := false
	if codeOf(planErr) == "E_SOURCE_UNREGISTERED" {
		if !*yes {
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
	if !*yes {
		return nil, &domain.Error{Code: "E_APPROVAL_REQUIRED", Message: "create would allocate and prepare a workspace; rerun with --yes after review"}
	}
	user, err := loadConfig()
	if err != nil {
		return nil, err
	}
	prepared, allocation, err := lifecycle.CreateLocal(ctx, s, g, plan, user)
	if err != nil {
		return nil, err
	}
	response := success("create", prepared)
	response.setServices(allocation)
	resourceRows, err := s.Resources(ctx, prepared.ID)
	if err != nil {
		return nil, err
	}
	response.setResources(resourceRows)
	data := "no_workspace_data_by_eve"
	if len(resourceRows) != 0 {
		data = "provider_empty_initial_state"
	}
	response.Verification = &verification{Configuration: "verified", Runtime: "not_checked", Code: "not_verified_by_eve", Data: data}
	if registered {
		response.Warnings = append(response.Warnings, "Registered this canonical source checkout after explicit approval.")
	}
	response.Human = fmt.Sprintf("created %s\npath: %s\nUse the project's existing setup/start procedure; stop it before sync or destroy.\n", strconv.Quote(branch), prepared.Path)
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
	if err := fs.Parse(args); err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid " + command + " options"}
	}
	if fs.NArg() > 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: command + " accepts at most one workspace"}
	}
	selector := ""
	if fs.NArg() == 1 {
		selector = fs.Arg(0)
	}
	g, s, err := storeFor(ctx, false)
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
	if workspaceInfo.State == "prepared" && workspaceInfo.Generation == 1 {
		configuration = "verified"
	}
	data := "no_workspace_data_by_eve"
	if len(resourceRows) != 0 {
		data = "provider_empty_initial_state"
	}
	response.Verification = &verification{Configuration: configuration, Runtime: "not_checked", Code: "not_verified_by_eve", Data: data}
	response.Human = fmt.Sprintf("workspace %s\nbranch: %s\nstate: %s phase=%s generation=%d\npath: %s\n%s%s", workspaceInfo.ID, quote(workspaceInfo.Branch), workspaceInfo.State, workspaceInfo.Phase, workspaceInfo.Generation, workspaceInfo.Path, servicesHuman(allocation), resourcesHuman(resourceRows))
	return response, nil
}
func resume(ctx context.Context, args []string) (*output, error) {
	fs, _ := newFlags("resume")
	if err := fs.Parse(args); err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid resume options"}
	}
	if fs.NArg() > 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "resume accepts at most one workspace"}
	}
	selector := ""
	if fs.NArg() == 1 {
		selector = fs.Arg(0)
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
	if err := fs.Parse(args); err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid destroy options"}
	}
	if fs.NArg() > 1 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "destroy accepts at most one workspace"}
	}
	selector := ""
	if fs.NArg() == 1 {
		selector = fs.Arg(0)
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
	case "E_CLEANUP_PENDING", "E_GIT_RECONCILE", "E_PUBLICATION_RECONCILE":
		return 6
	case "E_POSSIBLY_RUNNING", "E_WORKTREE_DIRTY", "E_APPROVAL_REQUIRED", "E_WORKSPACE_BUSY", "E_WORKSPACE_NOT_FOUND", "E_TRACKED_CREDENTIAL_FILE", "E_MANAGED_VALUE_CHANGED", "E_PORT_OCCUPIED", "E_GIT_OWNERSHIP", "E_GIT_LOCKED", "E_SOURCE_UNREGISTERED":
		return 3
	case "E_USAGE", "E_MANIFEST_INVALID", "E_ENV_SYNTAX", "E_ENV_SERIALIZATION", "E_PATH_ESCAPE", "E_CONFIG_INVALID":
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
	response := &output{SchemaVersion: 1, Command: command, OK: false, Error: &commandError{Code: code, Message: message, Details: map[string]any{}}}
	if errors.As(err, &d) {
		if d.Path != "" {
			response.Error.Details["path"] = d.Path
		}
		if d.Port != 0 {
			response.Error.Details["port"] = d.Port
		}
	}
	switch code {
	case "E_CLEANUP_PENDING":
		response.Error.NextAction = "stop or assess the listener, then rerun the same destroy command"
	case "E_APPROVAL_REQUIRED":
		response.Error.NextAction = "review the planned local effects and rerun with the listed safety flags"
	case "E_WORKTREE_DIRTY":
		response.Error.NextAction = "review changes or use --discard-changes to discard that work"
	case "E_POSSIBLY_RUNNING":
		response.Error.NextAction = "stop the ordinary project launcher first; --assume-stopped is an assessment assertion only"
	}
	return response
}
