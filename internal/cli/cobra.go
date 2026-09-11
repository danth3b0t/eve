package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"eve/internal/domain"
)

type exitSignal struct{ code int }

func (e exitSignal) Error() string { return fmt.Sprintf("exit %d", e.code) }

type helpSection struct {
	title string
	lines []string
}

type commandMeta struct {
	Short     string
	Purpose   string
	Usage     []string
	Examples  []string
	Reads     []string
	Changes   []string
	Preserves []string
}

type commandRoute struct {
	name      string
	leafs     []string
	short     string
	handler   func(context.Context, []string) (*output, error)
	flags     func(*cobra.Command)
	meta      commandMeta
	hidden    bool
	validArgs cobra.CompletionFunc
}

func invoke(parts ...string) func(context.Context, []string) (*output, error) {
	return func(ctx context.Context, args []string) (*output, error) {
		return run(ctx, append(append([]string{}, parts...), args...))
	}
}

func cobraCommand(ctx context.Context, route commandRoute, response **output) *cobra.Command {
	command := &cobra.Command{
		Use:                route.name,
		Short:              route.short,
		Long:               renderCommandReference(route.meta),
		Args:               cobra.ArbitraryArgs,
		Hidden:             route.hidden,
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(command *cobra.Command, args []string) error {
			result, err := route.handler(ctx, args)
			*response = result
			if err != nil {
				return err
			}
			return writeSuccess(command.OutOrStdout(), result, wantsJSON(args))
		},
		ValidArgsFunction: route.validArgs,
	}
	if route.flags != nil {
		route.flags(command)
	}
	return command
}

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
func newCommandTree(ctx context.Context, buffer **output) *cobra.Command {
	root := &cobra.Command{
		Use:                "eve",
		Short:              "Disposable connected development worktrees",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(command *cobra.Command, args []string) error {
			result := generalHelpResult(helpOptions{Interactive: terminalHuman(command.OutOrStdout())})
			*buffer = result
			return writeSuccess(command.OutOrStdout(), result, wantsJSON(args))
		},
	}
	root.SetHelpCommand(&cobra.Command{Hidden: true})
	commandTreeRoutes(ctx, buffer, root)
	return root
}

func commandTreeRoutes(ctx context.Context, buffer **output, root *cobra.Command) {
	root.AddCommand(cobraCommand(ctx, commandRoute{name: "version", short: "Print command version", meta: simpleReference("Version", "Show the compiled EVE version.", "No repository/provider/state effects."), handler: invoke("version")}, buffer))
	root.AddCommand(cobraCommand(ctx, commandRoute{name: "help", short: "Explain commands and local evidence limits", meta: simpleReference("Help", "Explain commands, evidence availability, and cleanup behavior.", "Help never opens writable state or contacts a provider."), handler: helpHandler}, buffer))
	root.AddCommand(completionCommandTree(ctx, buffer))
	auth := cobraCommand(ctx, commandRoute{name: "auth", short: "Manage local provider credentials", meta: authReference(), handler: invoke("auth")}, buffer)
	convex := cobraCommand(ctx, commandRoute{name: "convex", short: "Manage Convex credentials", meta: authReference(), handler: invoke("auth", "convex")}, buffer)
	convex.AddCommand(
		cobraCommand(ctx, commandRoute{name: "login", short: "Validate and store a team access token", meta: loginReference(), flags: loginFlags, handler: invoke("auth", "convex", "login")}, buffer),
		cobraCommand(ctx, commandRoute{name: "status", short: "Show stored credential metadata", meta: inspectReference("Credential status", "Show profile metadata without validating liveness"), flags: profileFlags, handler: invoke("auth", "convex", "status")}, buffer),
		cobraCommand(ctx, commandRoute{name: "logout", short: "Remove a local credential profile", meta: authLogoutReference(), flags: profileFlags, handler: invoke("auth", "convex", "logout")}, buffer),
	)
	auth.AddCommand(convex)
	root.AddCommand(auth)
	for _, route := range commandRoutes() {
		root.AddCommand(cobraCommand(ctx, route, buffer))
	}
}

