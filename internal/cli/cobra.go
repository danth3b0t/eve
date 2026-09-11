package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

type exitSignal struct{ code int }

func (e exitSignal) Error() string { return fmt.Sprintf("exit %d", e.code) }

type commandHandler func(context.Context, *commandOptions, []string) (*output, error)

type commandRoute struct {
	name     string
	use      string
	short    string
	args     cobra.PositionalArgs
	meta     commandMeta
	path     []string
	register func(*cobra.Command, *commandOptions)
	complete cobra.CompletionFunc
	handler  commandHandler
	hidden   bool
}

func attachRoute(ctx context.Context, options *commandOptions, route commandRoute) *cobra.Command {
	command := &cobra.Command{
		Use:               route.use,
		Short:             route.short,
		Args:              route.args,
		Hidden:            route.hidden,
		SilenceErrors:     true,
		SilenceUsage:      true,
		ValidArgsFunction: route.complete,
	}
	metaPath := route.path
	if len(metaPath) == 0 {
		metaPath = []string{route.name}
	}
	attachCommandMeta(qualifiedPath(metaPath...), route.meta)
	if route.register != nil {
		route.register(command, options)
	}
	if route.handler == nil {
		command.RunE = func(command *cobra.Command, args []string) error {
			return renderCommandHelp(command, helpOptions{NoContext: options.NoContext, Interactive: terminalHuman(command.OutOrStdout())}, options.JSON, command.OutOrStdout())
		}
		return command
	}
	command.RunE = func(command *cobra.Command, args []string) error {
		adapterContext := context.WithValue(command.Context(), commandInvocationKey{}, command.CommandPath())
		adapterContext = context.WithValue(adapterContext, completionRootKey{}, command.Root())
		command.SetContext(adapterContext)
		options.Progress = command.ErrOrStderr()
		result, err := route.handler(adapterContext, options, args)
		if err != nil {
			return err
		}
		return writeSuccess(command.OutOrStdout(), result, options.JSON)
	}
	return command
}

type commandInvocationKey struct{}

func writeSuccess(stdout io.Writer, result *output, jsonMode bool) error {
	if result == nil {
		return exitSignal{1}
	}
	if jsonMode {
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprint(stdout, result.Human); err != nil {
			return err
		}
	}
	if !result.OK {
		return exitSignal{3}
	}
	return nil
}

func writeJSON(stdout io.Writer, result any) error {
	return json.NewEncoder(stdout).Encode(result)
}

func newCommandTree(ctx context.Context, options *commandOptions) *cobra.Command {
	root := &cobra.Command{
		Use:                        "eve",
		Short:                      "Disposable connected development worktrees",
		Args:                       cobra.NoArgs,
		SilenceErrors:              true,
		SilenceUsage:               true,
		CompletionOptions:          cobra.CompletionOptions{DisableDefaultCmd: true, HiddenDefaultCmd: true},
		SuggestionsMinimumDistance: 2,
	}
	root.CompletionOptions.SetDefaultShellCompDirective(cobra.ShellCompDirectiveNoFileComp)
	root.SetContext(ctx)
	root.PersistentFlags().BoolVarP(&options.NoContext, "no-context", "", false, "omit bounded local evidence from help output")
	root.Flags().BoolVar(&options.JSON, "json", false, "emit the versioned JSON result")
	boolFlagValueCompletion(root, "json")
	boolFlagValueCompletion(root, "no-context")
	attachCommandMeta(root.CommandPath(), commandMeta{Purpose: "Prepare a connected worktree, publish native configuration, and exit.", Preserves: []string{"application launch commands, unrelated state, and cloud authorization"}, Context: helpContextGeneral})
	root.RunE = func(command *cobra.Command, args []string) error {
		return renderCommandHelp(command, helpOptions{NoContext: options.NoContext, Interactive: terminalHuman(command.OutOrStdout())}, options.JSON, command.OutOrStdout())
	}
	root.SetHelpFunc(func(command *cobra.Command, args []string) {
		_ = renderCommandHelp(command, helpOptions{NoContext: options.NoContext, Interactive: terminalHuman(command.OutOrStdout())}, options.JSON, command.OutOrStdout())
	})
	commandTreeRoutes(ctx, options, root)
	registerHelpCommand(root, options)
	_ = root.Flags().Lookup("json")
	return root
}

