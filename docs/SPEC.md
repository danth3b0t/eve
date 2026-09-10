# EVE — Implementation Specification

**Version:** 0.1 · **Status:** proposed implementation contract  
**Scope:** standalone CLI, Git worktrees, native environment files, Convex cloud  
**Target systems:** Linux and macOS, amd64 and arm64  
**Supported release scope today:** Linux/amd64 only; Linux arm64 and macOS artifacts are preview builds until physical-host validation passes
**Evidence reviewed:** September 9, 2026

> EVE provisions a disposable, connected development worktree and writes ordinary configuration. The project then runs without EVE, or an EVE-specific launcher.

The words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** express requirements. Unqualified defaults are decisions for this release, not questions for the implementer to resolve. Examples use fictitious provider identifiers and URLs.

## Contents

1. Product contract and boundaries
2. Architecture and implementation stack
3. User workflow and CLI
4. Configuration model
5. Resolution and validation
6. Discovery and onboarding
7. Git and filesystem ownership
8. Port allocation
9. Native environment-file materialization
10. Existing launchers and compatibility
11. Convex cloud adapter
12. Provider interface and HTTP behavior
13. Provisioning algorithm
14. Synchronization and recovery
15. Destruction and garbage collection
16. State and concurrency
17. Credentials and security
18. Diagnostics and machine-readable output
19. Internal packages and interfaces
20. Testing requirements
21. Delivery milestones
22. Release gates and remaining engineering validation
23. Deferred scope
24. Decision record and definition of done
25. Sources

---

## 1. Product contract and boundaries

### 1.1 Goal

Starting from a repository that already has working native development commands, create another checkout with independent local ports and a fresh, correctly selected Convex cloud backend.

```text
reviewed eve.toml + existing local configuration
                    │
                    ▼
              eve create <branch>
                    │
        ┌───────────┼─────────────────┐
        ▼           ▼                 ▼
    Git worktree  port allocation  Convex deployment
        └───────────┼─────────────────┘
                    ▼
        materialize native configuration
                    ▼
                 EVE exits
                    ▼
     bun run dev / another existing command
```

### 1.2 Required properties

| ID | Requirement |
|---|---|
| R01 | EVE is distributed as a standalone executable. Git is its only required external executable. |
| R02 | Existing `package.json` scripts, `turbo.json`, application source, lockfiles, and launch commands remain unchanged. |
| R03 | EVE doesn't participate in normal application startup. No shell activation, wrapper, injected preload, or generated task runner is required. |
| R04 | Configuration is written only to declared destinations inside the disposable worktree. Canonical checkout configuration is not mutated during create/sync/destroy. |
| R05 | One Convex **cloud development deployment** is created per declared Convex resource per workspace. No local Convex support initially. |
| R06 | Convex development defaults supply backend secrets. EVE does not implement application secret synchronization. |
| R07 | Each created Convex deployment expires after five days by default. Local worktrees do not expire. |
| R08 | Failed or interrupted operations remain inspectable and recoverable. Resource ownership must survive loss of the worktree directory. |
| R09 | The CLI works in any terminal, including an ordinary tmux or Herdr pane. Neither has a native integration in this release. |
| R10 | Setup decisions are explicit and reviewed. Discovery proposes configuration; it never independently authorizes provisioning. |
| R11 | All applications can run after EVE has exited, even if the EVE executable is subsequently removed. Their ordinary project dependencies remain necessary. |

### 1.3 What “prepared” means

`prepared` means the worktree exists, allocations are recorded, provider resources are verified, and all declared configuration destinations contain the resolved values.

It does **not** mean dependencies are installed, backend code has been pushed, data has been seeded, a process is running, or login has been tested. Status must distinguish these facts rather than call the application “healthy.”

The existing project command normally runs `convex dev`, which performs code deployment/watching. If a repository does not start its backend this way, its ordinary setup remains the user's or agent's responsibility. EVE does not invent those commands.

### 1.4 Non-goals

No process supervisor, task runner, package installer, deployment platform, container runtime, browser automation, auth-library adapter, account provisioning, automatic data seeding, proxy/DNS/TLS manager, cloud project creation, or general secrets manager. No implicit fetching, cloning, PR lookup, or code execution during discovery.

A worktree is not a sandbox against code or agents running as the same OS user. Separate worktrees and deployments reduce accidental interference; they do not create OS-level security boundaries.

## 2. Architecture and implementation stack

### 2.1 Responsibilities

| Layer | Owns | Does not own |
|---|---|---|
| Core | Workspace identity, Git lifecycle, allocations, resolution, files, durable state, diagnostics | Framework startup semantics or backend schema compilation |
| Native dotenv writer | Lossless edits of declared environment keys | Global shell environment or arbitrary configuration languages |
| Convex adapter | Project resolution, cloud deployment/key lifecycle, deployment selection, declared backend settings | Account tables, authentication libraries, seeding, project defaults |
| Existing project tooling | Dependency setup, code push/watch, build/dev commands | EVE's ownership registry |
| Terminal/multiplexer | User sessions and process interaction | Required EVE lifecycle integration |

### 2.2 Implementation choices

Use **Go**, with `net/http`, `os/exec`, filesystem APIs, and a small command layer. Use `github.com/pelletier/go-toml/v2` for TOML and `database/sql` with `modernc.org/sqlite` for embedded state. The latter is CGo-free; no SQLite executable or server is required. [S17, S18]

Build `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64` artifacts. Pin the Go toolchain and module versions in source control when implementation starts; do not dynamically select dependency versions at runtime. The reference release build uses `CGO_ENABLED=0`; verify every target artifact rather than assuming portability from compilation alone.

No dependency on installed Node, Bun, Python, mise, Docker, Convex CLI, curl, jq, lsof, or a credential-helper executable. Such tools may be present because the **project** needs them; core provisioning must not invoke them.

Ship a built-in Convex adapter through an internal Go interface. Do not implement dynamic plugin loading or an extension marketplace.

### 2.3 Data format choices

- Project manifest: `eve.toml`, versioned and committed.
- Runtime output: existing dotenv files, not an EVE-specific env format.
- Local state: SQLite outside repositories.
- Credentials and temporary sensitive file images: owner-only files outside repositories; not plaintext values in ordinary database rows.
- Automation output: versioned JSON envelopes on stdout.

## 3. User workflow and CLI

### 3.1 Onboarding

```sh
eve init --dry-run        # static discovery; no cloud writes or scripts
eve init                 # review and write eve.toml
# Review and commit eve.toml using normal Git commands.
eve auth convex login    # register a team token outside the repository
```

`init` does not install dependencies or edit package scripts. It may propose necessary ignore entries, but does not silently change `.gitignore`. The reviewed configuration must exist in the target revision before `create`; a missing/uncommitted manifest produces an actionable error.

### 3.2 Everyday use

```sh
eve create feature/payments
cd "$(eve path feature/payments)"

# Follow the project's existing install/setup procedure when needed.
bun run dev

# Stop the project through its ordinary mechanism, usually Ctrl-C.
eve destroy feature/payments
```

An executable cannot change its parent shell's working directory. `create` prints the path and `path` prints only the path. No shell function is required.

### 3.3 Command contract

| Command | Contract |
|---|---|
| `eve init [--dry-run]` | Inspect the invoking source checkout. Propose/write the manifest after review. No provider provisioning or executable config evaluation. |
| `eve plan <branch> [--from <ref>]` | Validate a creation plan. No worktree, allocation, token, deployment, or application-file creation. Ports are prospective, not reserved. |
| `eve create <branch> [--from <ref>] [--yes]` | Create an owned workspace from an existing branch or create the branch from the selected base. Print summary and path. |
| `eve path [<workspace>]` | Print canonical workspace path. Omitted workspace means current EVE worktree. |
| `eve list [--all]` | List this repository's workspaces; `--all` lists this user's registered repositories. Local-state read only. |
| `eve status [<workspace>] [--refresh]` | Show allocations, configuration generation, provider IDs/expiry, and failures. Network only with `--refresh`. |
| `eve doctor [<workspace>] [--remote]` | Diagnose files, configuration, ambient selector conflicts, and port observations. `--remote` adds read-only provider checks. Never launches the app. |
| `eve resume [<workspace>]` | Continue an interrupted create/sync/destroy operation using its frozen intent. Never silently recreate a missing deployment. |
| `eve sync [<workspace>] [--overwrite-managed]` | Re-resolve supported manifest changes and reapply owned values while preserving allocations and deployment identity. See §14. |
| `eve destroy <workspace> [--yes] [--discard-changes] [--assume-stopped]` | Remove exact owned remote resources and worktree, preserving the Git branch. Safety flags have distinct meanings. |
| `eve gc [--apply]` | Default is a cleanup report. With `--apply`, retry authorized cleanup and remove exact owned orphan resources whose local workspace is gone. Never remove existing worktrees merely because a deployment expired. |
| `eve auth convex login [--profile <name>] [--token-stdin]` | Validate and store a team token. Without stdin mode, use a hidden interactive prompt. |
| `eve auth convex status/logout [--profile <name>]` | Inspect credential metadata or remove the local credential. Logout does not delete resources or revoke a provider token. Warn when cleanup still needs it. |

