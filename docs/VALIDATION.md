# Specification artifact validation

**Date:** September 9, 2026  
**Scope:** the documents, examples, and initial schemas in this bundle—not an EVE implementation.

## Checks completed

| Check | Result |
|---|---|
| JSON Schema meta-validation | `eve.schema.json` passes Draft 2020-12 schema validation. |
| Example manifests | All three example TOML files parse and validate against the manifest schema. |
| Invalid-manifest rejection | Empty manifest, unsupported version, undeclared service command field, and invalid TTL are rejected. |
| Embedded examples | Every TOML and JSON fenced block in `SPEC.md` parses successfully. Code fences are balanced. |
| SQLite initialization | `state-schema.sql` executes in a fresh in-memory SQLite database. |
| Valid state operations | Repository/workspace registration, allocation, endpoint insertion, and an explicitly superseding operation succeed. |
| Allocation/ownership rejection | Overlapping port claims, claims outside a block, endpoints referencing another workspace's claim, and mismatched slots fail. |
| Allocation immutability | In-place claim reassignment, endpoint reallocation, and block resizing fail. |
| Operation concurrency | A second unfinished operation for one workspace is rejected. Cancelling and linking a superseding operation succeeds. |
| Database consistency | SQLite integrity check reports `ok`; foreign-key check returns no violations. |
| Cross-reference check | Every cited source identifier in the spec has a bibliography entry. |

Artifact checks used Python's TOML/JSON/SQLite libraries and a Draft 2020-12 JSON Schema validator. Python is **not** part of the proposed EVE runtime stack. These checks validate the schema's basic syntax and selected constraints; they do not replace application-level semantic validation or driver-specific tests.

## Not implemented or executed

No EVE binary, dotenv writer, Git lifecycle engine, provider adapter, or application launcher was implemented as part of producing this specification. In particular, the following have **not** been exercised here:

- Real unchanged Bun/Turborepo launch graphs consuming generated per-service configuration.
- Live Convex deployment creation, key issuance, expiration, backend binding, or cleanup.
- The selected Go SQLite driver on Linux/macOS, filesystem crash recovery, interprocess locking, or concurrent real port allocation.
- Security/fault-injection tests against symlinks, interrupted file publication, inherited credentials, or ambiguous cloud responses.

Those are explicit acceptance tests and release gates in sections 20–22 of `SPEC.md`. Milestone M0 deliberately tests the two central integration assumptions before the broader implementation proceeds. The specification does not claim universal support for arbitrary environment loaders or that configured workspaces have already deployed code, seeded data, or authenticated successfully.

## Subsequent M0 engineering probes

The statements above describe specification writing. The repository now also contains opt-in M0 probes; see [M0.md](M0.md) for exact versions, commands, limitations, and the live runbook.

On September 9, 2026, `go test ./...`, `go vet ./...`, and `EVE_M0_NATIVE=1 go test -race ./tests/m0 -count=1 -v` passed on Linux/amd64. Native tests exercised the **frontend-only slice** of a pinned Bun/Turbo/Vite fixture across a committed baseline and two real Git worktrees. They also reproduced an unsupported process-env-only loader and ambient-variable conflicts. Provider-probe safety tests and pinned API checksum checks passed offline.

The first supplied-token live runs demonstrated the unfiltered launch, cloud identity/keys/expiry/URLs, key isolation, exact deletion and preservation of project/default identities, but failed the missing-development-default marker checks. After the operator added that default, the full live test passed in 17.05 seconds, including all three inheritance assertions. All seven deployments across those runs were confirmed deleted. macOS and arm64 remain unverified.

## M1 configuration foundation

Strict Go manifest/user-config parsing, restricted references, public configuration resolution and lossless dotenv image preparation are now implemented. All bundled examples pass the Go validator. Unit tests, bounded fuzz runs, `go vet ./...`, `EVE_M0_NATIVE=1 go test -race ./... -count=1`, and `CGO_ENABLED=0 go test ./...` pass on Linux/amd64. Native tests also consume M1-generated file images through unchanged Bun/Turbo/Vite scripts.

The next slice adds protected SQLite state, immutable creation intents, per-process advisory locks, source/common-directory identity records and TCP block allocation. Real-process tests verify concurrent claims across two repositories, fresh database initialization, SIGKILL lock release, committed-candidate retention and uncommitted SQL rollback. Real IPv4/IPv6 sockets cover occupied ports and stable allocations after late conflicts. Database constraints, per-connection pragmas, private modes, unsafe links, newer-schema refusal and source replacement are tested.

The complete Linux suite, native/race checks and CGo-disabled tests pass. State/allocator test executables also cross-compile with `CGO_ENABLED=0` for all four target OS/architectures; **this does not establish runtime behavior on macOS or arm64**.