func commandRoutes() []commandRoute {
	return []commandRoute{
		{name: "create", short: "Create a connected worktree", meta: createReference(), flags: createFlags, handler: invoke("create")},
		{name: "plan", short: "Preview creation without mutation", meta: planReference(), flags: planFlags, handler: invoke("plan")},
		{name: "keys", short: "Show committed interpolation variables", meta: keysReference(), flags: jsonOnlyFlags, handler: invoke("keys")},
		{name: "path", short: "Print a workspace path", meta: inspectReference("Path", "Print the canonical path of one workspace"), flags: selectorFlags, validArgs: workspaceCompletion(completionSelector), handler: invoke("path")},
		{name: "status", short: "Show workspace configuration state", meta: statusReference(), flags: statusFlags, validArgs: workspaceCompletion(completionSelector), handler: invoke("status")},
		{name: "resume", short: "Continue an unfinished operation", meta: resumeReference(), flags: selectorFlags, validArgs: workspaceCompletion(completionResume), handler: invoke("resume")},
		{name: "sync", short: "Apply supported committed configuration changes", meta: syncReference(), flags: syncFlags, validArgs: workspaceCompletion(completionSync), handler: invoke("sync")},
		{name: "list", short: "List this repository's EVE workspaces", meta: listReference(), flags: listFlags, handler: invoke("list")},
		{name: "doctor", short: "Diagnose configuration and ownership", meta: doctorReference(), flags: doctorFlags, validArgs: workspaceCompletion(completionSelector), handler: invoke("doctor")},
		{name: "gc", short: "Report exact cleanup candidates", meta: gcReference(), flags: gcFlags, handler: invoke("gc")},
		{name: "destroy", short: "Remove an owned workspace and resources", meta: destroyReference(), flags: destroyFlags, validArgs: workspaceCompletion(completionDestroy), handler: invoke("destroy")},
		{name: "init", short: "Generate or update eve.toml", meta: initReference(), flags: initFlags, handler: invoke("init")},
		{name: "state", short: "Context evidence boundaries", meta: simpleReference("State", "Explain recorded versus observed EVE state.", "This reference performs no operation."), hidden: true, handler: topicHandler("State boundaries", "Recorded state is not live-process truth, credential presence is not validation, and expiry is not remote absence.")},
		{name: "cleanup", short: "Manual deletion recovery", meta: simpleReference("Cleanup", "Explain exact cleanup after manual removal.", "This reference performs no cleanup."), hidden: true, handler: topicHandler("Cleanup boundaries", "Raw deletion leaves EVE records, remote resources, and claims. Help reports evidence; it never authorizes remote deletion.")},
	}
}

func helpHandler(ctx context.Context, args []string) (*output, error) {
	if len(args) == 0 {
		return generalHelpResult(helpOptionsFromArgs(args)), nil
	}
	return referenceForPath(args, helpOptionsFromArgs(args))
}

func helpOptionsFromArgs(args []string) helpOptions {
	var options helpOptions
	for _, arg := range args {
		if arg == "--no-context" {
			options.NoContext = true
		}
	}
	return options
}

func generalHelpResult(options helpOptions) *output {
	var text strings.Builder
	text.WriteString("EVE — disposable connected development worktrees.\n\n")
	text.WriteString("Usage:\n  eve <command> [options]\n\n")
	text.WriteString("Setup:\n  init, auth\n\n")
	text.WriteString("Inspect:\n  plan, keys, list, status, path, doctor\n\nChange:\n  create, sync, resume, destroy, gc\n\nUtility:\n  completion, version, help\n\n")
	text.WriteString("Ask for details with `eve <command> --help` or `eve help <command>`. Help is read-only and never opens writable authority.\n")
	text.WriteString(contextSection(context.Background(), options))
	return &output{SchemaVersion: 1, Command: "help", OK: true, Human: text.String()}
}