All ordinary commands accept `--json` except `path`, which deliberately remains a shell-friendly scalar. Global `--version` includes build version, Git commit, state schema version, and adapter contract version.

A workspace selector is a full UUID, an unambiguous UUID prefix, a recorded path, or a branch label within the current repository. Never resolve ambiguous names by “most recently used.” Cross-repository destructive operations require UUID/path or an explicit repository selection.

`--yes` accepts the presented plan; it does not bypass path, credential, ownership, tracked-file, or dirty-worktree protections. In noninteractive mode a required confirmation fails unless the corresponding explicit authorization flag is present.

## 4. Configuration model

### 4.1 Minimal example

```toml
version = 1

[services.web]
path = "apps/web"
env_file = ".env.local"
port = "PORT"

[services.web.env]
NEXT_PUBLIC_CONVEX_URL = "${resources.backend.url}"
NEXT_PUBLIC_CONVEX_SITE_URL = "${resources.backend.site_url}"

[resources.backend]
provider = "convex"
path = "packages/backend"
project = "my-team:my-project"
# env_file defaults to .env.local; ttl defaults to 5d.

# Optional non-secret configuration, only if this project uses it:
[resources.backend.env]
SITE_URL = "${services.web.url}"
```

There are no command, package-manager, auth-library, pane, seed, or runtime-version fields.

### 4.2 Full schema vocabulary

The bundled `eve.schema.json` describes the parsed TOML object. The semantic rules in this document remain authoritative for paths, references, ownership, and compatibility.

| Field | Type/default | Meaning |
|---|---|---|
| `version` | required integer `1` | Manifest format; reject unsupported versions. |
| `workspace.copy` | string array, `[]` | Additional repository-relative files to copy. Supports bounded `*`, `?`, and `**` glob matching; see §7. |
| `workspace.port_block_size` | integer, `100` | Number of TCP ports claimed for a workspace with listeners. Range 1–1000. |
| `services.<id>.path` | required relative path | Existing service directory in the target checkout. `.` is valid. |
| `services.<id>.env_file` | required relative path | Native dotenv destination relative to the service directory; may use `..` only when the normalized destination remains inside the repository. |
| `services.<id>.port` | optional env key | Allocate a primary TCP port and write it to this key. Omit for a non-listening configuration consumer. |
| `services.<id>.host` | `localhost` | URL presentation host: `localhost`, `127.0.0.1`, or `::1`. Does not configure the process's bind address. |
| `services.<id>.scheme` | `http` | `http` or `https` for URL construction. Choosing HTTPS does not provision certificates. |
| `services.<id>.allow_tracked` | boolean, `false` | Explicit consent to edit a tracked destination in the disposable worktree. Never permits credential output into tracked files. |
| `services.<id>.env` | map of strings, `{}` | Additional variables, literal or interpolated. |
| `services.<id>.ports.<name>.env` | required env key | Optional additional TCP listener, such as HMR or a debug server. |
| `resources.<id>.provider` | required `convex` | Only provider supported in v0.1. |
| `resources.<id>.path` | required relative path | Convex project/CLI working directory, not necessarily its functions directory. |
| `resources.<id>.project` | required `team:project` | Explicit existing cloud project binding. No implicit project creation. |
| `resources.<id>.env_file` | `.env.local` | Native backend selection/credential file, relative to resource path. |
| `resources.<id>.credential_profile` | `default` | User-local management credential alias; never a token literal. |
| `resources.<id>.ttl` | `5d` | Positive duration using one `m`, `h`, or `d` suffix. Finite; provider limits also apply. |
| `resources.<id>.region` | absent | Optional explicit region; absent means provider default. |
| `resources.<id>.env` | map of strings, `{}` | Explicit non-secret deployment-side overrides, e.g. a frontend URL. |

Service/resource/extra-port identifiers match `[a-z][a-z0-9_-]{0,47}`. `primary` is reserved as the internal primary endpoint name. Environment keys match `[A-Za-z_][A-Za-z0-9_]*`.

At least one service or resource must exist. Unknown keys are errors, including misspellings. Duplicate TOML keys are errors. No includes, shell expansion, executable configuration, or arbitrary template functions.

### 4.3 Defaults that reduce configuration

The destination file of each service/resource is automatically a **candidate copy source** at the same repository-relative path in the registered source checkout. Existing untracked files are copied into the worktree staging area before their owned keys are changed. Missing files are created with only declared keys. Therefore most repositories need no `workspace.copy` list.

Only the selected destination is automatically copied, not every `.env*` sibling. Declare additional inputs in `workspace.copy` when the existing application also loads them. Copy lists are displayed during planning; filenames and key names are enough, secret values are not printed.

### 4.4 Shared root env file

Several bindings may target one root file, provided they write nonconflicting keys:

```toml
version = 1

[services.web]
path = "apps/web"
env_file = "../../.env.local"
port = "WEB_PORT"

[services.admin]
path = "apps/admin"
env_file = "../../.env.local"
port = "ADMIN_PORT"
```

This supports a project that **already consumes** `WEB_PORT` and `ADMIN_PORT`. It does not teach a project that only reads `PORT` to do so. Two services requiring different `PORT` values cannot share a single flat env file under that same key.

### 4.5 User-local configuration

The optional user configuration is deliberately separate from the repository manifest:

```toml
version = 1

[ports]
min = 20000
max = 49999
```

These are the only user-config fields in v0.1. Validate `1 <= min <= max <= 65535`; reject unknown keys. Credential profiles are managed by `eve auth`, not token strings in this TOML file. Repository/source registrations belong to the state database. An advanced `EVE_STATE_DIR` environment override can select a separate state root for tests or isolated use; warn that different state roots do not coordinate port claims. It cannot redirect or override the canonical source checkout recorded in an existing registry.

## 5. Resolution and validation

### 5.1 Available references

| Reference | Value |
|---|---|
| `${workspace.id}` | Stable UUID generated once. |
| `${workspace.slug}` | Filesystem/env-safe display slug derived once from label and ID. |
| `${workspace.branch}` | Recorded branch label; final native writer still validates representability. |
| `${workspace.port_base}` | First claimed port; unavailable when no listeners exist. |
| `${services.<id>.port}` | Primary endpoint port, decimal string. |
| `${services.<id>.url}` | Primary URL, including port, no trailing slash. |
| `${services.<id>.ports.<name>.port}` | Additional named endpoint. |
| `${resources.<id>.url}` | Convex client/deployment URL. |
| `${resources.<id>.site_url}` | Convex HTTP Actions origin. |
| `${resources.<id>.deployment}` | Native deployment selector, not a credential. |
| `${resources.<id>.name}` | Provider deployment name. |
| `${resources.<id>.reference}` | Recorded EVE-specific provider reference. |

Credentials are **not** expression outputs. In particular, `${resources.backend.deploy_key}` is invalid. The Convex adapter writes its key through a private credential binding that cannot be redirected to a frontend env mapping.

Interpolate only this vocabulary. `$${` escapes a literal `${`. No access to arbitrary process env, files, commands, network, or secret stores. Referencing another `env` assignment is not supported; reference the resource/endpoint directly. Every result is a string. Unresolved references fail, never become empty strings.

### 5.2 Evaluation phases

1. Parse and validate manifest, destinations, aliases, and provider bindings.
2. Allocate local endpoints and resolve local workspace/service values.
3. Provision remote resources, obtaining provider outputs.
4. Resolve all local and remote configuration values.
5. Validate destination conflicts and safe serialization; publish only when the whole plan is valid.

A backend `SITE_URL` referring to the frontend, while the frontend refers to the backend URL, is not a creation cycle. Both resources exist before their configuration is applied. Remote resource creation cannot depend on another resource's env values in v0.1.

### 5.3 Collision and scope rules

Canonicalize every destination before merging writes. A destination/key may have multiple owners only if the normalized desired value is identical; otherwise fail with all conflicting source locations. A provider-reserved selector/credential key cannot be overridden by a user env mapping.

Fail on secrets assigned to public frontend namespaces through provider output bindings. Public resource URLs and opaque IDs are allowed. The manifest is not a safe location for application secrets; secret interpolation/upload features are outside v0.1.

`resources.*.env` must not override provider system variables such as `CONVEX_CLOUD_URL` or `CONVEX_SITE_URL`. Those are provider-owned. Explicit ordinary application URL keys are permitted. [S06]

## 6. Discovery and onboarding

### 6.1 Discovery is a configuration authoring aid

`init` proposes services, candidate destinations, resource owners, and relationships with evidence. It never provisions resources and never considers dependency-installation location authoritative.

Installing `convex` can simply mean a package is a frontend client. Backend functions may live beside that frontend or in another workspace. The functions directory can be configured. Convex components are not automatically independent deployments. [S11, S12, S13]

### 6.2 Static inspection

Read declared workspaces from package metadata and workspace files, plus relevant repository paths. Inspect package scripts as text, Convex configuration, backend-source patterns, native env key names, and obvious localhost references. Use Git's tracked-file inventory plus narrowly selected local configuration files.

Exclude `.git`, dependencies, build output, generated API trees as ownership evidence, caches, and local backend data. Never recursively scan `node_modules` or execute TS/JS configuration. Discovery must work before dependencies are installed.

Classify findings as **explicit**, **strong candidate**, **ambiguous**, or **client-only**; show the evidence rather than invented numerical confidence.

