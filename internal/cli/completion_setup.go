package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"eve/internal/domain"
)

type setupBlock struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
}

type shellSetupGuide struct {
	Shell          string       `json:"shell"`
	Prerequisites  []string     `json:"prerequisites"`
	CurrentSession []setupBlock `json:"current_session"`
	Persistent     []setupBlock `json:"persistent"`
	Verification   []string     `json:"verification"`
	Removal        []string     `json:"removal"`
}

type completionSetupGuide struct {
	SelectedShell         string            `json:"selected_shell"`
	SelectionSource       string            `json:"selection_source"`
	SupportedShells       []string          `json:"supported_shells"`
	Guides                []shellSetupGuide `json:"guides"`
	ParentShellActivation string            `json:"parent_shell_activation"`
	NoFilesChanged        bool              `json:"no_files_changed"`
}

func completionSetup(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
	_ = ctx
	if len(args) != 0 {
		return nil, &domain.Error{Code: "E_USAGE", Message: "completion setup does not accept operands"}
	}
	if opts.Shell != "auto" && opts.Shell != "bash" && opts.Shell != "zsh" {
		return nil, &domain.Error{Code: "E_USAGE", Message: "unsupported setup shell " + strconvQuote(opts.Shell) + "; expected auto, bash, or zsh"}
	}
	guide := completionSetupGuideData(opts.Shell, os.Getenv("SHELL"))
	response := &output{SchemaVersion: 1, Command: "completion setup", OK: true, Setup: &guide, Human: renderCompletionSetupHuman(guide)}
	return response, nil
}

func completionSetupGuideData(requested, shellEnvironment string) completionSetupGuide {
	supported := []string{"bash", "zsh"}
	selected := requested
	source := "explicit --shell"
	if requested == "auto" {
		selected = "unspecified"
		source = "configured-shell hint from $SHELL; current shell not verified"
		if shell := recognizedShellHint(shellEnvironment); shell != "" {
			selected = shell
		} else {
			source = "no usable configured-shell hint; both supported shells are shown"
		}
	}
	guide := completionSetupGuide{SelectedShell: selected, SelectionSource: source, SupportedShells: supported, ParentShellActivation: "not_checked", NoFilesChanged: true}
	switch selected {
	case "bash", "zsh":
		guide.Guides = []shellSetupGuide{shellGuide(selected)}
	default:
		guide.Guides = []shellSetupGuide{shellGuide("bash"), shellGuide("zsh")}
	}
	return guide
}

func recognizedShellHint(value string) string {
	if value == "" || strings.ContainsRune(value, 0) {
		return ""
	}
	switch filepath.Base(value) {
	case "bash":
		return "bash"
	case "zsh":
		return "zsh"
	default:
		return ""
	}
}

func shellGuide(shell string) shellSetupGuide {
	if shell == "bash" {
		return shellSetupGuide{Shell: "bash", Prerequisites: []string{
			"Install or use the compatible bash-completion package for your Bash; EVE does not install shell packages.",
			"Check that its helper is already loaded in your actual shell: declare -F _get_comp_words_by_ref >/dev/null",
			"If that command fails, load the helper initialization provided by your bash-completion installation. macOS Bash 3.2 and newer Linux Bash can use different compatible packages/paths.",
		}, CurrentSession: []setupBlock{{Title: "Current shell", Lines: []string{
			"After the bash-completion helper check succeeds, run: source <(eve completion bash)",
			"This affects only the current shell session and edits no file.",
		}}}, Persistent: []setupBlock{{Title: "Persistent setup", Lines: []string{
			"Create the user directory: mkdir -p \"${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions\"",
			"Write that exact file: eve completion bash > \"${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions/eve\"",
			"Redirection creates or replaces that file. Inspect any existing file first; the shell must load bash-completion for lazy autoloading.",
			"If your setup does not autoload this directory, source the saved file after helper initialization. A login Bash may use .bash_profile instead of .bashrc; add one line once, deliberately.",
		}}}, Verification: []string{
			"In the real interactive shell, run: complete -p eve",
			"Then type: eve completion <Tab>",
			"Optional fuzzy selection on Bash with fzf installed: eve completion **<Tab>. The trigger defaults to fzf's FZF_COMPLETION_TRIGGER or **; override with EVE_FZF_COMPLETION_TRIGGER or disable with EVE_FZF_COMPLETION=0.",
			"Plain-Tab forward cycling is a Readline keybinding choice: bind '\"\\t\": menu-complete' deliberately in your own inputrc and, optionally, bind '\"\\e[Z\": menu-complete-backward'. EVE never changes terminal key bindings.",
			"Registration alone is not proof; a real Tab request must work. Lazy loading may register only on the first attempt.",
		}, Removal: []string{
			"Current session: complete -r eve",
			"Future sessions: remove only the saved eve completion file or the startup line you deliberately added.",
		}}
	}
	return shellSetupGuide{Shell: "zsh", Prerequisites: []string{
		"Zsh's completion system must be initialized by your existing shell configuration.",
		"Check in the real shell: whence -w compdef",
		"If it is missing, initialize once with the user's own pattern, commonly: autoload -Uz compinit; compinit",
		"EVE does not run compinit, override a framework, or disable permission checks.",
	}, CurrentSession: []setupBlock{{Title: "Current shell", Lines: []string{
		"After compdef is available, run: source <(eve completion zsh)",
		"This affects only the current shell session and edits no file.",
	}}}, Persistent: []setupBlock{{Title: "Autoload directory", Lines: []string{
		"Create a user directory and save the function: mkdir -p \"$HOME/.zsh/completions\"; eve completion zsh > \"$HOME/.zsh/completions/_eve\"",
		"Redirection creates or replaces that exact _eve file.",
		"In the actual .zshrc (under $ZDOTDIR when set), put this before existing completion/framework initialization: fpath=(\"$HOME/.zsh/completions\" $fpath)",
		"If completion is not otherwise initialized, initialize it once afterward. Do not duplicate framework initialization or disable compaudit.",
		"After updating a saved function, a normal completion cache refresh may be needed. Never advise compinit -u or deleting arbitrary dotfiles.",
	}}}, Verification: []string{
		"In an initialized shell you may inspect: print -r -- 'eve -> ' ${+_comps[eve]}",
		"Then perform a real Tab request: eve completion <Tab>",
		"For immediate registration in the current shell after fpath/compdef are ready: autoload -Uz _eve; compdef _eve eve",
	}, Removal: []string{
		"Current initialized session: compdef -d eve",
		"Future sessions: remove only $HOME/.zsh/completions/_eve and the fpath line if no other completion uses that directory.",
	}}
}