func renderCommandReference(meta commandMeta) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%s\n", meta.Purpose)
	if len(meta.Usage) != 0 {
		text.WriteString("\nUsage:\n")
		for _, usage := range meta.Usage {
			fmt.Fprintf(&text, "  eve %s\n", usage)
		}
	}
	appendSection := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&text, "\n%s:\n", title)
		for _, line := range lines {
			fmt.Fprintf(&text, "  %s\n", line)
		}
	}
	appendSection("Reads", meta.Reads)
	appendSection("Changes", meta.Changes)
	appendSection("Preserves", meta.Preserves)
	appendSection("Examples", meta.Examples)
	return text.String()
}

func simpleReference(title, purpose, effect string) commandMeta {
	return commandMeta{Short: title, Purpose: purpose, Reads: []string{effect}}
}

func runCobra(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if path, ok := helpRequestPath(args); ok {
		options := helpOptionsFromArgs(args)
		options.Interactive = terminalHuman(stdout)
		return writeHelpPath(stdout, path, options)
	}
	if len(args) >= 3 && args[0] == "__complete" && (args[len(args)-2] == "--from" || args[len(args)-2] == "--profile") {
		return writeFlagCompletion(ctx, args, stdout, stderr)
	}
	var executed *output
	root := newCommandTree(ctx, &executed)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	root.SetContext(ctx)
	err := root.Execute()
	if executed != nil {
		return 0, err
	}
	var signal exitSignal
	if errors.As(err, &signal) {
		return signal.code, nil
	}
	return 0, err
}

func helpRequestPath(args []string) ([]string, bool) {
	var path []string
	for index, arg := range args {
		if arg == "--" {
			if arg == "--no-context" {
				continue
			}
			break
		}
		if arg == "--help" || arg == "-h" {
			if len(path) > 1 {
				switch path[0] {
				case "auth":
					if len(path) > 3 {
						path = path[:3]
					}
				default:
					path = path[:1]
				}
			}
			return path, true
		}
		if arg == "help" && len(path) == 0 {
			if len(args) == 1 {
				return []string{}, true
			}
			path = append(path, args[index+1:]...)
			return path, true
		}
		path = append(path, arg)
	}
	return nil, false
}

func writeHelpPath(stdout io.Writer, path []string, options helpOptions) (int, error) {
	if len(path) == 0 {
		result := generalHelpResult(options)
		return 0, writeSuccess(stdout, result, false)
	}
	result, err := referenceForPath(path, options)
	if err != nil {
		return 0, err
	}
	return 0, writeSuccess(stdout, result, false)
}

func topicHandler(title, body string) func(context.Context, []string) (*output, error) {
	return func(ctx context.Context, args []string) (*output, error) {
		return &output{SchemaVersion: 1, Command: "help " + strings.ToLower(title), OK: true, Human: "# " + title + "\n\n" + body + "\n"}, nil
	}
}

func commandCompletionScript(ctx context.Context, tree *cobra.Command, shell string) (*output, error) {
	var buffer bytes.Buffer
	var err error
	switch shell {
	case "bash":
		err = tree.GenBashCompletionV2(&buffer, true)
	case "zsh":
		err = tree.GenZshCompletion(&buffer)
	default:
		return nil, &domain.Error{Code: "E_USAGE", Message: "completion supports bash or zsh"}
	}
	if err != nil {
		return nil, err
	}
	return &output{SchemaVersion: 1, Command: "completion " + shell, OK: true, Human: buffer.String()}, nil
}

func completionCommandTree(ctx context.Context, buffer **output) *cobra.Command {
	command := &cobra.Command{
		Use:                "completion <bash|zsh>",
		Short:              "Generate a shell completion script",
		Args:               cobra.ExactArgs(1),
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(command *cobra.Command, args []string) error {
			var result *output
			result, err := commandCompletionScript(ctx, command.Root(), args[0])
			*buffer = result
			if err != nil {
				return err
			}
			return writeSuccess(command.OutOrStdout(), result, wantsJSON(args))
		},
	}
	return command
}
