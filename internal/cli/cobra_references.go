package cli

import (
	"eve/internal/domain"
	"strings"
)

func createReference() commandMeta {
	return commandMeta{Short: "Create", Purpose: "Create an owned Git worktree, reserved local allocations, exact cloud development resource and generated native configuration.", Usage: []string{"create <branch> [--from <ref>] [--yes] [--json]"}, Reads: []string{"committed eve.toml and target Git metadata", "recorded source identity and authorized credential profile metadata"}, Changes: []string{"possibly creates one branch and one worktree", "reserves local port/slot claims", "creates exact Convex dev deployment and deployment-scoped key", "writes declared ignored/native destination keys", "journeys registry and ownership evidence"}, Preserves: []string{"canonical source checkout, unrelated branches and deployments", "project development defaults and unrelated env values", "ordinary project scripts loaded in-tree", "management credential profiles and runtime supervision policy"}, Examples: []string{"eve create --yes feature/payments", "eve create --from main --yes feature/review"}}
}

func planReference() commandMeta {
	return commandMeta{Short: "Plan", Purpose: "Evaluate the committed creation plan without creating state, ports, worktrees, credentials, or provider resources.", Usage: []string{"plan <branch> [--from <ref>] [--json]"}, Reads: []string{"committed target manifest and bounded Git/file policy"}, Preserves: []string{"EVE state, provider resources, destination files, reservations and app processes"}, Examples: []string{"eve plan feature/payments"}}
}

func keysReference() commandMeta {
	return commandMeta{Short: "Keys", Purpose: "Show the interpolation variables supported by the current committed eve.toml.", Usage: []string{"keys [--json]"}, Reads: []string{"committed manifest only"}, Preserves: []string{"provider remote, token files, dotenv values, and state objects"}, Examples: []string{"eve keys --json"}}
}

func inspectReference(name, purpose string) commandMeta {
	return commandMeta{Short: name, Purpose: purpose + ".", Usage: []string{strings.ToLower(name) + " [workspace] [--json]"}, Reads: []string{"local registry and bounded filesystem evidence"}, Preserves: []string{"worktrees, credentials, remote resources, and running applications"}}
}

func statusReference() commandMeta {
	return commandMeta{Short: "Status", Purpose: "Show workspace records, allocations, public resource metadata and recovery state.", Usage: []string{"status [workspace] [--refresh] [--json]"}, Reads: []string{"local registry; exact provider identity only when --refresh is explicit"}, Preserves: []string{"remote environments, local files, credentials, and running applications"}}
}

func resumeReference() commandMeta {
	return commandMeta{Short: "Resume", Purpose: "Continue an unfinished create, sync, destroy, or cleanup operation strictly from its durable journal.", Usage: []string{"resume <workspace> [--json]"}, Reads: []string{"frozen operation intent and current ownership evidence"}, Changes: []string{"May finish creation, synchronization, deletion, credential purge, or claim release for the recorded operation"}, Preserves: []string{"Workspaces with unrelated operations, provider identity and all recovery evidence"}, Examples: []string{"eve resume feature/payments --json"}}
}

func syncReference() commandMeta {
	return commandMeta{Short: "Sync", Purpose: "Apply supported committed manifest changes to an existing exact workspace/backend without replacing either.", Usage: []string{"sync <workspace> [--overwrite-managed] [--json]"}, Reads: []string{"current applied manifest, managed HMACs and exact provider/webworkspace identity"}, Changes: []string{"May update owned local keys, supported endpoint additions and declared remote env values"}, Preserves: []string{"resource identity, unrelated unmanaged content, existing allocations, and the immutable creation intent"}, Examples: []string{"eve sync feature/payments"}}
}

func listReference() commandMeta {
	return commandMeta{Short: "List", Purpose: "List live recorded workspaces for this repository, or all repositories with --all.", Usage: []string{"list [--all] [--json]"}, Reads: []string{"existing registry records and public allocation/resource metadata"}, Preserves: []string{"working files, provider calls, credentials, app processes, and destroyed tombstones"}}
}

func doctorReference() commandMeta {
	return commandMeta{Short: "Doctor", Purpose: "Diagnose registry, Git, file ownership, port and optional remote identity safety without launching the application.", Usage: []string{"doctor [workspace] [--remote] [--json]"}, Reads: []string{"local evidence and exact provider identities only when --remote is explicit"}, Preserves: []string{"runtime health claims, credentials, files, cloud data, and recovery journals"}}
}

func gcReference() commandMeta {
	return commandMeta{Short: "GC", Purpose: "Report exact cleanup candidates after interrupted operations or raw local workspace loss. Bare gc never mutates; --apply performs approved exact cleanup in a visible scope.", Usage: []string{"gc [--json]", "gc --workspace <id> [--apply] [--json]", "gc --apply [--json]"}, Reads: []string{"recorded identities, expiry evidence and filesystem absence/identity"}, Changes: []string{"--apply may delete exact Convex deployments, purge workspace credentials, remove retained Git admin metadata and release claims, across all registered repositories unless --workspace targets one"}, Preserves: []string{"existing worktrees found again, unreachable paths, unrelated resources, divergent or preexisting branches"}, Examples: []string{"eve gc --json", "eve gc --apply --json"}}
}

