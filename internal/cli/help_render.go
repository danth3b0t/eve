package cli

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"eve/internal/domain"
)

type helpFlag struct {
	Name        string `json:"name"`
	Shorthand   string `json:"shorthand,omitempty"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

type helpChild struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type helpReference struct {
	Path      string         `json:"path"`
	Synopsis  string         `json:"synopsis"`
	Arguments []argumentMeta `json:"arguments,omitempty"`
	Children  []helpChild    `json:"children,omitempty"`
	Options   []helpFlag     `json:"options,omitempty"`
	Reads     []string       `json:"reads,omitempty"`
	Changes   []string       `json:"changes,omitempty"`
	Preserves []string       `json:"preserves,omitempty"`
	Output    []string       `json:"output,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	Examples  []string       `json:"examples,omitempty"`
	Context   string         `json:"context,omitempty"`
}

func renderCommandHelp(command *cobra.Command, options helpOptions, jsonMode bool, stdout io.Writer) error {
	meta := metadataForPath(command.CommandPath())
	reference := helpReference{Path: command.CommandPath(), Synopsis: command.UseLine(), Arguments: meta.Arguments, Reads: meta.Reads, Changes: meta.Changes, Preserves: meta.Preserves, Output: meta.Output, Notes: meta.Notes, Examples: meta.Examples}
	for _, child := range command.Commands() {
		if child.Hidden || child.Name() == "help" || strings.HasPrefix(child.Name(), "__complete") {
			continue
		}
		reference.Children = append(reference.Children, helpChild{Name: child.Name(), Description: child.Short})
	}
	sort.SliceStable(reference.Children, func(i, j int) bool {
		completionOrder := map[string]int{"bash": 0, "zsh": 1, "setup": 2}
		left, leftOK := completionOrder[reference.Children[i].Name]
		right, rightOK := completionOrder[reference.Children[j].Name]
		if command.Name() == "completion" && leftOK && rightOK {
			return left < right
		}
		return reference.Children[i].Name < reference.Children[j].Name
	})
	reference.Options = registeredHelpFlags(command)
	if meta.Context != helpContextNone {
		reference.Context = contextSection(command.Context(), options)
	}
	human := renderHumanHelp(reference, meta.Purpose)
	result := &output{SchemaVersion: 1, Command: "help " + command.CommandPath(), OK: true, Help: &reference, Human: human}
	if command.CommandPath() == "eve" {
		result.Command = "help"
	}
	if jsonMode {
		return writeJSON(stdout, result)
	}
	_, err := fmt.Fprint(stdout, human)
	return err
}

func registeredHelpFlags(command *cobra.Command) []helpFlag {
	seen := map[string]bool{}
	var out []helpFlag
	appendSet := func(set *pflag.FlagSet) {
		if set == nil {
			return
		}
		set.VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden || seen[flag.Name] {
				return
			}
			seen[flag.Name] = true
			typeName := flag.Value.Type()
			if flag.NoOptDefVal != "" {
				typeName = "bool"
			}
			out = append(out, helpFlag{Name: flag.Name, Shorthand: flag.Shorthand, Type: typeName, Default: flag.DefValue, Description: flag.Usage})
		})
	}
	appendSet(command.LocalNonPersistentFlags())
	appendSet(command.PersistentFlags())
	appendSet(command.InheritedFlags())
	if !seen["help"] {
		out = append(out, helpFlag{Name: "help", Shorthand: "h", Type: "bool", Default: "false", Description: "Show this help"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func renderHumanHelp(reference helpReference, purpose string) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%s — %s\n", reference.Path, purpose)
	if reference.Synopsis != "" {
		fmt.Fprintf(&text, "\nUsage:\n  %s\n", reference.Synopsis)
	}
	if len(reference.Arguments) != 0 {
		text.WriteString("\nArguments:\n")
		for _, arg := range reference.Arguments {
			requirement := arg.Requirement
			if requirement == "" {
				requirement = "optional"
			}
			if len(requirement) != 0 {
				requirement = strings.ToUpper(requirement[:1]) + requirement[1:]
			}
			description := strings.TrimSuffix(arg.Description, ".")
			fmt.Fprintf(&text, "  %-18s %s. %s.", arg.Name, requirement, description)
			if arg.Omitted != "" {
				fmt.Fprintf(&text, " Omitted: %s.", arg.Omitted)
			}
			text.WriteString("\n")
		}
	}
	if len(reference.Children) != 0 {
		text.WriteString("\nCommands:\n")
		for _, child := range reference.Children {
			fmt.Fprintf(&text, "  %-16s %s\n", child.Name, child.Description)
		}
	}
	if len(reference.Options) != 0 {
		text.WriteString("\nOptions:\n")
		for _, option := range reference.Options {
			name := "      --" + option.Name
			if option.Shorthand != "" {
				name = fmt.Sprintf("  -%s, --%s", option.Shorthand, option.Name)
			}
			if option.Type != "bool" {
				name += " <" + option.Type + ">"
			}
			fmt.Fprintf(&text, "%-25s (default %s) %s\n", name, strconv.Quote(option.Default), option.Description)
		}
	}
	appendLines := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&text, "\n%s:\n", title)
		for _, line := range lines {
			fmt.Fprintf(&text, "  %s\n", line)
		}
	}
	appendLines("Output", reference.Output)
	appendLines("Reads", reference.Reads)
	appendLines("Changes", reference.Changes)
	appendLines("Preserves", reference.Preserves)
	appendLines("Notes", reference.Notes)
	if len(reference.Examples) != 0 {
		text.WriteString("\nExamples:\n")
		for _, example := range reference.Examples {
			fmt.Fprintf(&text, "  %s\n", example)
		}
	}
	if reference.Context != "" {
		text.WriteString(reference.Context)
	}
	return text.String()
}