func registerHelpCommand(root *cobra.Command, options *commandOptions) {
	help := &cobra.Command{
		Use:   "help [command]",
		Short: "Explain commands and local evidence limits",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			target, err := helpPathTarget(root, args)
			if err != nil {
				return err
			}
			return renderCommandHelp(target, helpOptions{NoContext: options.NoContext, Interactive: terminalHuman(command.OutOrStdout())}, options.JSON, command.OutOrStdout())
		},
		ValidArgsFunction: helpArgsCompletion,
	}
	help.Flags().BoolVar(&options.JSON, "json", false, "emit the versioned JSON result")
	attachCommandMeta(qualifiedPath("help"), commandMeta{Purpose: "Explain commands, defaults, effects, and local evidence limits.", Arguments: []argumentMeta{{Name: "command", Requirement: "optional", Description: "Command path or help-only state/cleanup topic", Omitted: "root overview"}}, Preserves: []string{"writable state, credentials, provider resources, and user files"}, Examples: []string{"eve help completion", "eve help completion zsh --json"}, Context: helpContextGeneral})
	root.SetHelpCommand(help)
}

func helpPathTarget(root *cobra.Command, path []string) (*cobra.Command, error) {
	current := root
	for index, name := range path {
		var next *cobra.Command
		for _, child := range current.Commands() {
			if child.Name() == name {
				next = child
				break
			}
		}
		if next == nil || (next.Hidden && !(index == 0 && (name == "state" || name == "cleanup"))) {
			return nil, &scopedUsageError{commandPath: qualifiedPath(path[:index+1]...), message: "unknown help topic", usage: "eve help <command>"}
		}
		current = next
		if current.Hidden && len(path) > index+1 {
			return nil, &scopedUsageError{commandPath: qualifiedPath(path[:index+1]...), message: "unknown nested help topic", usage: "eve help " + name}
		}
	}
	return current, nil
}

func commandTreeRoutes(ctx context.Context, options *commandOptions, root *cobra.Command) {
	auth := attachRoute(ctx, options, commandRoute{name: "auth", use: "auth", short: "Manage local provider credentials", args: cobra.NoArgs, meta: authReference()})
	convex := attachRoute(ctx, options, commandRoute{name: "convex", use: "convex", short: "Manage Convex credentials", args: cobra.NoArgs, meta: authReference(), path: []string{"auth", "convex"}})
	login := attachRoute(ctx, options, commandRoute{name: "login", use: "login --project <team:project> --profile <name> [--token-stdin]", short: "Validate and store a team access token", args: cobra.NoArgs, meta: loginReference(), path: []string{"auth", "convex", "login"}, register: loginFlags, complete: flagsOnlyCompletion, handler: func(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
		return authLogin(ctx, opts, args, defaultAuthValidator)
	}})
	status := attachRoute(ctx, options, commandRoute{name: "status", use: "status [--profile <name>]", short: "Show stored credential metadata", args: cobra.NoArgs, meta: authStatusReference(), path: []string{"auth", "convex", "status"}, register: profileFlags, complete: flagsOnlyCompletion, handler: authStatus})
	logout := attachRoute(ctx, options, commandRoute{name: "logout", use: "logout [--profile <name>]", short: "Remove a local credential profile", args: cobra.NoArgs, meta: authLogoutReference(), path: []string{"auth", "convex", "logout"}, register: profileFlags, complete: flagsOnlyCompletion, handler: authLogout})
	convex.AddCommand(login, status, logout)
	auth.AddCommand(convex)
	root.AddCommand(auth)

	root.AddCommand(completionCommandTree(ctx, options))
	for _, route := range commandRoutes() {
		root.AddCommand(attachRoute(ctx, options, route))
	}
}

