package cli

// Effect and example annotations for the executable command tree. Syntax,
// defaults and children intentionally come from registered Cobra metadata so
// this file cannot drift as a second grammar.

func createReference() commandMeta {
	return commandMeta{Purpose: "Create an owned Git worktree, reserved local allocations, exact cloud development resource, and generated native configuration.", Arguments: []argumentMeta{{Name: "branch", Requirement: "required", Description: "New or recorded branch selector resolved by creation policy"}}, Reads: []string{"committed eve.toml and target Git metadata", "recorded source identity and authorized credential profile metadata"}, Changes: []string{"possibly creates one branch and one worktree", "reserves local port/slot claims", "creates exact Convex development deployment and deployment-scoped key", "writes declared ignored/native destination keys", "records registry and ownership evidence"}, Preserves: []string{"canonical source checkout, unrelated branches and deployments", "project development defaults and unrelated environment values", "ordinary project scripts and runtime supervision", "management credential profiles"}, Examples: []string{"eve create --yes feature/payments", "eve create --from main --yes feature/review"}, Context: helpContextGeneral}
}

func planReference() commandMeta {
	return commandMeta{Purpose: "Evaluate the committed creation plan without creating state, ports, worktrees, credentials, or provider resources.", Arguments: []argumentMeta{{Name: "branch", Requirement: "required", Description: "Branch target evaluated against the committed source revision"}}, Reads: []string{"committed target manifest and bounded Git/file policy"}, Preserves: []string{"EVE state, provider resources, destination files, reservations and application processes"}, Examples: []string{"eve plan feature/payments"}, Context: helpContextGeneral}
}

func keysReference() commandMeta {
	return commandMeta{Purpose: "Show the interpolation variables supported by the current committed eve.toml.", Reads: []string{"committed manifest only"}, Preserves: []string{"provider remote, token files, dotenv values, and state objects"}, Examples: []string{"eve keys --json"}, Context: helpContextGeneral}
}

func pathReference() commandMeta {
	return selectorReference("Print the canonical path of one workspace.", "status, authentication, or destructive changes")
}

func statusReference() commandMeta {
	return selectorReference("Show workspace records, allocations, public resource metadata, and recovery state.", "local files, credentials, remote resources, and running applications")
}

func doctorReference() commandMeta {
	return selectorReference("Diagnose registry, Git, file ownership, port, and optional remote identity safety without launching the application.", "runtime health claims, credentials, files, cloud data, and recovery journals")
}

func selectorReference(purpose, preserves string) commandMeta {
	return commandMeta{Purpose: purpose, Arguments: []argumentMeta{{Name: "workspace", Requirement: "optional", Description: "Recorded branch in this repository, canonical UUID, or recorded absolute path", Omitted: "the current EVE worktree"}}, Reads: []string{"recorded repository/workspace metadata and bounded filesystem evidence"}, Preserves: []string{preserves}, Examples: []string{"eve status feature/payments --json"}, Context: helpContextSelected}
}

func resumeReference() commandMeta {
	return commandMeta{Purpose: "Continue an unfinished create, sync, destroy, or cleanup operation strictly from its durable journal.", Arguments: []argumentMeta{{Name: "workspace", Requirement: "optional", Description: "Recorded branch in this repository, canonical UUID, or recorded absolute path", Omitted: "the current EVE worktree"}}, Reads: []string{"frozen operation intent and current ownership evidence"}, Changes: []string{"may finish creation, synchronization, deletion, credential purge, or claim release for the recorded operation"}, Preserves: []string{"workspaces with unrelated operations, provider identity, and recovery evidence"}, Examples: []string{"eve resume feature/payments --json"}, Context: helpContextSelected}
}

func syncReference() commandMeta {
	return commandMeta{Purpose: "Apply supported committed manifest changes to an existing exact workspace/backend without replacing either.", Arguments: []argumentMeta{{Name: "workspace", Requirement: "optional", Description: "Recorded branch in this repository, canonical UUID, or recorded absolute path", Omitted: "the current EVE worktree"}}, Reads: []string{"current applied manifest, managed HMACs, and exact provider/web workspace identity"}, Changes: []string{"may update owned local keys, supported endpoint additions and declared remote values"}, Preserves: []string{"resource identity, unrelated unmanaged content, existing allocations, and immutable creation intent"}, Examples: []string{"eve sync feature/payments --dry-run", "eve sync feature/payments"}, Context: helpContextSelected}
}

func listReference() commandMeta {
	return commandMeta{Purpose: "List live recorded workspaces for this repository, or all repositories with --all.", Reads: []string{"existing registry records and public allocation/resource metadata"}, Preserves: []string{"working files, provider calls, credentials, app processes, and destroyed tombstones"}, Examples: []string{"eve list", "eve list --all --json"}, Context: helpContextGeneral}
}

func gcReference() commandMeta {
	return commandMeta{Purpose: "Report exact cleanup candidates after interrupted operations or raw local workspace loss. Bare gc never mutates; --apply performs approved exact cleanup in a visible scope.", Reads: []string{"recorded identities, expiry evidence, and filesystem absence/identity"}, Changes: []string{"--apply may delete exact Convex deployments, purge workspace credentials, remove retained Git admin metadata, and release claims"}, Preserves: []string{"existing worktrees found again, unreachable paths, unrelated resources, and divergent or preexisting branches"}, Examples: []string{"eve gc --json", "eve gc --workspace <uuid> --json", "eve gc --apply --json"}, Context: helpContextGeneral}
}

