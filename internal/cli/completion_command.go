package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"eve/internal/domain"
)

func completionCommandTree(ctx context.Context, options *commandOptions) *cobra.Command {
	parent := attachRoute(ctx, options, commandRoute{name: "completion", use: "completion", short: "Generate or set up shell completions", args: cobra.NoArgs, meta: completionReference(), register: jsonOnlyFlags})
	bash := attachRoute(ctx, options, commandRoute{name: "bash", use: "bash [--no-descriptions]", short: "Print the Bash completion script", args: cobra.NoArgs, meta: completionBashReference(), path: []string{"completion", "bash"}, register: scriptGeneratorFlags, complete: flagsOnlyCompletion, handler: func(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
		return completionScript(ctx, opts, "bash", args)
	}})
	zsh := attachRoute(ctx, options, commandRoute{name: "zsh", use: "zsh [--no-descriptions]", short: "Print the Zsh completion script", args: cobra.NoArgs, meta: completionZshReference(), path: []string{"completion", "zsh"}, register: scriptGeneratorFlags, complete: flagsOnlyCompletion, handler: func(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
		return completionScript(ctx, opts, "zsh", args)
	}})
	setup := attachRoute(ctx, options, commandRoute{name: "setup", use: "setup [--shell auto|bash|zsh] [--json]", short: "Show how to enable completion in your shell", args: cobra.NoArgs, meta: completionSetupReference(), path: []string{"completion", "setup"}, register: completionSetupFlags, complete: flagsOnlyCompletion, handler: completionSetup})
	parent.AddCommand(bash, zsh, setup)
	return parent
}

func scriptGeneratorFlags(command *cobra.Command, options *commandOptions) {
	command.Flags().BoolVar(&options.NoDescriptions, "no-descriptions", false, "omit descriptions from completion candidates")
	boolFlagValueCompletion(command, "no-descriptions")
}

func completionSetupFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().StringVar(&options.Shell, "shell", "auto", "setup guide shell: auto, bash, or zsh")
	_ = command.RegisterFlagCompletionFunc("shell", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var out []string
		for _, shell := range []string{"auto", "bash", "zsh"} {
			if hasCompletionPrefix(shell, toComplete) {
				out = append(out, shell)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
}

func completionScript(ctx context.Context, opts *commandOptions, shell string, args []string) (*output, error) {
	_ = ctx
	if len(args) != 0 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "script generation accepts no operands"}
	}
	command, ok := currentCompletionRoot(ctx)
	if !ok {
		// The action adapter supplies the root through context. Retain a narrow
		// fallback for direct unit calls without making a second grammar.
		return nil, &domain.Error{Code: "E_INTERNAL", Message: "completion command tree is unavailable"}
	}
	var buffer bytes.Buffer
	var err error
	switch shell {
	case "bash":
		err = adaptCompletionScript(command, "bash", &buffer, !opts.NoDescriptions)
	case "zsh":
		err = adaptCompletionScript(command, "zsh", &buffer, !opts.NoDescriptions)
	default:
		return nil, &domain.Error{Code: "E_USAGE", Message: "completion supports bash or zsh"}
	}
	if err != nil {
		var bufferErr completionAdapterError
		if errors.As(err, &bufferErr) {
			return nil, &domain.Error{Code: "E_COMPLETION_TEMPLATE", Message: "installed Cobra completion template does not match the reviewed safe adapter"}
		}
		return nil, err
	}
	return &output{SchemaVersion: 1, Command: "completion " + shell, OK: true, Human: buffer.String()}, nil
}

type completionRootKey struct{}

func currentCompletionRoot(ctx context.Context) (*cobra.Command, bool) {
	value, ok := ctx.Value(completionRootKey{}).(*cobra.Command)
	return value, ok && value != nil
}

type completionAdapterError struct{ shell string }

func (e completionAdapterError) Error() string {
	return "unsupported " + e.shell + " completion template"
}