type scopedUsageError struct {
	commandPath string
	message     string
	usage       string
	examples    []string
}

func (e *scopedUsageError) Error() string { return e.message }
func (e *scopedUsageError) Unwrap() error {
	return &domain.Error{Code: "E_USAGE", Message: e.message}
}

func normalizeCobraError(command *cobra.Command, args []string, err error) error {
	if err == nil {
		return nil
	}
	var usage *scopedUsageError
	if errors.As(err, &usage) {
		return usage
	}
	var d *domain.Error
	if errors.As(err, &d) && d.Code != "E_USAGE" {
		return err
	}
	target, unknown := commandForArgs(command, args)
	message := sanitizeCompletionDescription(err.Error())
	if errors.As(err, &d) && d.Code == "E_USAGE" && d.Message != "" {
		message = sanitizeCompletionDescription(d.Message)
	}
	if unknown != "" && isCompletionPath(target) {
		if target.Name() == "completion" {
			message = fmt.Sprintf("unsupported completion target %s", strconv.Quote(unknown))
		} else {
			message = target.Name() + " accepts no extra operand(s)"
		}
	} else if strings.HasPrefix(message, "unknown command") {
		message = "unknown command or command path"
	} else if strings.HasPrefix(message, "unknown flag") {
		message = firstLine(message)
	}
	meta := metadataForPath(target.CommandPath())
	return &scopedUsageError{commandPath: target.CommandPath(), message: message, usage: target.UseLine(), examples: meta.Examples}
}

func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return value[:index]
	}
	return value
}

func isCompletionPath(command *cobra.Command) bool {
	for current := command; current != nil; current = current.Parent() {
		if current.Name() == "completion" {
			return true
		}
	}
	return false
}

func commandForArgs(root *cobra.Command, args []string) (*cobra.Command, string) {
	current := root
	lastUnknown := ""
	literal := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if literal {
			continue
		}
		if arg == "--" {
			literal = true
			continue
		}
		if strings.HasPrefix(arg, "--") || (strings.HasPrefix(arg, "-") && arg != "-") {
			if strings.HasPrefix(arg, "--") {
				name, _, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
				var flag *pflag.Flag
				if flags := current.Flags(); flags != nil {
					flag = flags.Lookup(name)
				}
				if flag != nil && flag.NoOptDefVal == "" && !hasValue && index+1 < len(args) {
					index++
				}
			}
			continue
		}
		var child *cobra.Command
		for _, candidate := range current.Commands() {
			if candidate.Name() == arg {
				child = candidate
				break
			}
		}
		if child != nil {
			current = child
			continue
		}
		if lastUnknown == "" {
			lastUnknown = arg
			break
		}
	}
	return current, lastUnknown
}

func writeUsageError(stdout, stderr io.Writer, err error, jsonMode bool) error {
	var usage *scopedUsageError
	if !errors.As(err, &usage) {
		return err
	}
	helpCommand := "eve help " + strings.TrimPrefix(usage.commandPath, "eve")
	helpCommand = strings.ReplaceAll(helpCommand, "  ", " ")
	result := &output{SchemaVersion: 1, Command: usage.commandPath, OK: false, Error: &commandError{Code: "E_USAGE", Message: usage.message, Details: map[string]any{"usage": usage.usage, "help_command": helpCommand}, NextAction: "run the scoped help command; EVE did not repair request syntax"}}
	if jsonMode {
		return writeJSON(stdout, result)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "error: %s: %s\n\nUsage:\n  %s\n", result.Error.Code, result.Error.Message, usage.usage)
	if len(usage.examples) != 0 {
		text.WriteString("\nExamples:\n")
		for _, example := range usage.examples {
			fmt.Fprintf(&text, "  %s\n", example)
		}
	}
	text.WriteString("\nNo changes made. Use `" + result.Error.Details["help_command"].(string) + "` for the full reference.\n")
	_, writeErr := fmt.Fprint(stderr, text.String())
	return writeErr
}

func parsedJSONMode(root *cobra.Command, args []string) bool {
	current := root
	literal := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if literal {
			continue
		}
		if arg == "--" {
			literal = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			var flag *pflag.Flag
			if current.Flags() != nil {
				flag = current.Flags().Lookup(name)
			}
			if flag == nil {
				continue
			}
			if name == "json" {
				if !hasValue {
					return true
				}
				enabled, parseErr := strconv.ParseBool(value)
				if parseErr == nil {
					return enabled
				}
			}
			if flag.NoOptDefVal == "" && !hasValue && index+1 < len(args) {
				index++
			}
			continue
		}
		var child *cobra.Command
		for _, candidate := range current.Commands() {
			if candidate.Name() == arg {
				child = candidate
				break
			}
		}
		if child != nil {
			current = child
		}
	}
	return false
}

func helpArgsCompletion(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	current := command.Root()
	for _, part := range args {
		var next *cobra.Command
		for _, child := range current.Commands() {
			if child.Name() == part {
				next = child
				break
			}
		}
		if next == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		current = next
	}
	var out []string
	for _, child := range current.Commands() {
		if child.Name() == "__complete" || child.Name() == "__completeNoDesc" {
			continue
		}
		if child.Hidden && !(child.Name() == "state" || child.Name() == "cleanup") {
			continue
		}
		if child.Hidden && len(args) != 0 {
			continue
		}
		if hasCompletionPrefix(child.Name(), toComplete) {
			short := child.Short
			if short == "" {
				short = metadataForPath(child.CommandPath()).Purpose
			}
			out = append(out, child.Name()+"\t"+sanitizeCompletionDescription(short))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