func destroyReference() commandMeta {
	return commandMeta{Purpose: "Delete one exact owned workspace’s local site, Convex deployment, deployment credential, and port claims after safety review.", Arguments: []argumentMeta{{Name: "workspace", Requirement: "optional", Description: "Recorded branch in this repository, canonical UUID, or recorded absolute path", Omitted: "the current EVE worktree"}}, Reads: []string{"recorded ownership, Git/worktree identity, local tracked changes, and provider/local state"}, Changes: []string{"deletes the exact development deployment and its recorded data when authorized", "removes the worktree and ignored files it contains and purges deployment credentials", "releases claims and records destruction history", "prunes only an unchanged EVE-created branch still at its starting target"}, Preserves: []string{"canonical source, project defaults, and management credentials", "pre-existing and divergent branches", "unrelated environments, workspaces, and port users"}, Examples: []string{"eve destroy feature/payments --dry-run --json", "eve destroy feature/payments --yes"}, Context: helpContextSelected}
}

func initReference() commandMeta {
	return commandMeta{Purpose: "Compile reviewed backend, consumer, and listener evidence into a committed v1 manifest, or update relations incrementally.", Reads: []string{"committed package/Git evidence and reviewed local destination key names"}, Changes: []string{"--write creates or atomically updates eve.toml; the user still commits it"}, Preserves: []string{"scripts, Git files, ignored customer values, provider resources, and credential storage"}, Examples: []string{"eve init --convex --project team:project --write --yes", "eve init --update --json"}, Context: helpContextGeneral}
}

func versionReference() commandMeta {
	return commandMeta{Purpose: "Show the compiled EVE version.", Preserves: []string{"repository, provider, credential, and user shell state"}, Examples: []string{"eve version", "eve --version"}, Context: helpContextNone}
}

func authReference() commandMeta {
	return commandMeta{Purpose: "Manage local provider credential profiles. Cloud account authentication and application login flows are separate.", Reads: []string{"nonsecret local profile metadata"}, Changes: []string{"login stores or reconciles one approved local profile secret", "logout removes local metadata/secret but never revokes cloud tokens or deletes deployments"}, Preserves: []string{"token values outside protected files, application account data, and provider ownership"}, Context: helpContextNone}
}

func loginReference() commandMeta {
	return commandMeta{Purpose: "Validate a team-scoped Convex access token against one explicit team:project binding and store it behind local profile metadata.", Reads: []string{"provider project/team identity and a hidden token input"}, Changes: []string{"writes the token object and public team profile metadata"}, Preserves: []string{"application secrets, code login flows, cloud deployments, and shell state"}, Examples: []string{"eve auth convex login --project team:project --token-stdin"}, Context: helpContextNone}
}

func authStatusReference() commandMeta {
	return commandMeta{Purpose: "Show nonsecret metadata for one local Convex credential profile without validating liveness or printing secrets.", Reads: []string{"local profile metadata only"}, Preserves: []string{"token objects and provider state"}, Examples: []string{"eve auth convex status --profile default"}, Context: helpContextNone}
}

func authLogoutReference() commandMeta {
	return commandMeta{Purpose: "Remove one local Convex credential profile and local secret. It does not revoke the team token or delete deployments.", Reads: []string{"local profile and reference metadata"}, Changes: []string{"removes local profile/secret only"}, Preserves: []string{"remote resources and other credential profiles"}, Examples: []string{"eve auth convex logout --profile default"}, Context: helpContextNone}
}

func completionReference() commandMeta {
	return commandMeta{Purpose: "Enable Tab completion for EVE's commands, options, and local workspace names.", Output: []string{"human setup guidance; use completion bash/zsh for raw shell code"}, Changes: []string{"none — No files or EVE state are changed by this command"}, Preserves: []string{"shell startup files, EVE state, credentials, and provider resources"}, Examples: []string{"eve completion setup", "eve completion bash > eve.bash", "eve completion zsh > _eve"}, Context: helpContextNone}
}

func completionBashReference() commandMeta {
	return commandMeta{Purpose: "Print EVE's Bash completion script.", Output: []string{"valid Bash completion code only; it does not enable completion by itself"}, Changes: []string{"none; no EVE state, startup files, or provider resources are modified"}, Notes: []string{"The compatible _get_comp_words_by_ref bash-completion helper must be loaded before use; see completion setup --shell bash."}, Examples: []string{"eve completion setup --shell bash", "source <(eve completion bash)", "eve completion bash > ${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions/eve"}, Context: helpContextNone}
}

func completionZshReference() commandMeta {
	return commandMeta{Purpose: "Print EVE's Zsh completion script.", Output: []string{"valid Zsh completion code only; it does not initialize compinit or enable completion by itself"}, Changes: []string{"none; no EVE state, startup files, or provider resources are modified"}, Examples: []string{"eve completion setup --shell zsh", "source <(eve completion zsh)", "eve completion zsh > \"$HOME/.zsh/completions/_eve\""}, Notes: []string{"Zsh's completion system must be initialized before loading or autoloading the function."}, Context: helpContextNone}
}

func completionSetupReference() commandMeta {
	return commandMeta{Purpose: "Show shell-specific setup, verification, troubleshooting, and removal instructions without editing startup files.", Output: []string{"human instructions or one versioned setup JSON envelope"}, Changes: []string{"none; printed commands are for the user to inspect and execute"}, Examples: []string{"eve completion setup", "eve completion setup --shell zsh", "eve completion setup --json"}, Context: helpContextNone}
}

func stateReference() commandMeta {
	return commandMeta{Purpose: "Explain recorded versus observed EVE state boundaries.", Preserves: []string{"state, providers, files, and credentials"}, Context: helpContextNone}
}

func cleanupReference() commandMeta {
	return commandMeta{Purpose: "Explain exact cleanup after manual removal.", Preserves: []string{"remote resources, worktrees, and cleanup authority"}, Context: helpContextNone}
}