// adaptCompletionScript runs the pinned Cobra generator and applies a narrow,
// version-reviewed transport patch. It deliberately fails closed when the
// template markers disappear instead of silently shipping eval-based request
// construction for user-controlled words.
func adaptCompletionScript(root *cobra.Command, shell string, buffer *bytes.Buffer, descriptions bool) error {
	var raw bytes.Buffer
	var err error
	switch shell {
	case "bash":
		err = root.GenBashCompletionV2(&raw, descriptions)
		if err == nil {
			return adaptBashTransport(&raw, buffer)
		}
	case "zsh":
		if descriptions {
			err = root.GenZshCompletion(&raw)
		} else {
			err = root.GenZshCompletionNoDesc(&raw)
		}
		if err == nil {
			return adaptZshTransport(&raw, buffer)
		}
	}
	return err
}

func replaceRequired(script []byte, old, replacement string, shell string) (string, error) {
	text := string(script)
	if count := stringsCount(text, old); count != 1 {
		return "", completionAdapterError{shell: shell}
	}
	return stringsReplace(text, old, replacement), nil
}

const bashFZFCompletion = `
__eve_fzf_complete()
{
    if [[ ${EVE_FZF_COMPLETION-} == 0 ]]; then
        return 1
    fi

    local fzfTrigger typed protocolPacket protocolDirective candidateText selected selectionStatus
    fzfTrigger="${EVE_FZF_COMPLETION_TRIGGER:-${FZF_COMPLETION_TRIGGER:-**}}"
    [[ -n ${fzfTrigger} ]] || return 1
    (( ${#lastParam} >= ${#fzfTrigger} )) || return 1
    typed="${lastParam:0:${#lastParam}-${#fzfTrigger}}"
    [[ ${lastParam:${#typed}} == "${fzfTrigger}" ]] || return 1

    args=(__complete "${words[@]:1}")
    if (( ${#args[@]} > 0 )); then
        args[$((${#args[@]}-1))]="${typed}"
    else
        args=("${typed}")
    fi
    protocolPacket=$("${requestExecutable}" "${args[@]}" 2>/dev/null)
    protocolDirective="${protocolPacket##*:}"
    if [[ ${protocolDirective} == "${protocolPacket}" || ${protocolDirective} != [0-9]* ]]; then
        out=""
        directive=4
        return 0
    fi
    candidateText="${protocolPacket%:*}"
    if [[ -z ${candidateText//$'\n'/} ]]; then
        out=""
        directive=4
        return 0
    fi

    if declare -F __fzf_comprun > /dev/null 2>&1; then
        selected=$(printf '%s\n' "${candidateText}" | awk 'index($0, "_activeHelp_ ") != 1' | __fzf_comprun "cli" -q "${typed}" --reverse)
    elif command -v fzf > /dev/null 2>&1; then
        selected=$(printf '%s\n' "${candidateText}" | awk 'index($0, "_activeHelp_ ") != 1' | fzf --reverse --query "${typed}")
    else
        return 1
    fi
    selectionStatus=$?
    if (( selectionStatus != 0 )) || [[ -z ${selected} ]]; then
        out=""
        directive=4
        return 0
    fi

    out="${selected%$'\n'}"
    out="${out}:4"
    directive=4
    cur="${typed}"
}

`

