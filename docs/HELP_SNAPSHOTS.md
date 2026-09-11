# Generated help snapshots

These snapshots are generated from the executable command tree. They intentionally show the default/argument metadata that must stay synchronized with execution. Regenerate after changing command grammar.

## `eve completion`

```text
eve completion — Enable Tab completion for EVE's commands, options, and local workspace names.

Usage:
  eve completion [flags]

Commands:
  bash             Print the Bash completion script
  zsh              Print the Zsh completion script
  setup            Show how to enable completion in your shell

Options:
  -h, --help              (default "false") help for completion
      --json              (default "false") emit the versioned JSON result
      --no-context        (default "false") omit bounded local evidence from help output

Output:
  human setup guidance; use completion bash/zsh for raw shell code

Changes:
  none — No files or EVE state are changed by this command

Preserves:
  shell startup files, EVE state, credentials, and provider resources

Examples:
  eve completion setup
  eve completion bash > eve.bash
  eve completion zsh > _eve
```

## `eve help completion zsh`

```text
eve completion zsh — Print EVE's Zsh completion script.

Usage:
  eve completion zsh [--no-descriptions] [flags]

Options:
  -h, --help              (default "false") Show this help
      --no-context        (default "false") omit bounded local evidence from help output
      --no-descriptions   (default "false") omit descriptions from completion candidates

Output:
  valid Zsh completion code only; it does not initialize compinit or enable completion by itself

Changes:
  none; no EVE state, startup files, or provider resources are modified

Notes:
  Zsh's completion system must be initialized before loading or autoloading the function.

Examples:
  eve completion setup --shell zsh
  source <(eve completion zsh)
  eve completion zsh > "$HOME/.zsh/completions/_eve"
```

## `eve help status`

```text
eve status — Show workspace records, allocations, public resource metadata, and recovery state.

Usage:
  eve status [workspace] [flags]

Arguments:
  workspace          Optional. Recorded branch in this repository, canonical UUID, or recorded absolute path. Omitted: the current EVE worktree.

Options:
  -h, --help              (default "false") Show this help
      --json              (default "false") emit the versioned JSON result
      --no-context        (default "false") omit bounded local evidence from help output
      --refresh           (default "false") include read-only provider identity checks

Reads:
  recorded repository/workspace metadata and bounded filesystem evidence

Preserves:
  local files, credentials, remote resources, and running applications

Examples:
  eve status feature/payments --json
```

The workspace argument is optional. A short UUID remains a completion expansion only; execution accepts the full canonical UUID, a branch from the invoking registered repository, or a recorded absolute path.