| Evidence | Interpretation |
|---|---|
| Convex dependency/import | Usage only. |
| Native public Convex URL key | Potential consumer. |
| Backend functions plus a `convex dev` script | Strong backend-owner candidate. |
| `convex.json` with validated functions directory | Strong owner/path evidence. |
| Generated API import from another package | Potential consumer-to-owner link. |
| Multiple apparent backend roots | Require explicit selection or separate resource declarations. |
| Component definition | Component candidate; do not create a resource for it. |

`convex/` or `schema.ts` alone is not sufficient and neither is universally required. Ambiguous env references, duplicate default ports, and unknown loaders must remain unresolved in the proposed plan.

### 6.3 Review and authority

`init --dry-run` does not write a manifest, register credentials, or reserve ports. Interactive `init` writes only the reviewed manifest and local source-checkout registration. Noninteractive `init` requires complete explicit inputs; it must not invent a team/project.

On subsequent `create`, use the **target revision's committed manifest**. If the target branch has different resource bindings or copy paths, show those differences for review. Never copy a current branch's different manifest over the target branch to make it “work.”

The first release may discover fewer unusual layouts than it supports through explicit configuration. Correct manual declarations are preferable to false automatic confidence.

## 7. Git and filesystem ownership

### 7.1 Repository and source identity

Resolve the canonical Git common directory and register a random repository UUID in EVE state. All linked worktrees of that checkout family share it. A separate clone is a different local repository, even with the same remote URL.

The source checkout is explicitly registered by `init`; its local files are the source of developer configuration. Creating a workspace from inside another EVE worktree must still use that registered source, not recursively copy the previous ephemeral deployment's configuration. If the source moved or disappeared, fail with a re-registration instruction.

### 7.2 Branch rules

Existing branch: use its current commit without resetting it. New branch: create from `--from`, or the registered source checkout's `HEAD`. Resolve the exact commit before mutation. Do not implicitly fetch remote refs, choose an upstream, or copy uncommitted application code.

Let Git reject a branch already checked out elsewhere. Validate branch names with Git and pass arguments without a shell. Branch names are never used directly as directory paths. Pre-existing branches are preserved. A branch recorded as EVE-created is pruned only from durable creation intent, after its branch name and current tip still match that recorded target; a changed tip or absent branch is diagnostic evidence, not replacement authorization. Worktree tombstones retain branch/path/generation identity for audit. [S01]

Require a normal, non-bare repository and initialized `HEAD`. Sparse checkout/submodule orchestration is outside the initial compatibility guarantee; diagnose it before cloud provisioning rather than silently generating an incomplete environment.

### 7.3 Workspace path

Default to a sibling root:

```text
<source-parent>/.eve-worktrees/<repo-label>-<repo-id8>/<branch-slug>-<workspace-id8>
```

The leaf must not exist. Never overwrite or adopt an unrelated directory. Store the full random UUID independently of the human-readable path. Case-insensitive collisions on macOS must be checked using filesystem identity, not only string comparison.

EVE does not support attaching arbitrary externally created worktrees in v0.1. That avoids unclear directory ownership; an attach mode can be added separately.

### 7.4 Copies and destinations

All file paths must normalize inside the source/target repository roots. Inspect every component without following escaping symlinks. Reject symlink/hardlink destinations and non-regular source files; do not write through links into a canonical checkout. Do not copy `.git`, EVE state, installed dependency trees, or `.convex` local backend state.

`workspace.copy` globs are repository-relative, slash-separated, case-sensitive patterns over regular files. `*` and `?` do not cross `/`; `**` does. Dotfiles participate. No brace expansion, negation, shell expansion, or directories as copy units. Exact missing paths are errors; a glob matching nothing warns. Cap expansion at 10,000 files and total copies at 256 MiB; exceeding the cap requires narrowing the manifest, not silent truncation.

If a selected source file is tracked, use the target revision's copy rather than overwrite it with the source checkout's working copy. A missing tracked counterpart in the target is an explicit conflict. Local, untracked source files are copied byte-for-byte before managed edits. Unselected files stay untouched.

Do not copy active provider selectors verbatim into a runnable destination before rebinding: prepare final file images in staging, then publish them (§9). A user must not start the half-created worktree before `create` succeeds; without a runtime wrapper EVE cannot physically enforce that.

### 7.5 Tracking and ignore policy

New application config containing local values must already be ignored by repository/global rules; verify using Git. If it is not ignored, stop and recommend the exact ignore entry. EVE does not silently change common Git exclude configuration or hide tracked edits using `skip-worktree`/`assume-unchanged`.

Editing a tracked native config file requires `allow_tracked = true` on every binding targeting it. This deliberately produces a visible worktree diff. Provider credentials may **never** be written to tracked files, even with that flag.

Canonical application files, common Git config, global Git config, and the user's source env files must remain unchanged during workspace lifecycle operations. Git's own worktree metadata changes are expected. EVE disables Git hooks for its worktree mutation commands through a temporary empty hooks directory. User-configured Git filters can still execute through normal Git checkout behavior; this is not an untrusted-repository sandbox.

## 8. Port allocation

### 8.1 Guarantee and defaults

Use a host-local, **per-user** registry shared across EVE-managed repositories. It prevents overlapping allocations among cooperating EVE processes using the same state directory. It is not a machine-wide allocator across different OS users, containers, or separate EVE state directories.

User-local defaults: inclusive TCP range `20000–49999`; workspace block size `100`. The range can be changed in the user's EVE configuration, not by an untrusted repository. Allocate no block for a workspace with no local endpoints.

A block is an allocator reservation, not an OS socket lease. EVE exits and cannot prevent unrelated software from binding a selected port afterward.

### 8.2 Algorithm

1. Validate the requested block fits the configured range and all endpoint slots fit the block.
2. In a short SQLite transaction, select a nonoverlapping candidate and insert a claim for every port in the block.
3. Probe every candidate port using exclusive IPv4 wildcard and, where supported, IPv6-only wildcard TCP binds, without reuse-port behavior. Release probe sockets immediately afterward.
4. On failure, release that candidate's claims transactionally and try another. On success, persist the selected block and endpoint slots.
5. Re-probe assigned endpoints immediately before final publication. If a new conflict appears, leave the operation resumable and unprepared; never silently change a resource's URL after remote configuration has been applied.

URL rendering must bracket IPv6 literals correctly; `::1` produces `http://[::1]:<port>`, not an ambiguous unbracketed address. Initial endpoints are sorted by `(service_id, endpoint_name)` and assigned zero-based offsets. Persist those offsets. During supported `sync`, new endpoints use unused slots; existing endpoints do not move. No in-place block resizing/reallocation in v0.1; report exhaustion clearly.

Validate `1 <= base + offset <= 65535`. Concurrent allocators must not rely only on a preceding read; enforce unique port claims in SQLite.

### 8.3 Process assumptions

Listening services must use their configured port without silently falling back. EVE does not change framework flags to enforce this. `doctor` reports known fallback behavior or observed conflicts but does not kill listeners. UDP-only allocation and browser routing are deferred.

## 9. Native environment-file materialization

### 9.1 Ownership is per key, not per file

EVE owns only the variables declared by the manifest plus the Convex adapter's reserved bindings. Other entries, comments, formatting, and developer secrets belong to the project/user.

Example source file:

```dotenv
# Developer settings
STRIPE_SECRET_KEY=existing-secret
PORT=3000 # frontend
NEXT_PUBLIC_CONVEX_URL=https://original.convex.cloud
```

Example worktree result:

```dotenv
# Developer settings
STRIPE_SECRET_KEY=existing-secret
PORT=32400 # frontend
NEXT_PUBLIC_CONVEX_URL=https://workspace.convex.cloud
```

No global search-and-replace of port numbers, hosts, or URLs. A literal `3000` elsewhere is unrelated unless declared.

### 9.2 Parser/writer contract

Build a bounded, lossless dotenv document parser retaining assignment spans, comments, line endings, and uninterpreted text. It must understand assignment boundaries for optional `export`, whitespace, single/double-quoted values, backtick-quoted input, multiline quoted input, blank lines, and inline comments. Never evaluate variable expansion or command substitution.

Only a targeted assignment's value span is replaced. Preserve unrelated bytes exactly. Append missing keys in stable key order at the end using the file's line-ending convention. An empty/new file uses LF. Do not create duplicate keys.

A targeted key with multiple definitions, malformed quoting, or an ambiguous boundary is an error with path and line numbers. Malformed unrelated text is also an error if the parser cannot prove where assignments end; do not fall back to line-oriented replacement.

For v0.1, EVE-generated **local dotenv values** use a deliberately portable subset: single-line printable ASCII without whitespace, quotes, backticks, backslash, `$`, or `#`. Empty values are allowed. Emit these as unquoted `KEY=value`. This covers ordinary ports, UUIDs, standard URLs, and Convex deployment selectors/keys. More complex generated values fail with `E_ENV_SERIALIZATION`; they are not silently escaped according to one loader's rules. Unmanaged values may still use the original project's richer dotenv syntax and remain byte-preserved.

Do not assume a conventional parse-to-map/serialize library preserves comments, duplicate definitions, interpolation, or formatting. The lossless contract requires fixtures and fuzzing. Maximum dotenv input size is 2 MiB per file; reject NUL bytes and invalid UTF-8.

