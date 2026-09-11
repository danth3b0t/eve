# Uninstall EVE

**Do not delete the binary, state directory, or worktrees before assessing cloud resources.** Removing local files does **not** clean up deployments, deploy keys, or claimed local configuration.

## 1. Review outstanding resources

From an existing source checkout:

```sh
eve list --all
eve status <workspace> --json
eve gc
```

For every non-expired workspace:

1. Stop its project through the ordinary command.
2. Run `eve destroy <branch> --yes` from its registered repository.
3. If a workspace was manually deleted, use `eve gc --apply` only after the reported exact resources were reviewed.
4. Run `eve list --all` and `eve gc` again; the reports must be empty except destroyed tombstone history, which is not active state.

Never delete an `eve`-named resource directly through the Convex dashboard or by prefix matching without first verifying EVE's recorded exact reference/name and ownership.

## 2. Remove work content you no longer need

Destroying a workspace intentionally removes:

- its Git worktree and generated ignored files/caches;
- exact owned remote deployment/resource state;
- protected deployment key objects and local port claims.

Its canonical source checkout remains. Preexisting or divergent branches also remain; an unchanged EVE-created branch may be pruned only while it still equals the recorded creation target. Any application dependencies, source files and unowned external artifacts remain the user's responsibility.

## 3. Remove EVE state only after cleanup completes

State locations:

- Linux: `${XDG_STATE_HOME:-~/.local/state}/eve/`
- macOS: `~/Library/Application Support/eve/`

That directory contains the registry, locks, protected token profiles, private staged image snapshots, operation evidence and the machine HMAC key. Delete it only after all exact workspaces/resources are cleaned or provider TTL is independently understood. Deleting it earlier erases recovery/coordination evidence but does **not** delete remote resources.

User port configuration is at:

- Linux: `${XDG_CONFIG_HOME:-~/.config}/eve/config.toml`
- macOS: `~/Library/Application Support/eve/config.toml`

No EVE state is encrypted for disk storage. Destroying it does not protect credentials already visible to this OS user or in worktree files created by EVE.


## 4. Remove shell completion setup you approved

EVE only prints completion scripts; it never edits `.bashrc` or `.zshrc`. If you manually saved/source a generated script or placed it in a shell function path, remove that startup line or file. Before removal, `eve completion uninstall` does not run. EVE's completion failures remain inert when the executable is absent.
## 5. Remove the executable

Delete the downloaded `eve` binary after resources are clean. Reinstalling later is safe, but the registry cannot be reconstructed from source alone.

## 6. No automatic cleanup

EVE has no daemon or uninstall hook. Remote five-day expiration is only a provider fallback. Uninstalled binaries cannot inspect or delete resources.