func renderCompletionSetupHuman(guide completionSetupGuide) string {
	var text strings.Builder
	text.WriteString("Completion setup for EVE\n")
	fmt.Fprintf(&text, "Selected guide: %s\n", guide.SelectedShell)
	fmt.Fprintf(&text, "Selection source: %s\n", guide.SelectionSource)
	text.WriteString("Parent-shell activation is not checked by an external command; perform the printed checks in your real shell.\n")
	text.WriteString("No shell startup files, EVE state, Git state, credentials, or provider resources are changed.\n\n")
	for _, guide := range guide.Guides {
		fmt.Fprintf(&text, "%s\n", strings.ToUpper(guide.Shell[:1])+guide.Shell[1:])
		text.WriteString("  Prerequisites:\n")
		for _, line := range guide.Prerequisites {
			fmt.Fprintf(&text, "  - %s\n", line)
		}
		for _, block := range append(append([]setupBlock{}, guide.CurrentSession...), guide.Persistent...) {
			fmt.Fprintf(&text, "  %s:\n", block.Title)
			for _, line := range block.Lines {
				fmt.Fprintf(&text, "  - %s\n", line)
			}
		}
		text.WriteString("  Verify in the actual shell:\n")
		for _, line := range guide.Verification {
			fmt.Fprintf(&text, "  - %s\n", line)
		}
		text.WriteString("  Troubleshoot:\n")
		text.WriteString("  - Generated scripts invoke `eve` from PATH as one executable with preserved argv; wrapper aliases/functions that rewrite argv or multi-command aliases are unsupported and should not be used.\n")
		if guide.Shell == "bash" {
			text.WriteString("  - If Tab completion does not change, printing/saving a script does not load it in your current shell.\n")
			text.WriteString("  - If _get_comp_words_by_ref is missing, load the compatible bash-completion helpers first.\n")
			text.WriteString("  - If the fzf trigger opens no picker, install the standalone fzf package or disable EVE_FZF_COMPLETION; ordinary Tab completion does not require fzf.\n")
			text.WriteString("  - If it works in another terminal, running shells do not automatically gain newly written functions.\n")
		} else {
			text.WriteString("  - If Tab completion does not change, the script must be sourced after compinit or placed in an fpath loaded by your configuration.\n")
			text.WriteString("  - If compdef is missing, initialize Zsh completion once with your own shell configuration pattern.\n")
			text.WriteString("  - If workspace names do not complete, they are scoped metadata suggestions; inspect repository/state context without using credentials.\n")
		}
		text.WriteString("  - `gc --workspace` completes long canonical workspace UUIDs, not branch names; descriptions identify the repository and record.\n")
		text.WriteString("  - If a recorded workspace directory disappeared, completion may still show its record for diagnosis/cleanup; it never recreates or deletes from the shell request.\n")
		text.WriteString("  - Saved scripts use `eve` on PATH and must not pin a version-specific mise installation path; regenerate/reload after changing EVE versions.\n")
		text.WriteString("  Removal:\n")
		for _, line := range guide.Removal {
			fmt.Fprintf(&text, "  - %s\n", line)
		}
		text.WriteString("\n")
	}
	text.WriteString("Only Bash and Zsh are supported shell integrations. Explicit `eve completion bash` and `eve completion zsh` remain the script-output interfaces; `eve completion setup` never emits their raw scripts.\n")
	return text.String()
}

func strconvQuote(value string) string {
	return fmt.Sprintf("%q", value)
}