### 9.3 Atomic file writes and multi-file transactions

All final file images must be prepared and validated before publishing any of them. Use a journaled file transaction:

1. Record each destination, original-content HMAC, staged-content HMAC, permissions, and protected preimage/staged-image references.
2. Create a `0600` temporary file in the destination's parent directory; use exclusive creation and reject link races.
3. Write, flush, and sync it; recheck destination identity/content before replacement.
4. Rename atomically over the destination and sync the parent directory where supported.
5. Record that destination as committed. After every destination succeeds, commit the configuration generation and `prepared` state.
6. Remove temporary images/backups after the operation is durably complete.

A collection of files is **not atomically replaceable as a unit**. EVE's journal and status make partial publication recoverable; they do not prevent an application started concurrently from seeing mixed versions. Users must stop the project before `sync` and wait for successful creation before initial startup.

File modes must never become broader than owner read/write for files containing copied configuration or credentials. Credentials and sensitive file images must not appear in SQL journals, stdout, or error payloads.

### 9.4 Drift policy

Record a keyed HMAC of each last-applied managed value and the complete last-published file. On sync:

- Unmanaged additions/edits are preserved.
- Managed value unchanged since last EVE application: update it normally.
- Managed value edited externally: fail with `E_MANAGED_VALUE_CHANGED` unless the user explicitly supplies `--overwrite-managed`.
- A wholly replaced/missing file is diagnosed; missing files can be reconstructed from current untracked source config only with a reviewed plan, not silently assumed equivalent.
- A destination that became tracked, linked, or moved outside the worktree fails again even if previously valid.

The overwrite flag permits only declared owned-key changes. It never authorizes replacing unrelated content. Persistent plaintext copies of prior secrets are unnecessary for normal drift checks.

### 9.5 Removal and retargeting limits

In v0.1 `sync` does not remove an existing managed key, change its destination, or relinquish its ownership automatically. These changes fail with a migration diagnostic rather than guessing whether to restore an old developer value. Retargeting a Convex resource to another project, changing its owner path, removing/adding cloud resources, or changing primary endpoint identity requires a new workspace. Additive local env keys/endpoints and changed values against the same resources are supported.

These constraints keep safe synchronization small. More complex ownership migrations are a later feature, not a hidden implementation requirement.

## 10. Existing launchers and compatibility

### 10.1 Configuration supply is not configuration consumption

EVE never modifies `package.json`, `turbo.json`, `bunfig.toml`, framework config, or source to make a setting work. It does not add `eve run`, shell sourcing, preload modules, or environment-injection scripts.

A project is compatible when its **existing** launch path reads the selected file/key at the correct time. EVE declares that path and supplies the values. File existence alone is not evidence of consumption.

Bun documents automatic dotenv loading. Turbo has its own environment filtering and does not itself load dotenv files. Next.js's own dotenv loading does not select the listening port. These are separate behaviors that must be verified together for the supported fixture. [S02, S03, S04]

### 10.2 Root Bun/Turbo support

The principal release fixture must use the project's ordinary form:

```text
bun run dev
  └── turbo run dev
       ├── existing web package dev script
       └── existing backend package convex dev script
```

Its scripts are established **before** EVE is applied. Creating an environment must not change their hashes. Actual child values/listening ports and selected backend must be verified, not inferred from the env files.

Particularly test:

- Turbo strict mode versus any existing pass-through declarations.
- Bun invoked at the repository root and separately at package boundaries.
- Higher-precedence process variables and root dotenv values masking leaf values.
- Framework-specific URL prefixes and port consumption.
- `--env-file` arguments or disabled automatic loading already present in scripts.
- Shared-root dotenv setups using service-specific key names.
- Build/cache inputs when dev startup depends on cached preparation artifacts.

EVE does not set `--env-mode=loose`, change cache settings, or copy every variable to the root as a workaround. A root generic `PORT` cannot represent multiple listening services.

### 10.3 Honest compatibility diagnostics

Discovery may report an obvious incompatible command such as a hardcoded port. Otherwise, status records runtime verification as `not_checked`. `doctor` may identify suspect loaders/precedence but must not pretend static analysis proves arbitrary shell command behavior.

If a fixture cannot use existing files without command changes, it is unsupported under the current contract. Report the precise incompatibility; do not solve it by silently relaxing R02/R03. The first milestone exists to establish a working representative launch path before building a broad CLI.

Mise may already be used by the developer. EVE leaves it alone and warns when detectable mise/shell values conflict with managed keys. It cannot remove higher-precedence variables from a later unrelated shell. The same limitation applies to explicit CLI flags and subsequent manual edits.

### 10.4 Process lifetime

Changing an env file does not update an existing process or compiled frontend artifact. `sync` always reports `restart_required=true` when applied values changed. EVE never restarts the root command or attaches to its processes.

## 11. Convex cloud adapter

### 11.1 Supported resource and authorization

Provision an additional, nondefault **cloud dev deployment** in the manifest's existing Convex project. Never create a project, replace a default deployment, reuse the source deployment, select production, or fall back to local Convex. Convex supports additional referenced deployments and finite expiration. [S05]

The MVP management credential is a **team access token**, entered through `eve auth` and validated against the requested team/project. EVE does not scrape Convex CLI login files. OAuth onboarding is deferred; application authentication is unrelated to this management credential.

The Management API uses bearer authorization. Its documented base is `https://api.convex.dev/v1`; project slug lookup resolves the recorded numeric identity. Store immutable IDs as well as labels, and reject a slug resolving to a different identity on a later operation. [S07]

### 11.2 Resource identity and request

Generate the complete intended reference before the HTTP request and persist it in the operation journal:

```text
dev/eve/<workspace-uuid-without-dashes>/<resource-slug>-<resource-id-hash8>
```

Keep the resource slug bounded so the whole reference stays within provider limits. Reference generation is deterministic from the persisted workspace ID/resource ID, not from the current branch name. Check for an unexpected preexisting resource with that reference before first creation; do not adopt it without an outstanding recorded creation intent.

The creation request must explicitly supply `type: "dev"`, `isDefault: false`, the unique `reference`, and a finite `expiresAt` Unix-millisecond timestamp. `region` is supplied only if configured. Current create/update/key wire schemas are defined by the official OpenAPI document; pin a reviewed snapshot in the implementation. [S08]

The response is not accepted blindly: verify project identity, `kind=cloud`, deployment type, nondefault status, reference, unique remote ID/name, deployment URL, and expiry. Any mismatch is a provider-contract error. Preserve the response identity for controlled cleanup; never attempt deletion of an unexpected default/production resource.

### 11.3 Expiration policy

Default requested lifetime: `5d` (120 hours) from provisioning. It is a maximum remote lifetime, not a worktree expiration or local session limit. Normal `destroy` attempts immediate deletion; remote expiration covers abandoned deployments.

Do not silently remove the expiration or lengthen/shorten it when the provider rejects the request. Report the requested lifetime and provider error. Provider expiration constraints include entitlement limits; no universal maximum beyond the configured request is assumed. [S08]

Record the provider-confirmed timestamp. Do not silently extend it on `status`, `sync`, or `resume`. No automatic keepalive/renewal in v0.1. Detect expiry, preserve the local worktree, and report `remote_missing`/`expired`. Recreating a backend with empty data requires creating a new workspace, not merely retrying a development command through EVE.

### 11.4 Deploy key

Create one named deployment key through the official key endpoint using the team management token. Its name includes the workspace/resource identity and a recorded generation counter. Request expiry no later than the deployment's expiry when supported by the pinned contract; verify the key's returned metadata.

Use the provider's normal deployment-key permission set within the disposable deployment; narrower action profiles can be evaluated later. Do not request project-wide or preview keys as a shortcut. With the non-OAuth flow, the documented create-key operation issues a token scoped to that deployment. [S09]

Persist the key in the protected credential store before native-file publication. Its value is never a public adapter output. The management credential is never copied into a worktree.

A lost response to key creation may leave a valid key whose secret cannot be recovered. Reconcile by exact recorded unique key name; revoke that owned key and create a replacement generation. Never revoke other developer keys. Record both generations until deletion is confirmed.

### 11.5 Native binding

Inside the declared backend `env_file`, own these entries:

```dotenv
CONVEX_DEPLOYMENT=dev:example-deployment
CONVEX_DEPLOY_KEY=dev:example-deployment|example-token
```

Exact selector serialization must follow the pinned Convex CLI compatibility contract and have fixtures against real supported CLI versions. `CONVEX_DEPLOYMENT` is not interchangeable with a human-readable project reference. Convex's deploy key/selection behavior gives the credential priority in relevant CLI paths. [S10]

EVE writes no inferred frontend URL variable. Frontend consumers must explicitly declare their native key name, e.g. `NEXT_PUBLIC_CONVEX_URL`, `VITE_CONVEX_URL`, or another existing key.

Before writing bindings, inspect selected native input files and current process env for stale selectors, deploy tokens/keys, self-hosted URL/admin keys, and explicit project targets. Replace reserved values only in the authorized worktree destination. Conflicting values elsewhere that can override it are errors or an explicit unsupported-launch diagnostic; do not mutate arbitrary files or the user's shell.