func adaptBashTransport(raw, buffer *bytes.Buffer) error {
	text := string(raw.Bytes())
	requestCommand := "__complete"
	if strings.Contains(text, "__completeNoDesc") {
		requestCommand = "__completeNoDesc"
	}
	replacements := [][2]string{
		{"# This function calls the eve program to obtain the completion\n", bashFZFCompletion + "# This function calls the eve program to obtain the completion\n"},
		{"    local requestComp lastParam lastChar args\n", "    local lastParam lastChar args requestExecutable\n"},
		{"    args=(\"${words[@]:1}\")\n    requestComp=\"${words[0]} " + requestCommand + " ${args[*]}\"\n", "    args=(" + requestCommand + " \"${words[@]:1}\")\n    requestExecutable=\"${words[0]}\"\n"},
		{"        requestComp=\"${requestComp} ''\"\n", "        args+=(\"\")\n"},
		{"    if [[ -z ${cur} && ${lastChar} != = ]]; then\n", "    if [[ -z ${cur} && ${lastChar} != = && ${lastParam} != *=* ]]; then\n"},
		{"    __eve_debug \"Calling ${requestComp}\"\n    # Use eval to handle any environment variables and such\n    out=$(eval \"${requestComp}\" 2>/dev/null)\n", "    if __eve_fzf_complete; then\n        return\n    fi\n\n    __eve_debug \"Calling EVE completion adapter\"\n    out=$(\"${requestExecutable}\" \"${args[@]}\" 2>/dev/null)\n"},
		{"if [[ $(type -t compopt) = \"builtin\" ]]; then\n    complete -o default -F __start_eve eve\nelse\n    complete -o default -o nospace -F __start_eve eve\nfi\n", "complete -o nospace -F __start_eve eve\n"},
	}
	for _, replacement := range replacements {
		changed, err := replaceRequired([]byte(text), replacement[0], replacement[1], "bash")
		if err != nil {
			return err
		}
		text = changed
	}
	if _, err := buffer.WriteString("# EVE reviewed completion transport adapter v1 (Cobra v1.10.2; argv-preserving)\n"); err != nil {
		return err
	}
	_, err := buffer.WriteString(text)
	return err
}

func adaptZshTransport(raw, buffer *bytes.Buffer) error {
	text := string(raw.Bytes())
	requestCommand := "__complete"
	if strings.Contains(text, "__completeNoDesc") {
		requestCommand = "__completeNoDesc"
	}
	replacements := [][2]string{
		{"    local lastParam lastChar flagPrefix requestComp out directive comp lastComp noSpace keepOrder\n", "    local lastParam lastChar flagPrefix out directive comp lastComp keepOrder requestExecutable\n    local -a requestArgs flagPrefixArgs keepOrderArgs noSpaceArgs\n"},
		{"        flagPrefix=\"-P ${BASH_REMATCH}\"\n", "        flagPrefix=\"-P ${BASH_REMATCH}\"\n        flagPrefixArgs=(-P \"${BASH_REMATCH}\")\n"},
		{"    requestComp=\"${words[1]} " + requestCommand + " ${words[2,-1]}\"\n", "    requestExecutable=\"${words[1]}\"\n    requestArgs=(" + requestCommand + " \"${words[@]:1}\")\n"},
		{"        requestComp=\"${requestComp} \\\"\\\"\"\n", "        requestArgs+=(\"\")\n"},
		{"    if [ \"${lastChar}\" = \"\" ]; then\n", "    if [[ -z \"${lastChar}\" && \"${lastParam}\" != *=* ]]; then\n"},
		{"    __eve_debug \"About to call: eval ${requestComp}\"\n\n    # Use eval to handle any environment variables and such\n    out=$(eval ${requestComp} 2>/dev/null)\n", "    __eve_debug \"Calling EVE completion adapter\"\n    out=$(\"${requestExecutable}\" \"${requestArgs[@]}\" 2>/dev/null)\n"},
		{"        noSpace=\"-S ''\"\n", "        noSpaceArgs=(-S \"\")\n"},
		{"        keepOrder=\"-V\"\n", "        keepOrderArgs=(-V)\n"},
		{"        if eval _describe $keepOrder \"completions\" completions $flagPrefix $noSpace; then\n", "        if _describe \"${keepOrderArgs[@]}\" completions completions \"${flagPrefixArgs[@]}\" \"${noSpaceArgs[@]}\"; then\n"},
	}
	for _, replacement := range replacements {
		changed, err := replaceRequired([]byte(text), replacement[0], replacement[1], "zsh")
		if err != nil {
			return err
		}
		text = changed
	}
	const adapterComment = "# EVE reviewed completion transport adapter v1 (Cobra v1.10.2; argv-preserving)"
	if afterTag, found := strings.CutPrefix(text, "#compdef eve\n"); found {
		text = "#compdef eve\n" + adapterComment + "\n" + afterTag
	} else {
		text = adapterComment + "\n" + text
	}
	_, err := buffer.WriteString(text)
	return err
}

func stringsCount(value, needle string) int { return strings.Count(value, needle) }
func stringsReplace(value, old, replacement string) string {
	return strings.Replace(value, old, replacement, 1)
}