func commandRoutes() []commandRoute {
	return []commandRoute{
		{name: "version", use: "version", short: "Print command version", args: cobra.NoArgs, meta: versionReference(), register: jsonOnlyFlags, complete: flagsOnlyCompletion, handler: versionResult},
		{name: "create", use: "create <branch>", short: "Create a connected worktree", args: exactOne("branch"), meta: createReference(), register: createFlags, complete: createTargetCompletion, handler: create},
		{name: "plan", use: "plan <branch>", short: "Preview creation without mutation", args: exactOne("branch"), meta: planReference(), register: planFlags, complete: createTargetCompletion, handler: plan},
		{name: "keys", use: "keys", short: "Show committed interpolation variables", args: cobra.NoArgs, meta: keysReference(), register: jsonOnlyFlags, complete: flagsOnlyCompletion, handler: keys},
		{name: "path", use: "path [workspace]", short: "Print a workspace path", args: optionalOne("workspace"), meta: pathReference(), register: jsonOnlyFlags, complete: workspaceCompletion(completionSelector), handler: func(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
			return inspect(ctx, opts, args, true)
		}},
		{name: "status", use: "status [workspace]", short: "Show workspace configuration state", args: optionalOne("workspace"), meta: statusReference(), register: statusFlags, complete: workspaceCompletion(completionSelector), handler: func(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
			return inspect(ctx, opts, args, false)
		}},
		{name: "resume", use: "resume [workspace]", short: "Continue an unfinished operation", args: optionalOne("workspace"), meta: resumeReference(), register: resumeFlags, complete: workspaceCompletion(completionResume), handler: resume},
		{name: "sync", use: "sync [workspace]", short: "Apply supported committed configuration changes", args: optionalOne("workspace"), meta: syncReference(), register: syncFlags, complete: workspaceCompletion(completionSync), handler: syncWorkspace},
		{name: "list", use: "list", short: "List this repository's EVE workspaces", args: cobra.NoArgs, meta: listReference(), register: listFlags, complete: flagsOnlyCompletion, handler: list},
		{name: "doctor", use: "doctor [workspace]", short: "Diagnose configuration and ownership", args: optionalOne("workspace"), meta: doctorReference(), register: doctorFlags, complete: workspaceCompletion(completionSelector), handler: doctor},
		{name: "gc", use: "gc", short: "Report exact cleanup candidates", args: cobra.NoArgs, meta: gcReference(), register: gcFlags, complete: flagsOnlyCompletion, handler: gc},
		{name: "destroy", use: "destroy [workspace]", short: "Remove an owned workspace and resources", args: optionalOne("workspace"), meta: destroyReference(), register: destroyFlags, complete: workspaceCompletion(completionDestroy), handler: destroy},
		{name: "init", use: "init", short: "Generate or update eve.toml", args: cobra.NoArgs, meta: initReference(), register: initFlags, complete: flagsOnlyCompletion, handler: initialize},
		{name: "state", use: "state", short: "Context evidence boundaries", args: cobra.NoArgs, meta: stateReference(), hidden: true, handler: topicHandler("State boundaries", "Recorded state is not live-process truth, credential presence is not validation, and expiry is not remote absence.")},
		{name: "cleanup", use: "cleanup", short: "Manual deletion recovery", args: cobra.NoArgs, meta: cleanupReference(), hidden: true, handler: topicHandler("Cleanup boundaries", "Raw deletion leaves EVE records, remote resources, and claims. Help reports evidence; it never authorizes remote deletion.")},
	}
}

func exactOne(name string) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("missing required %s", name)
		}
		if len(args) != 1 {
			return fmt.Errorf("%s accepts exactly one %s", command.Name(), name)
		}
		return nil
	}
}

func optionalOne(name string) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if len(args) > 1 {
			return fmt.Errorf("%s accepts at most one %s", command.Name(), name)
		}
		return nil
	}
}

func topicHandler(title, body string) commandHandler {
	return func(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
		_ = ctx
		return &output{SchemaVersion: 1, Command: "help " + title, OK: true, Human: "# " + title + "\n\n" + body + "\n"}, nil
	}
}

func versionResult(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
	_ = ctx
	return &output{SchemaVersion: 1, Command: "version", OK: true, Version: Version, Human: "eve " + Version + "\n"}, nil
}

func runCobra(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	options := newCommandOptions(stderr)
	root := newCommandTree(ctx, options)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	jsonMode := parsedJSONMode(root, args)
	err := root.Execute()
	if err != nil {
		var signal exitSignal
		if errors.As(err, &signal) {
			return signal.code, nil
		}
		normalized := normalizeCobraError(root, args, err)
		var usage *scopedUsageError
		if errors.As(normalized, &usage) {
			if writeErr := writeUsageError(stdout, stderr, normalized, jsonMode); writeErr != nil {
				return 0, writeErr
			}
			return 2, nil
		}
		target, _ := commandForArgs(root, args)
		result := errorResult(target.CommandPath(), normalized)
		if jsonMode {
			if writeErr := writeJSON(stdout, result); writeErr != nil {
				return 0, writeErr
			}
		} else {
			_, _ = fmt.Fprintf(stderr, "error: %s: %s\n", result.Error.Code, result.Error.Message)
			if result.Error.NextAction != "" {
				_, _ = fmt.Fprintf(stderr, "next: %s\n", result.Error.NextAction)
			}
		}
		return exitCode(normalized), nil
	}
	return 0, nil
}