Reserve and audit at least `CONVEX_DEPLOYMENT`, `CONVEX_DEPLOY_KEY`, `CONVEX_DEPLOYMENT_TOKEN`, `CONVEX_SELF_HOSTED_URL`, and `CONVEX_SELF_HOSTED_ADMIN_KEY`. Key aliases and precedence must track the tested provider CLI version. [S10]

The ordinary backend file may be colocated with a frontend in a single-package project. Native process inheritance may also carry a deployment-scoped key through a root launcher. EVE prevents deliberate public-variable export and never distributes its management token, but does not claim per-process secrecy in an unchanged application launch graph.

### 11.6 URLs

Take the deployment URL from the verified provider response, not from assumptions about region or an animal-name hostname. Obtain the HTTP Actions origin using the documented canonical-URL interface and the actual deployment's defaults. Do not construct it by blindly replacing a substring or by stripping a region. [S14]

The initial adapter does not provision custom domains. If a provider response would require sending a deploy credential outside the approved Convex origin set, stop rather than follow an arbitrary redirect. Expose only verified public URL outputs.

### 11.7 Backend env and defaults

Leave project development defaults untouched. Convex applies them when creating deployments; this is a creation-time copy, not ongoing inheritance. EVE does not read back and redistribute those secrets locally. [S06]

If the manifest declares resource env overrides, resolve and write **only those ordinary application keys** using the deployment environment API. For example, update `SITE_URL` to the allocated frontend origin. Do not upload the shell, a frontend dotenv file, the management credential, or all of a source deployment's variables.

Deployment API calls use their documented deployment authorization format rather than the management bearer header. Verify only owned key values in memory and redact all unrelated returned values. Avoid writes when owned values already match. [S15, S16]

No built-in auth recipes or secret generation. If the app needs something not present in Convex dev defaults and the declared mappings, the app/agent configures it separately. EVE's success message does not assert that account registration or login is ready.

### 11.8 Code and data

Do not push schema/functions, run migrations, install packages, create test accounts, or seed fixtures during `create`. The project/agent uses its existing tools after provisioning. Convex's CLI owns code bundling and deployment; EVE must not reimplement that machinery. [S19]

Report `code_status=not_verified_by_eve` and `data_policy=provider_empty_initial_state` after creation. If a subsequent status refresh observes a provider code-deployment timestamp, report that observation without claiming EVE performed the push.

### 11.9 Cleanup

Delete the exact owned deployment through the documented deployment-deletion operation, never a project deletion endpoint. Deletion removes that deployment's data and files; the parent project must remain. [S20]

Before deletion, re-read identity and compare remote ID, name, project ID, type, and ownership reference with the creation record. A mismatch blocks automatic deletion. A verified already-absent owned deployment is an idempotent success. Authentication/network failures are not evidence of absence.

## 12. Provider interface and HTTP behavior

### 12.1 Internal contract

```go
// Domain interfaces; concrete types live in internal/domain.
type Provider interface {
    Validate(ctx context.Context, spec ResourceSpec) (ValidatedResource, error)
    Lookup(ctx context.Context, intent ResourceIntent) (Observation, error)
    Provision(ctx context.Context, intent ResourceIntent) (ProvisionResult, error)
    Configure(ctx context.Context, resource ResourceRecord, values map[string]string) error
    Inspect(ctx context.Context, resource ResourceRecord) (Observation, error)
    Destroy(ctx context.Context, resource ResourceRecord) error
}
```

`ProvisionResult` contains verified public outputs, identity, expiry, and a **secret reference**, not a public credential string. Core persists the creation intent before invoking `Provision` and persists returned identity before any later work. The provider must expose ambiguous outcomes distinctly from definite failures.

No arbitrary executable adapter hooks in v0.1. Bespoke project initialization remains external. A bounded hook/provider extension interface may be added later without making existing execution depend on EVE.

### 12.2 HTTP client rules

Use `net/http` with cancellation, bounded response bodies, TLS verification, and no credential-bearing cross-origin redirect following. API hosts are fixed by the adapter, not configurable through project TOML. Test transports can be injected only by tests.

Default request timeout: 30 seconds; provider operation reconciliation budget: two minutes. These are implementation timeouts, not user-facing duration promises. Respect `Retry-After` on throttling and use bounded exponential backoff with jitter. Do not retry a non-idempotent write merely because a network operation returned an error.

Classify errors as validation, unauthorized, forbidden, quota, throttled, transport, server, not-found, conflict, contract, or ambiguous-write. Preserve sanitized provider request IDs when supplied. Never log authorization headers, secret payloads, or raw response bodies from credential/env APIs.

### 12.3 Endpoint boundary

Management operations use the documented API for project lookup, deployment creation/inspection/reference lookup/deletion, and deploy-key create/list/delete. Native schema/function deployment stays outside EVE. Pin reviewed OpenAPI-generated or hand-written wire types and add contract fixtures; do not parse dashboard HTML or rely on undocumented internal CLI endpoints. [S07–S09, S20]

Deployment calls use the verified deployment origin plus the documented `/api/v1/` base for environment and canonical URL operations. Use the separate `Convex <credential>` authorization scheme. [S15]

### 12.4 No distributed transaction claim

SQLite, Git, filesystem rename, and Convex cannot participate in one atomic transaction. Implement a **journaled sequence of recoverable steps**. Unknown remote-write outcomes remain unknown until reconciliation; they are never converted into unconditional retry or assumed failure.

## 13. Provisioning algorithm

`create` executes these phases with a durable operation ID and immutable manifest/target-commit snapshot.

### Phase A — plan and preflight

Resolve registered source, repository/common-directory identity, target commit and manifest. Validate paths, ignore/tracking policy, dotenv syntax, collisions, declared input files, endpoint count, credential profile, and obvious launch conflicts. Perform read-only provider validation, including explicit team/project identity.

Read source local configuration for staging without displaying values. Detect source edits during snapshotting using content/identity checks. Present planned copies, owned keys, provider targets, expiration, and visible tracked-file modifications. Get authorization before mutation.

Local-only manifests work without provider credentials/network. A preflight failure must create no remote resource.

### Phase B — persist intent and reserve

Create workspace UUID, intended path, endpoint list, remote references, initial expiration intent, and operation journal. Acquire the workspace lock and repository mutation lock as appropriate. Reserve port claims transactionally. Commit intent before invoking Git or a provider.

### Phase C — worktree and staged configuration

Invoke Git worktree creation with the pinned commit/branch arguments. Record the actual Git administrative identity. Reconcile an interrupted Git operation from Git's state rather than blindly creating the directory again.

Prepare copies and native-file images without publishing source deployment bindings. Validate tracked target content/paths once more in the actual checkout. Do not install dependencies or execute repository setup hooks.

### Phase D — provision resources

For each resource in stable ID order, inspect the persisted unique reference, create if definitively absent and not already completed, verify/persist remote identity, create/persist its deploy credential, and obtain public outputs. Use serial provider provisioning initially; parallelism is not required for v0.1.

If the provider write outcome is uncertain, mark that step `unknown` and reconcile. Do not create a different reference to “get unstuck.”

### Phase E — configure and publish

Resolve all final values. Verify native writer compatibility. Re-probe allocated endpoints. Apply declared remote configuration, then commit local file images through the file journal. Confirm every file's final owned values, and persist the completed generation.

### Phase F — completion

Set `prepared`, release operation locks, and print path, assigned endpoints, resource identity, expiration, and ordinary setup/start reminder. Include code/data/runtime verification statuses. Exit zero only after every declared operation completed.

### Failure policy

Default: preserve the partially created worktree, allocations, deployment identities, and recoverable journal, with a nonzero exit and `eve resume`/`eve destroy` instructions. Do not automatically destroy a resource that may already contain user activity, nor conceal it behind a failed local write. Finite deployment expiration remains a fallback.

No automatic post-failure port release when a configured or partially published environment still exists. An explicit cleanup operation owns that decision.

## 14. Synchronization and recovery

### 14.1 Operation states

Workspace state is one of:

```text
creating → prepared
    │          │
    ▼          ▼
 failed      syncing → prepared
    │          │
    └──────► failed

creating / failed / prepared / expired / remote_missing
    → destroying → destroyed
                     or cleanup_pending
```

`expired` and `remote_missing` describe a preserved local workspace with an unavailable provider resource. A separate current operation/phase records whether local configuration or teardown is in progress.

Operation steps have `pending`, `inflight`, `succeeded`, `failed`, or `unknown` states. The workspace state alone is not enough to reconcile remote writes.

### 14.2 Resume

`resume` uses the stored operation snapshot. If the on-disk manifest changed during interruption, do not reinterpret the old operation through it. Report the difference and finish/cancel the original intent first.

For every step, verify the observable postcondition before skipping/repeating it. Examples: actual worktree registration, exact remote reference and ID, a key generation, or a destination's preimage/postimage HMAC.

A resource found under the reserved reference may be adopted only when there is an outstanding recorded creation intent and its identity/type/project/creation window are consistent. Otherwise fail with a conflict requiring review. Random references prevent accidental name reuse but do not justify deleting arbitrary matching prefixes.

After an ambiguous key write, revoke only the precisely recorded owned key generation before replacement. After a mixed file publication, complete remaining files if pre/postimages match. If neither matches, stop for manual drift resolution.