## M1 Git creation boundary

The next bounded slice implements Git-aware source registration/planning, pinned committed target manifests, hook-suppressed worktree creation, durable Git intent/identity checkpoints and exact removal primitives. Removal preserves pre-existing branches; a branch durably recorded as EVE-created is pruned only while still at its original target. Real Git/SQLite tests cover moved refs, invoking-worktree isolation, dirty files, ownership refusal, created/pre-existing branch treatment, stale-branch recovery and lost-response reconciliation without repeating creation. A blocked checkout filter demonstrated that Git writes its lock reason before checkout finishes; observation now requires a clean worktree, stable regular index and no index lock. Cancellation also terminates the filter's process group.

## M1 read-only file preflight

`PlanGit` now captures bounded source configuration/copy snapshots. `internal/files` selects tracked target content rather than dirty source bytes, validates original path components and actual target tracking/ignore policy, detects changed inputs, and prepares private local-only images without writing application files. Real worktree tests cover these boundaries, limits, links, private diagnostics and target-specific ignore rules. Git's `check-ignore` required explicitly disabling its incompatible global literal-pathspec flag.

Linux tests, native/race checks, repeated file/lifecycle tests and CGo-disabled tests pass. Files/lifecycle test executables cross-compile for all four target OS/architectures; macOS/arm64 runtime behavior remains unverified. In-memory images do not establish protected staging, HMAC drift handling, journaled publication or crash recovery.

## M1 protected image staging

The staging lifecycle now records opaque image references and keyed fingerprints before sensitive writes, verifies synced private objects, and checkpoints staging without publishing application files or completing a generation. Machine-key initialization is serialized; a demonstrably uncompleted bootstrap with no dependent HMAC can be repaired, but any established/dependent machine key is never regenerated.

Real Git/SQLite tests kill a subprocess after partial or complete image sets but before SQL acknowledgment. Complete sets reconcile from their original objects; incomplete sets remain unresolved. Tests also cover corruption with restored timestamps, key initialization concurrency, private permissions/link/type checks, bounded reads and value-free metadata/formatting. These staging tests alone do not establish publication, drift-aware sync, snapshot cleanup or power-loss recovery.

## M1 initial publication

The production local-only lifecycle now commits `prepared` generation `1` after verified atomic per-file publication. Tests cover fresh Git/filesystem policy, occupied endpoints, missing parents, late parent-link substitution, no-replace rename, exact receipt reconciliation after SIGKILL, user-edit refusal and verified post-completion snapshot cleanup.

`TestPublishedLifecycleNativeFrontends` prepares two real worktrees with production APIs and then runs unchanged Bun/Turbo/Vite frontend commands. Independent ports and public configuration pass; scripts/configuration/lockfiles remain unchanged. Installation and supervision are test-harness actions and no live cloud claim follows here.

## M1 local-only CLI and destruction

The real `eve` executable now creates, reports and destroys LOCAL-ONLY workspaces through the guarded `--yes` path. Real CLI tests cover consent gates, JSON output, selectors, dirty work, readonly inspection, exact-owned tracked files, source preservation, exact pruning of unchanged EVE-created branches, preservation of pre-existing or divergent target branches, occupied ports, cleanup pending and port-claim release.

Command parsing now accepts the documented mixed argument order (`status branch --refresh`, flags before selectors, and equivalents) without disabling validation. Auth status/logout open the user registry without requiring an invoking Git checkout and expose profile metadata only, never token values. Bounded provider/`envfile` failures preserve codes, key/line details, exits and next actions.
`TestCLILocalLifecycleNativeFrontends` creates two independent monorepo worktrees, starts unchanged Bun/Turbo/Vite commands in both, and verifies page JSON through a real `agent-browser` session. It destroys one remaining application while the other stays available. This is Linux frontend evidence, not cloud or macOS proof.

## M2 Convex lifecycle

Production `eve` now provisions and destroys exact nondefault Convex dev deployments. An 80.74-second Linux run created two workspaces and two independent five-day deployments, pushed fixture code through the ordinary Convex CLI, verified each backend's configured `SITE_URL`, destroyed the first while the second kept running, then deleted the second and verified that no `dev/eve/` deployment remained. Original project/default identities were preserved. Earlier failed runs left four deployment names; each exact nondefault test-owned resource was verified and deleted.