func destroyReference() commandMeta {
	return commandMeta{Short: "Destroy", Purpose: "Delete one exact owned workspace’s local site, Convex deployment, deployment credential and port claims after safety review.", Usage: []string{"destroy <workspace> [--yes] [--discard-changes] [--assume-stopped] [--dry-run] [--json]"}, Reads: []string{"recorded ownership, Git/worktree identity, local tracked changes and provider/local state"}, Changes: []string{"deletes the exact deployment and its data when authorized", "removes the worktree and ignored files it contains and purges deployment credentials", "releases claims and records destruction history", "prunes only an unchanged EVE-created branch still at its starting target"}, Preserves: []string{"canonical source, project defaults and management credentials", "pre-existing and divergent branches", "unrelated environments, workspaces, and port users"}, Examples: []string{"eve destroy feature/payments --yes", "eve destroy feature/payments --dry-run --json"}}
}

func initReference() commandMeta {
	return commandMeta{Short: "Init", Purpose: "Compile reviewed backend, consumer and listener evidence into a committed v1 manifest, or update relations incrementally.", Usage: []string{"init [--convex] [--project team:project] [--write --yes]", "init --update [--convex] [--write --yes]"}, Reads: []string{"committed package/git evidence and reviewed local destination key names"}, Changes: []string{"--write creates or atomically updates eve.toml; the user still commits it"}, Preserves: []string{"scripts, Git files, ignored customer values, provider resources, and credential storage"}, Examples: []string{"eve init --convex --project init-devs:es-staging --write --yes", "eve init --update --json"}}
}

func authReference() commandMeta {
	return commandMeta{Short: "Auth", Purpose: "Manage local provider credential profiles. Cloud authentication, account credentials, and application login flows are separate.", Usage: []string{"auth convex login|--status|logout", "Use auth <provider> <operation> --help for exact operands"}, Reads: []string{"profile metadata and provider identity during login"}, Changes: []string{"login stores or reconciles only an approved local profile secret", "logout removes local metadata/secures but never revokes cloud tokens or deletes deployments"}, Preserves: []string{"token values outside protected files, app account data, and provider ownership"}}
}

func loginReference() commandMeta {
	return commandMeta{Short: "Login", Purpose: "Validate a team-scoped Convex access token against one explicit team:project binding and store it behind local profile metadata.", Usage: []string{"auth convex login --project team:project [--profile name] [--token-stdin]"}, Reads: []string{"provider project/team identity and a hidden token input"}, Changes: []string{"writes the token object and public team profile metadata"}, Preserves: []string{"application secrets, code login flows, cloud deployments, and shell state"}}
}

func authLogoutReference() commandMeta {
	return commandMeta{Short: "Logout", Purpose: "Remove one local Convex credential profile and its local secret. It does not revoke the team token or delete deployments.", Usage: []string{"auth convex logout [--profile name]"}, Reads: []string{"local profile and reference metadata"}, Changes: []string{"removes local profile/secret only"}, Preserves: []string{"remote resources and other credential profiles"}}
}

func commandReferenceMap() map[string]commandRoute {
	routes := map[string]commandRoute{}
	for _, route := range commandRoutes() {
		routes[route.name] = route
	}
	routes["version"] = commandRoute{name: "version", meta: simpleReference("Version", "Show the compiled EVE version.", "No repository/provider/state effects.")}
	routes["help"] = commandRoute{name: "help", meta: simpleReference("Help", "Explain commands, evidence availability, and cleanup behavior.", "Help never opens writable state or contacts a provider.")}
	routes["completion"] = commandRoute{name: "completion", meta: simpleReference("Completion", "Generate a Bash or Zsh completion script.", "Printing a script creates no EVE state; caller redirection may write a shell startup file.")}
	routes["auth"] = commandRoute{name: "auth", meta: authReference()}
	return routes
}

func referenceForPath(path []string, options helpOptions) (*output, error) {
	catalog := commandReferenceMap()
	if len(path) == 0 {
		return generalHelpResult(options), nil
	}
	switch path[0] {
	case "auth":
		if len(path) == 1 {
			return referenceOutput("auth", authReference()), nil
		}
		if len(path) == 2 && path[1] == "convex" {
			return referenceOutput("auth convex", authReference()), nil
		}
		if len(path) == 3 && path[1] == "convex" {
			switch path[2] {
			case "login":
				return referenceOutput("auth convex login", loginReference()), nil
			case "status":
				return referenceOutput("auth convex status", simpleReference("Credential status", "Show profile metadata without executing liveness checks or printing secrets.", "Read-only metadata operation.")), nil
			case "logout":
				return referenceOutput("auth convex logout", authLogoutReference()), nil
			}
		}
		return nil, &domain.Error{Code: "E_USAGE", Message: "unknown auth help topic"}
	case "state", "cleanup":
		if route := catalog[path[0]]; len(path) == 1 && route.handler != nil {
			return route.handler(nil, nil)
		}
		return nil, &domain.Error{Code: "E_USAGE", Message: "unknown help topic " + strings.Join(path, " ")}
	default:
		route, ok := catalog[path[0]]
		if !ok || len(path) != 1 {
			return nil, &domain.Error{Code: "E_USAGE", Message: "unknown help topic " + strings.Join(path, " ")}
		}
		return referenceOutput(path[0], route.meta), nil
	}
}

func referenceOutput(name string, meta commandMeta) *output {
	return &output{SchemaVersion: 1, Command: "help " + name, OK: true, Human: renderCommandReference(commandMeta{Short: name, Purpose: meta.Purpose, Usage: meta.Usage, Examples: meta.Examples, Reads: meta.Reads, Changes: meta.Changes, Preserves: meta.Preserves})}
}