### 14.3 Repeated create

Repeated `create` for an existing prepared EVE workspace in the same repository returns its existing identity/path without duplicating or resetting it. If failed, direct the user to `resume`; if a non-EVE branch is already checked out, respect Git's conflict. Never interpret repeated creation as “delete and recreate backend.”

### 14.4 Sync

`sync` is explicit and requires the project to be stopped. Read the current worktree manifest, show the changes, validate allowed topology changes (§9.5), and preserve existing endpoint allocations/deployment IDs. Do not recopy developer-local files wholesale over the workspace.

Apply additive env mappings/endpoints and changed values only after drift checks. Update only EVE-owned remote keys. Do not reapply all Convex defaults or seed anything. Existing credential generations are retained unless known missing/revoked; rotation is a separately recorded repair, not routine sync behavior.

A changed block size, existing endpoint key/destination, resource region/path/project, or creation TTL is not an in-place migration; fail with `E_CREATION_ONLY_CHANGE`. Updating a credential profile is allowed only after it validates against the same immutable resource identities. If a cloud deployment is missing/expired, sync fails without replacement. If remote env update succeeds and local publication fails, persist that partial state; `resume` converges to the desired generation before reporting success. No false atomicity claim.

## 15. Destruction and garbage collection

### 15.1 Safety preflight

Resolve exact ownership, acquire operation lock, verify Git common-directory/worktree identity, and inspect local changes **before** deleting remote resources. Compare tracked changes with EVE's recorded intentional tracked edits; arbitrary staged or unstaged user changes and nonowned untracked files block normal deletion.

`--discard-changes` explicitly authorizes discarding those local changes. It is separate from `--yes`. Never delete the branch. Show a warning that ignored local files/caches inside a removed worktree also disappear; Git cannot determine their business value.

Probe declared local endpoint ports. If any are occupied, default to `E_POSSIBLY_RUNNING`; `--assume-stopped` acknowledges that the user has separately stopped or assessed processes. EVE does not invoke lsof or kill a process by port. Non-listening watchers such as a Convex CLI cannot be reliably ruled out; the user must stop the ordinary project launcher.

### 15.2 Order

1. Complete preflight and obtain destruction consent.
2. Persist `destroying` and the exact remote identities.
3. Re-verify and delete owned cloud deployments (reverse stable resource order).
4. Confirm absence. Remove known owned residual deploy keys only if needed through their exact recorded identities.
5. Recheck local safety/identity, then ask Git to remove the owned worktree. Git force removal is allowed only after the diff is verified to contain exclusively recorded EVE-owned edits, or after explicit `--discard-changes` authorization; it is not a blanket default.
6. Release port claims only after local removal and endpoint recheck; occupied residual endpoints retain a cleanup-pending allocation until assessed.
7. Purge workspace credential material and transient sensitive file images.
8. Retain a small non-secret destruction tombstone and mark `destroyed`.

Deleting the parent Convex project is never permitted. Neither is automatic deletion of the Git branch or source checkout.

If remote deletion fails, retain the local worktree and mark `cleanup_pending`; do not hide the failure by erasing evidence. If remote deletion succeeds but local removal fails, report that the backend is gone while local work remains. Resume the removal steps; never reprovision a backend to undo an intentional destroy.

### 15.3 Garbage collection

Default `gc` only reports. `gc --apply` retries previous cleanup operations and can remove exact recorded cloud resources for missing owned worktree paths after showing the plan. Verify the registered Git identity/path is truly absent, not merely inaccessible.

An expired cloud deployment does not authorize deleting an existing worktree. A remote object with an `eve`-looking name but no ownership record is not garbage that EVE may delete automatically. Report it as an unowned candidate for manual review.

No periodic daemon. Nothing local is garbage-collected while the CLI is not running. Provider expiration is the only autonomous cleanup mechanism in this design.

## 16. State and concurrency

### 16.1 Storage locations

Use owner-only local directories:

```text
Linux:
  ${XDG_STATE_HOME:-~/.local/state}/eve/
  ${XDG_CONFIG_HOME:-~/.config}/eve/config.toml

macOS:
  ~/Library/Application Support/eve/
  ~/Library/Application Support/eve/config.toml
```

State layout:

```text
state.sqlite
locks/                    # advisory lock files; never inside worktrees
secrets/                  # credential objects, 0600
pending/                  # temporary sensitive transaction images, 0600
```

Directories are `0700`; state/database/WAL/SHM and sensitive files are not world/group-readable. State must live on a local filesystem supporting the required locking semantics. Reject an explicitly selected unsupported storage location rather than promising safety on arbitrary shared filesystems.

Do not place a new `.eve` directory or executable startup marker in the worktree. The external registry and Git administrative identity locate ownership. Moving worktrees manually requires a later explicit repair capability; v0.1 diagnoses mismatches rather than guessing.

### 16.2 Database schema

The bundled `state-schema.sql` is an initial executable schema. Core entities:

| Entity | Purpose |
|---|---|
| `repositories` | Local repository UUID, canonical common directory, registered source checkout. |
| `workspaces` | UUID, path/branch/commit, manifest snapshot/hash, generation, lifecycle state. |
| `port_blocks` / `port_claims` | Reserved range and one unique claim per TCP port. |
| `endpoints` | Stable service/name slot and allocated port. |
| `resources` | Provider intent, exact remote identity, public outputs, expiry, credential reference, state. |
| `operations` / `operation_steps` | Durable intent and phase-level progress, including ambiguous outcomes. |
| `managed_files` / `managed_values` | Ownership and keyed digests, never plaintext secret values. |
| `file_transactions` | Preimage/staged-image references and per-file publication progress. |
| `credential_objects` | Safe credential metadata and opaque local secret-object references. |

Use foreign keys, uniqueness constraints, `journal_mode=WAL`, `synchronous=FULL`, and an explicit busy timeout. Configure pragmas on each required connection, not just once in an unrelated process. SQLite documents these concurrency/durability controls. [S21]

Do not hold a write transaction open during a network call or Git subprocess. Persist intent first, release transaction, perform the action, then persist outcome. Never store whole unredacted provider responses in `outputs_json` or error fields.

### 16.3 Locks

Use host advisory file locks through platform code for a long-running workspace operation; an OS-held lock is released if its process exits. The file's mere existence is not a live lock. Add a short repository lock around worktree/branch mutations. Unique claims and database transactions coordinate allocations across different workspaces.

Lock order: workspace operation lock, repository mutation lock when needed, then short database transaction. Do not acquire another workspace lock while holding a database transaction. Concurrent commands on one workspace fail with a named busy diagnostic rather than wait indefinitely.

When an explicit destroy supersedes a failed create/sync, acquire the same workspace lock, atomically mark the prior operation cancelled and insert a linked replacement operation, retaining every prior step and resource intent. The destroy must reconcile outstanding unknown writes before deciding what exists; cancelling the local operation does not imply the remote request failed. An `inflight` journal row after process death is not proof an API action failed. The next authorized `resume` reconciles it. Do not automatically “steal a lease” based solely on PID or timestamp.

### 16.4 Identity and time

Generate IDs with cryptographically secure random UUIDv4. Use opaque immutable remote IDs as well as names/references. Store timestamps as UTC Unix milliseconds; render human dates with timezone/offset and JSON dates in RFC 3339 UTC.

Use monotonic deadlines for waits/backoff. Suspicious local/provider clock skew is a diagnostic; avoid accidentally requesting an already expired resource. Local clock changes never cause automatic worktree deletion.

### 16.5 Migration and loss

Record schema migrations transactionally; refuse to open newer unsupported schemas for mutation. Back up a consistent database snapshot before migration using SQLite's backup facilities, not by blindly copying a live database without its journal. Credential-object permissions remain intact.

Loss of the state database removes EVE's evidence of ownership. Do not reconstruct permission to delete remote objects from name prefixes alone. Offer a read-only report; manual reviewed recovery is separate from ordinary garbage collection. Existing applications can still run from their native files while their provider resources remain available.

## 17. Credentials and security

### 17.1 Credential sources

A team token may be entered through a hidden prompt or `--token-stdin`; never as a literal command-line flag. An ephemeral `EVE_CONVEX_TOKEN` process variable can override the default profile for a single EVE invocation, but EVE must never copy it into project files or pass it to Git. Named nondefault profiles are stored explicitly through `eve auth`.

Store MVP credentials in an owner-only plaintext credential object outside the repository. This is an intentional dependency-free baseline, **not encrypted-at-rest storage**. Do not market filesystem permissions as protection against the same OS user. Native keychain support is a future optional enhancement, not a required executable/service.

Verify token scope/team during login and before using it with a project. Management tokens may be broad; prompt for a development team token and do not request or discover production credentials automatically. Deleting a local profile must warn about pending cleanup but not delete cloud resources.

### 17.2 Data minimization

Only deployment-scoped provider keys enter native backend files. Never expose them as interpolation values, in a frontend-public prefixed key, in JSON output, logs, telemetry, or the manifest. A colocated app still has normal filesystem/process access; no app-level secrecy claim is made.

The credential store also holds the random machine HMAC key used for managed-value fingerprints and any staged sensitive file images. SQL contains only opaque references and metadata. Delete transaction snapshots after confirmed completion; retain only live deployment credentials until destruction.