A 96.91-second `aws-eu-west-1` production run also passed and left no resources. Browser validation passed with `agent-browser` 0.16.3 against EVE-generated worktrees. Offline tests cover sanitized provider failures, exact-reference reconciliation after an ambiguous create, protected deploy-key storage, native selectors, public frontend URLs, remote env selection, remote/local cleanup and secret purge. macOS remains the open platform gate.
Provider delete denial preserves the local worktree and exact `cleanup_pending` identity; recovery finishes remote/local cleanup without growing ownership.
TTL evidence now includes an actual elapsed 31-minute deployment in 1872 seconds, preserving the worktree until explicit destroy; a 2-minute TTL failed validation without aliasing. One boundary-error deployment was verified/deleted by exactly its name; no residue remains.
Authentication now opens a hidden read on interactive terminals and rejects non-TTY login input unless `--token-stdin` is explicit; unit tests cover normalization, non-TTY refusal, cancellation and secret-free errors.
Credential sign-out removes local secret/profile references only after matching checksum metadata; SQL and private tests confirm absent secrets, missing profiles, and no provider mutation.

## M3 initial resume

`eve resume` now resumes the original operation from durable UUID, revision, manifest, allocation, resource and journal intent. Separate-process tests resume after Git interruption and a test fake proves one deployment is reconciled after an ambiguous create instead of another being created. Prepared resume is idempotent; failed/unknown or dirty states remain diagnostic. Repeating `create` over a prepared branch is exact and mutation-free; `--from` conflicts and incomplete branches route to resume.

Bounded `gc` is report-only by default. `gc --apply` verifies the recorded worktree checkout is absent before generating a new destroy intent, completes exact remote deletion/key purge and claim release, and refuses recreated paths. A raw deleted checkout with retained Git admin metadata is reconciled by matching the admin directory's recorded filesystem identity and exact `gitdir` pointer, never by broad `git worktree prune`. Global `list --all`, `gc`, and credential metadata operations also run outside an invoking checkout.


Additive key/extra-endpoint `sync` runs local and Git drift checks before any remote write, uploads only declared resource env keys, preserves every existing endpoint slot while appending probed capacities inside the block, publishes generation 2 with fresh fingerprints, and reports `restart_required`. Tests cover absent bytecode no-ops, unmanaged content, drift/overwrite, endpoint immutability, remote additive values, and destruction after sync.
## M4 inspection

Read-only `eve list [--all]` reports registered repositories, live workspaces, public ports and public Convex metadata without creating locks, credentials, files or remote calls. CLI tests confirm the same workspace appears with its assigned port and disappears from the default report after exact destruction. Broader framework discovery remains limited.
`eve init --convex` now separates backend, URL-consumer and listener discovery: resource-only manifests work, `vite.config.js` and `vite.config.ts` are recognized deterministically, native ports require strict `PORT` proof, existing local Convex URL keys bind by relationship without storing values, and custom keys require matching public hints from the selected backend destination.
`eve keys` reports committed workspace/service/Convex interpolation variables as read-only source inventory; unit and production CLI tests prove exact IDs/extra endpoints are exposed while env/config/secret values and state creation are not.
`eve doctor` checks registry/Git/current file HMACs, endpoint availability, resource expiry and optional exact remote identity while leaving runtime/loader unread. `status --refresh` uses that read-only identity path without env/key access; tests cover drift warnings and provider-resource remote verification.
`eve plan` independently evaluates the committed target before registration/mutation; CLI evidence confirms no registry database or worktrees are created and no ports/providers are exercised.
Interactive `create` now prints a committed-target review and requires typed `yes`; pure noninteractive, JSON and refusal paths are tested with no registry mutation.
`init --update --write --yes` inherits committed project/profile bindings, preserves declarations, merges new reviewed relationships and writes a synced 0600 replacement. Fake/provider tests prove defaults-only resources make zero env-list/update calls, same-invocation project validation is reused, and create JSON reports redacted phase timings. Guided dry-run evidence against the clean external `es-fe` source recognized its Vite TS consumer and preserved its exact `init-devs/evidence-system` backend binding without credentials or cloud writes.

## M5 packaging foundation

 Linux/arm64 QEMU execution passes the release version command and the CGo-disabled state/files/lifecycle suites. Self-executing crash fixtures were skipped because QEMU child re-exec was unavailable; this remains emulated evidence, not hardware arm64 or macOS evidence.

`eve --version` reports 0.1.0. `scripts/release.sh` produces stripped CGo-disabled Linux/macOS amd64/arm64 binaries and `SHA256SUMS.txt`; Linux/amd64 executes expected output, while other artifacts still need physical host validation. docs/UNINSTALL.md now warns against deleting binaries/state before exact cloud cleanup.

## Prerelease publication

MIT is committed as the source license. `scripts/release.sh` now stamps versions through `-X eve/internal/cli.Version=...`, the GitHub release workflow is installed, and its `actions/checkout`/`actions/setup-go` references are pinned by reviewed full commit SHA.