No automatic production data copying, global secret export, browser cookie manipulation, or seeding. Backend defaults remain provider-side.

### 17.3 Untrusted repository boundary

Manifest content is data, not code. No arbitrary HTTP origins, shell interpolation, hooks, external includes, or source paths outside the repository. Copies and remote resource choices are shown in the plan. A malicious project can still read its copied developer secrets when the user later runs it; EVE is not the mechanism that makes untrusted applications safe.

Use argument arrays for Git, reject option-injection branch/path values, disable EVE-triggered hooks, escape terminal control characters in rendered names, and bound all parsed input sizes. HTTP redirects must never forward credentials to a different origin.

## 18. Diagnostics and machine-readable output

### 18.1 Output rules

Human output goes to stdout for results, stderr for progress/errors. `--json` emits exactly one JSON result object to stdout; progress remains stderr. No raw environment dump command in v0.1. Terminal coloring must not affect JSON and must honor `NO_COLOR`.

Example successful creation result:

```json
{
  "schema_version": 1,
  "command": "create",
  "ok": true,
  "workspace": {
    "id": "963a3d1b-cd82-48b0-8d74-5026d8cf7427",
    "branch": "feature/payments",
    "path": "/home/dev/.eve-worktrees/app-a18d0b29/payments-963a3d1b",
    "state": "prepared",
    "generation": 1
  },
  "services": {
    "web": {"port": 32400, "url": "http://localhost:32400"}
  },
  "resources": {
    "backend": {
      "provider": "convex",
      "name": "example-deployment",
      "url": "https://example-deployment.convex.cloud",
      "expires_at": "2026-09-14T12:00:00Z"
    }
  },
  "verification": {
    "configuration": "verified",
    "runtime": "not_checked",
    "code": "not_verified_by_eve",
    "data": "provider_empty_initial_state"
  },
  "warnings": []
}
```

The example dates/ports are illustrative, not expected fixed values. JSON should remain additive-compatible within schema version 1. Encode provider IDs as strings in public output to avoid numeric precision issues in consumers.

### 18.2 Error shape

```json
{
  "schema_version": 1,
  "command": "create",
  "ok": false,
  "error": {
    "code": "E_NATIVE_ENV_CONFLICT",
    "message": "Two services assign different values to PORT in the same file.",
    "retryable": false,
    "phase": "validate",
    "workspace_id": null,
    "details": {"path": ".env.local", "key": "PORT", "owners": ["web", "admin"]},
    "next_action": "Select existing per-service files or distinct keys consumed by the project."
  }
}
```

Include paths/key names but redact values unless they are explicitly classified public URLs/ports. A parser error never includes the entire dotenv line when it may contain a secret.

### 18.3 Exit codes

| Code | Meaning |
|---|---|
| 0 | Requested operation completed; informational warnings may remain. |
| 1 | Internal/unclassified failure; sanitized diagnostic required. |
| 2 | Invalid arguments, manifest, unsupported layout, or serialization. |
| 3 | Safety/ownership conflict, drift, busy workspace, approval required, or unsafe deletion. |
| 4 | Missing/invalid credentials or denied provider permission. |
| 5 | Provider failure, quota, network error, or failed reconciliation. |
| 6 | Partial operation or cleanup pending, with durable recovery record. |
| 7 | Managed resource expired or disappeared. |
| 130 | Interrupted; state recorded to the extent possible and next action reported. |

Specific `error.code` strings distinguish failures within these categories. Required named diagnostics include missing target manifest, path escape, symlink, tracked credential file, missing ignore rule, allocation exhaustion, occupied port, managed-key drift, ambient Convex selector conflict, unsupported launch semantics, wrong provider identity, ambiguous remote write, and pending cleanup.

### 18.4 Doctor output

For each check report `pass`, `warning`, `fail`, or `not_checked`, with evidence. Check file existence/permissions, exact managed values via safe comparison, available Git metadata, selected ownership, expiration, and observed listener availability. Remote checks are read-only.

A listening port is not proof the expected service owns it. An HTTP response is not proof authentication works. Do not mark runtime healthy merely because the expected port is occupied. No browser or login checks.

## 19. Internal packages and interfaces

Suggested source layout:

```text
cmd/eve/
internal/
  cli/             command parsing, presentation, JSON envelopes
  domain/          IDs, value types, errors, plans, states
  config/          TOML decoding, defaults, semantic validation
  discover/        static workspace/provider evidence
  git/             checked Git subprocess interface
  ports/           registry allocation and socket probes
  resolve/         restricted references and destination ownership
  envfile/         lossless parser/writer and serializer
  fsops/           safe paths, atomic files, file transaction journal
  state/           SQLite queries and migrations
  credentials/     protected objects, token profiles, HMAC fingerprints
  lifecycle/       plan/create/sync/resume/destroy/gc coordination
  providers/
    convex/        public API client, binding, identity checks
  platform/        per-OS paths and advisory locks
spec/
  SPEC.md
  eve.schema.json
  examples/
  state-schema.sql
testdata/
  envfiles/
  repositories/
  provider-contracts/
```

Important dependency direction: CLI → lifecycle → domain services/providers. Providers do not mutate arbitrary local files or access the state database directly. They return planned reserved bindings and recordable outcomes; core enforces file ownership and journals them. The provider's HTTP client is injectable for tests, as are clock, random ID generator, socket prober, and Git execution interface.

Use small interfaces around side effects; avoid a general dependency-injection framework. Context cancellation must propagate through the lifecycle, HTTP, and Git subprocess layer. Terminating EVE's own Git subprocess is distinct from supervising the application.

Pin wire schema snapshots with origin, retrieval date, checksum, and provider/CLI compatibility test versions. Generated wire types do not define domain ownership rules. Decode expected discriminators strictly while tolerating irrelevant additive provider fields.

## 20. Testing requirements

### 20.1 Unit and property tests

| ID | Test | Required assertion |
|---|---|---|
| U01 | Manifest parsing/defaults/unknown fields | Unknown or duplicate fields fail; example manifests match schema and semantic validation. |
| U02 | Reference resolution | Missing endpoint/resource and unsupported secret/env references fail without partial output. |
| U03 | Destination merging | Conflicting shared-file keys fail; identical public values merge deterministically. |
| U04 | Path safety | Absolute escapes, `..` escapes, symlink parents, hardlink destinations, and case aliases are rejected. |
| U05 | Lossless env editing | All unmanaged bytes survive; CRLF, comments, quote styles, and multiline values round-trip. |
| U06 | Duplicate/malformed managed keys | Clear error; no partial rewrite or secret-bearing line in logs. |
| U07 | Generated-value subset | Unsafe expansion/metacharacters fail rather than change semantics. |
| U08 | Drift | Unmanaged edits survive; changed managed values require explicit overwrite. |
| U09 | Allocator | No overlapping claims across concurrent processes; bound ports are skipped; slots do not renumber. |
| U10 | Sensitive outputs | Tokens cannot reach interpolation outputs, logs, errors, or JSON serialization. |
| U11 | Discovery | Client-only dependency, frontend-owned backend, custom functions path, and components are correctly distinguished. |
| U12 | TTL and IDs | Stable UUID/reference generation; bounded provider names; UTC conversion and expiry refusal. |

Fuzz the dotenv parser, interpolation parser, TOML handling, path normalization, and provider error decoder. Property-test idempotent file application: applying the same desired values twice produces identical bytes.

### 20.2 Integration tests with temporary Git repositories and fake provider

| ID | Scenario | Expected result |
|---|---|---|
| I01 | Fresh create, no external runtimes on PATH | Core works with only Git and EVE; no installs or script execution. |
| I02 | Existing/new branches | Correct commit checked out; existing branch never reset; source dirty code not copied. |
| I03 | Canonical source preservation | Application config and script hashes in source unchanged after create/sync/destroy. |
| I04 | Canonical source reuse | Creating from an EVE worktree uses the registered source's local inputs, not old ephemeral bindings. |
| I05 | Nonignored or tracked credential output | Fails before provisioning; no secret file introduced. |
| I06 | Provider create succeeds then connection drops | Resume resolves exact reference; at most one deployment created. |
| I07 | Key response lost | Owned key generation reconciled/revoked; no unrelated key revoked. |
| I08 | Crash after each durable boundary | State can resume/destroy without lost identity or ambiguous silent success. |
| I09 | Crash between file renames | Workspace not prepared; resume completes safe files or reports drift. |
| I10 | Remote env succeeds, local write fails | Partial generation persists; resume converges without replacing backend. |
| I11 | Manual worktree deletion | Remote resource remains identifiable and cleanup is possible. |
| I12 | Provider delete denied/offline | Cleanup remains pending, identifiers retained, no project deletion called. |
| I13 | TTL elapsed | Local worktree preserved; no automatic replacement or data reset. |
| I14 | User work on destroy | Default refusal before remote deletion; explicit discard required; branch retained. |
| I15 | Scope mismatch | Wrong project/type/default/ID blocks mutation/deletion. |
| I16 | Concurrent workspaces, including separate repos | Unique local claims and remote references under the shared user registry. |
| I17 | Script immutability | package.json, turbo.json, bunfig.toml, mise files, lockfiles, and source hashes unchanged. |
| I18 | State/credential permissions | Parent dirs and sensitive files have required modes; metadata has no plaintext key. |
| I19 | False-absence response | 401/403/5xx never treated as successful deletion. |
| I20 | No broad root injection | Per-service PORT/key mappings stay in their declared native destinations. |