GitHub Actions run [34682001116](https://github.com/danth3b0t/eve/actions/runs/34682001116) passed the credential-free core suite, `go mod verify`, and `go vet -mod=readonly ./...` on `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15`, and `macos-15-intel`. It rebuilt tag `v0.1.0-rc.13`, uploaded all four plain binaries and `SHA256SUMS.txt`, and the reviewed draft was published as a prerelease. The downloaded Linux/amd64 alias asset matched its checksum and responded with `eve 0.1.0-rc.13`; clean `mise exec github:danth3b0t/eve@0.1.0-rc.13 -- eve version` and `MISE_MINIMUM_RELEASE_AGE=0 mise exec github:danth3b0t/eve@latest -- eve version` probes both reported `0.1.0-rc.13`. Alias workflow [34682441045](https://github.com/danth3b0t/eve/actions/runs/34682441045) verified the immutable assets, moved the floating Git `latest` tag, and published the documented full-release `latest` alias with identical assets. Recorded checksums are in [RELEASE.md](RELEASE.md).

Pre-publication candidates exposed completion-only integration issues: generated shell transport/PTY portability, cold 150 ms candidate budgets on the slow Intel runner, and an expensive backend-path tree scan. The final published source narrows static completion metadata while preserving the requested 150 ms target and lifecycle safety boundaries.


## Help and completion refactor

The single Cobra tree now owns public command parsing, registered flags/defaults, help resolution and hidden `__complete`/`__completeNoDesc` transport. Runtime handlers receive typed parsed options; the older `flag.FlagSet` argv reconstruction, raw help-path scanner and penultimate-token flag completion path are removed from production dispatch. Scoped missing/extra-operand errors return exit 2 with only local usage metadata before state initialization.

The completion family has real `bash`, `zsh`, and `setup` children. `eve completion` exits 0 without state; setup selects Bash/Zsh or both through an explicit `$SHELL`-only hint, prints current/persistent/verification/removal guidance, and emits one additive setup JSON envelope without scripts. Bash and Zsh generation uses a reviewed transport adapter over the pinned Cobra v1.10.2 template: argv is passed without request `eval`, returned data is not re-evaluated, old Bash does not reinstall generic filename fallback, and marker mismatches fail closed.

Candidate providers use one 150 ms request deadline, read-only bounded registry/Git/package snapshots, full-UUID GC scopes, command-specific workspace/create/sync/resume policies, local heads/tags/cached remote refs, profiles, projects, backend paths, and declared service IDs. They do not initialize/migrate state, read credential objects, fetch, contact Convex, evaluate dotenvs or mutate startup files. Active Help respects Cobra and `EVE_ACTIVE_HELP=0` degradation.

Regression coverage includes bare/nested/JSON help, mixed interspersed flags, `--` literals, argument/default metadata drift, unsupported shells/flags, setup shell hinting, state/global GC UUID candidates, selector/continuation distinctions, no-description protocol equality, isolated state, Bash 5/Zsh 5.9 syntax checks, real PTY insertion, and actual fzf trigger selection in both shells. `go test ./...` passes on Linux; shell PTY evidence is Linux in this workspace, while shell support remains Linux/macOS scoped.

## Completed-review stabilization

The completed-project review findings now have regression coverage for option dispatch, typed create approval, incomplete create/sync recovery and destruction, provider-attempt journaling, multi-resource creation, journaled endpoint/remote sync, selector audits, incomplete HMAC bootstrap repair, publication subprocess bounds, short Git-scoped repository locks, actionable errors, named and hyphenated credential profiles, global registry access, exact stale admin/branch cleanup and explicit discovery. The focused Convex onboarding proposal is now implemented as well: backend-first/convex-only initialization, exact local and custom binding import, incremental update inheritance, native listener validation/revalidation, defaults-only environment short-circuit, validation reuse, create phase timings/progress, read-only interpolation inventory, Cobra help/completion, scoped GC outcome reporting, and create/sync/resume/destroy dry-runs. The final Linux/amd64 `go test ./...`, `go vet ./...`, module verification, and CGo-disabled/native/browser/race suite pass; the published Linux/amd64 binary has SHA-256 `6f0c5b1b3d3ad83515a872551ad22bb63a6068344dca7af5c9dbadfb2094fdb8`.

Kairo has an external source checkout with unrelated in-flight edits and no authorized EVE credential profile; its ignored Convex state was not scraped, so its live externally-owned validation remains credential-gated, not silently replaced with a fixture claim.

See [M1.md](M1.md), [M2.md](M2.md), [M3.md](M3.md), [M4.md](M4.md), and [RELEASE.md](RELEASE.md) for scope. Stable promotion, signing/notarization, broader topology sync, cross-platform live application evidence and externally owned live-repository evidence remain open.