### 20.3 Native-launch acceptance fixture

Build and commit a representative Bun/Turbo monorepo **before integrating EVE**: two frontend/listening services plus a separate Convex backend package, with its ordinary dotenv strategy. Pin fixture dependency versions.

1. Start the baseline with root `bun run dev` to prove it is a working project.
2. Create two EVE worktrees from the same fixture and configure different ports/backend outputs.
3. Install dependencies through the fixture's unchanged normal procedure, outside EVE.
4. Remove EVE/mise from the launch PATH and run the identical root `bun run dev` in each.
5. Assert actual listener ports, frontend-observed public Convex URLs, and the backend CLI's selected deployment differ correctly.
6. Assert scripts and launch configuration files are byte-identical to the baseline.
7. Stop the processes normally and destroy one workspace. The other must remain functional.

Include a deliberately unsupported hardcoded-port/overriding-env fixture. EVE must diagnose it rather than silently rewriting its scripts. A failing real integration test is not fixed by weakening the acceptance criterion or pretending file values prove runtime values.

### 20.4 Live Convex acceptance tests

Opt-in tests use a dedicated nonproduction project and supplied management credential. They create a named nondefault cloud dev deployment, verify finite expiry and defaults, create a usable deployment-scoped key, write an owned non-secret backend URL, bind a worktree, let the ordinary project CLI push code, and delete the deployment while preserving its project/default deployment.

Tests must clean up by exact owned IDs in a finalizer and use expiration as a fallback. Logs must be redacted. Test key inability to access an unrelated deployment where permissions can be verified safely. Do not use production data or automatically clone fixtures from a production project.

Run on both Linux and macOS. Mock-provider tests alone do not establish that cloud permissions, token formats, URL discovery, and CLI loading work together.

## 21. Delivery milestones

### M0 — validate the two difficult boundaries

Before building the full CLI, prove the unchanged Bun/Turbo launch fixture with manually prepared native files, and prove the management-API-only Convex provisioning/key/expiry flow. Document exact successful runtime/CLI versions. This is the first engineering task, not an open product preference.

### M1 — local core

Implement strict manual manifest loading, Git lifecycle, source registration, durable state, TCP allocation, safe dotenv materialization, and `create/path/status/destroy` for local-only resources. Acceptance: R01–R04 and concurrency/file-safety tests.

### M2 — Convex cloud

Implement team-token profiles, project resolution, referenced dev deployment creation, key generation, URL outputs, native bindings, defaults policy, and exact remote cleanup. Acceptance: dedicated live Convex tests and no provider CLI dependency in provisioning.

### M3 — recovery and synchronization

Implement durable step journal, `resume`, restricted `sync`, drift checks, `gc`, operation locks, interruptions, and all partial-failure cases. This milestone is required before a public release—not optional cleanup polish.

### M4 — onboarding and diagnostics

Implement conservative `init`, evidence output, `plan`, `doctor`, JSON envelopes, ignore/loader/conflict guidance, and source/manifest change review.

### M5 — release

Validate all supported OS/architecture binaries, publish checksums, pin dependency and API contracts, document the native-launch compatibility fixture and limitations, and ship an uninstall procedure that warns about outstanding cloud resources. No telemetry or implicit update checks in the MVP.

## 22. Release gates and remaining engineering validation

No unresolved product questions block this design. The user decisions are recorded in §24. The following are technical validations, not assumed capabilities:

| Gate | What must be established | Failure response |
|---|---|---|
| G1 | Real unchanged Bun/Turbo leaf launchers read the declared per-service files with correct precedence. | Diagnose unsupported layouts; do not introduce wrappers or edit scripts. |
| G2 | The management API supports the desired dev/defaults/key/expiry workflow with the chosen team credential. | Do not fall back to shared, production, local, or non-expiring deployments. |
| G3 | Native Convex binding/credential format is consumed by the pinned project CLI without interactive retargeting. | Block compatibility claim and adjust adapter serialization, not project scripts. |
| G4 | HTTP Actions URL retrieval works for supported regional deployment URLs. | Do not guess domains or expose wrong cross-service URLs. |
| G5 | Lossless env writes meet the supported syntax contract across fixture loaders. | Reject unsupported generated values safely. |
| G6 | Recovery correctly handles every external-write and local-publication interruption point. | Retain unknown/cleanup-pending state; no unconditional retries. |
| G7 | Linux/macOS locking, file permissions, IPv4/IPv6 probes, and package distribution pass real tests. | Narrow the supported platform claim until validated. |

This document does not claim these live integrations have been implemented or exercised during specification writing. The included example/schema artifacts can be validated locally; cloud behavior requires the dedicated acceptance environment.

## 23. Deferred scope

Local/self-hosted Convex; other providers; existing-worktree attach mode; automatic PR fetching; automatic resource replacement; TTL renewal; automatic seeding; database cloning; auth-specific provisioning; per-worktree application secret synchronization; application process management; Herdr/tmux plugins; reverse proxies/DNS/TLS; automatic browser isolation; executable hooks; general JSON/YAML/TOML application-config patching; arbitrary dotenv expression generation; managed-key removal/retarget migrations; optional mise export; OAuth credential onboarding; native keychains; Windows.

These are not prerequisites for the core abstraction. Add them only as concrete supported use cases require them.

## 24. Decision record and definition of done

| Decision | Rationale |
|---|---|
| Native files rather than mise overlays | Application execution must not depend on EVE. |
| No changes to commands/scripts | Preserve root `bun run dev` and language-native project conventions. |
| Go + embedded SQLite | Standalone CLI with durable concurrent allocation/ownership state and no external database service. |
| TOML manifest | Small declarative configuration, inspired by mise ergonomics without its runtime dependency. |
| One Convex cloud dev deployment per resource/workspace | Isolate backend schema, data, and deployment-side env from other worktrees. |
| Convex defaults for secrets | Keep initial scope to environment creation, not secret administration. |
| No auth-library/browser/seeding subsystem | These are application concerns; only declared connectivity configuration is EVE-owned. |
| Five-day remote expiration | Fallback for abandoned Convex resources; never a timer for deleting source code. |
| No arbitrary local concurrency cap | Allocation capacity and provider quotas are reported rather than hidden behind a new product limit. |
| External multiplexers | EVE is an ordinary CLI, not a terminal coordinator. |
| Conservative discovery | Unusual layouts are valid; false ownership inference is worse than a small explicit manifest. |
| Journaled recovery | Correct cleanup and retry behavior are core functionality. |

**Definition of done:** From a supported existing repository, two `eve create` operations produce two independent worktrees whose unchanged native startup commands use different declared local ports and Convex backends. Neither runtime needs EVE. Destroying either workspace deletes only its owned resources, preserves the other workspace and the source checkout, and retains enough state to recover from failures.

## 25. Sources

The document's architecture, defaults, CLI, schema, and acceptance criteria are proposed EVE design decisions. Sources below ground third-party integration facts; documentation/schema snapshots should be pinned and revalidated during implementation.

- **S01 — Git worktree:** https://git-scm.com/docs/git-worktree
- **S02 — Bun environment variables:** https://bun.sh/docs/runtime/environment-variables
- **S03 — Turborepo environment variables:** https://turborepo.dev/docs/crafting-your-repository/using-environment-variables
- **S04 — Next.js CLI:** https://nextjs.org/docs/app/api-reference/cli/next
- **S05 — Convex multiple deployments:** https://docs.convex.dev/production/multiple-deployments
- **S06 — Convex environment variables and project defaults:** https://docs.convex.dev/production/environment-variables
- **S07 — Convex Management API overview:** https://docs.convex.dev/management-api/overview
- **S08 — Convex official Management OpenAPI schema:** https://api.convex.dev/v1/openapi.json
- **S09 — Convex create deploy key:** https://docs.convex.dev/management-api/create-deploy-key
- **S10 — Convex CLI selection implementation and reference:** https://github.com/get-convex/convex-js/blob/main/src/cli/lib/deploymentSelection.ts ; https://docs.convex.dev/cli/reference/deployment
- **S11 — Convex frontend quickstart:** https://docs.convex.dev/quickstart/nextjs
- **S12 — Convex project configuration:** https://docs.convex.dev/production/project-configuration
- **S13 — Convex components:** https://docs.convex.dev/components/authoring
- **S14 — Convex canonical URLs:** https://docs.convex.dev/deployment-api/get-canonical-urls
- **S15 — Convex Deployment Platform API:** https://docs.convex.dev/deployment-platform-api
- **S16 — Convex deployment environment update:** https://docs.convex.dev/deployment-api/update-environment-variables
- **S17 — go-toml v2:** https://pkg.go.dev/github.com/pelletier/go-toml/v2
- **S18 — CGo-free SQLite driver:** https://pkg.go.dev/modernc.org/sqlite
- **S19 — Convex platform orchestration and code deployment guidance:** https://docs.convex.dev/platform-apis/overview
- **S20 — Convex delete deployment:** https://docs.convex.dev/management-api/delete-deployment
- **S21 — SQLite pragmas:** https://sqlite.org/pragma.html
