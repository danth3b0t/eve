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

The first supplied-token live runs demonstrated the unfiltered launch, cloud identity/keys/expiry/URLs, key isolation, exact deletion and preservation of project/default identities, but failed the missing-development-default marker checks. After the operator added that default, the full live test passed in 17.05 seconds, including all three inheritance assertions. All seven deployments across those runs were confirmed deleted. Regional URLs, browser execution and macOS remain unverified.

## M1 configuration foundation

Strict Go manifest/user-config parsing, restricted references, public configuration resolution and lossless dotenv image preparation are now implemented. All bundled examples pass the Go validator. Unit tests, bounded fuzz runs, `go vet ./...`, `EVE_M0_NATIVE=1 go test -race ./... -count=1`, and `CGO_ENABLED=0 go test ./...` pass on Linux/amd64. Native tests also consume M1-generated file images through unchanged Bun/Turbo/Vite scripts.

The next slice adds protected SQLite state, immutable creation intents, per-process advisory locks, source/common-directory identity records and TCP block allocation. Real-process tests verify concurrent claims across two repositories, fresh database initialization, SIGKILL lock release, committed-candidate retention and uncommitted SQL rollback. Real IPv4/IPv6 sockets cover occupied ports and stable allocations after late conflicts. Database constraints, per-connection pragmas, private modes, unsafe links, newer-schema refusal and source replacement are tested.

The complete Linux suite, native/race checks and CGo-disabled tests pass. State/allocator test executables also cross-compile with `CGO_ENABLED=0` for all four target OS/architectures; **this does not establish runtime behavior on macOS or arm64**.

## M1 Git creation boundary

The next bounded slice implements Git-aware source registration/planning, pinned committed target manifests, hook-suppressed worktree creation, durable Git intent/identity checkpoints and branch-preserving removal primitives. Real Git/SQLite tests cover moved refs, invoking-worktree isolation, dirty files, ownership refusal and lost-response reconciliation without repeating creation. A blocked checkout filter demonstrated that Git writes its lock reason before checkout finishes; observation now requires a clean worktree, stable regular index and no index lock. Cancellation also terminates the filter's process group.

## M1 read-only file preflight

`PlanGit` now captures bounded source configuration/copy snapshots. `internal/files` selects tracked target content rather than dirty source bytes, validates original path components and actual target tracking/ignore policy, detects changed inputs, and prepares private local-only images without writing application files. Real worktree tests cover these boundaries, limits, links, private diagnostics and target-specific ignore rules. Git's `check-ignore` required explicitly disabling its incompatible global literal-pathspec flag.

Linux tests, native/race checks, repeated file/lifecycle tests and CGo-disabled tests pass. Files/lifecycle test executables cross-compile for all four target OS/architectures; macOS/arm64 runtime behavior remains unverified. In-memory images do not establish protected staging, HMAC drift handling, journaled publication or crash recovery.

## M1 protected image staging

The staging lifecycle now records opaque image references and keyed fingerprints before sensitive writes, verifies synced private objects, and checkpoints staging without publishing application files or completing a generation. Machine-key initialization is serialized; missing/partial/changed keys are never silently regenerated.

Real Git/SQLite tests kill a subprocess after partial or complete image sets but before SQL acknowledgment. Complete sets reconcile from their original objects; incomplete sets remain unresolved. Tests also cover corruption with restored timestamps, key initialization concurrency, private permissions/link/type checks, bounded reads and value-free metadata/formatting. These staging tests alone do not establish publication, drift-aware sync, snapshot cleanup or power-loss recovery.

## M1 initial publication

The production local-only lifecycle now commits `prepared` generation `1` after verified atomic per-file publication. Tests cover fresh Git/filesystem policy, occupied endpoints, missing parents, late parent-link substitution, no-replace rename, exact receipt reconciliation after SIGKILL, user-edit refusal and verified post-completion snapshot cleanup.

`TestPublishedLifecycleNativeFrontends` prepares two real worktrees with production APIs and then runs unchanged Bun/Turbo/Vite frontend commands. Independent ports and public configuration pass; scripts/configuration/lockfiles remain unchanged. Installation and supervision are test-harness actions. The unprovisioned backend is excluded with the existing frontend filters; no live cloud or browser-execution claim follows.

## M1 local-only CLI and destruction

The real `eve` executable now creates, reports and destroys LOCAL-ONLY workspaces through the guarded `--yes` path. Real CLI tests cover consent gates, JSON output, selectors, dirty work, readonly inspection, exact-owned tracked files, branch/source preservation, occupied ports, cleanup pending and port-claim release.

`TestCLILocalLifecycleNativeFrontends` creates two independent monorepo worktrees, starts unchanged Bun/Turbo/Vite commands in both, stops one normally, destroys it, and verifies that the other continues. The stopped harness closes client idle connections before signaling the application; without that, TIME_WAIT can hold an exclusive probe after the process exits. This is Linux frontend evidence only, not browser/Cloud/macOS proof.

## M2 Convex lifecycle

Production `eve` now provisions and destroys exact nondefault Convex dev deployments. An 80.74-second Linux run created two workspaces and two independent five-day deployments, pushed fixture code through the ordinary Convex CLI, verified each backend's configured `SITE_URL`, destroyed the first while the second kept running, then deleted the second and verified that no `dev/eve/` deployment remained. Original project/default identities were preserved. Earlier failed runs left four deployment names; each exact nondefault test-owned resource was verified and deleted.

Offline tests cover sanitized provider failures, exact-reference reconciliation after an ambiguous create, protected deploy-key storage, native selectors, public frontend URLs, remote env selection, remote/local cleanup and secret purge. macOS/regional/browser/TTL-elapsed gates remain open.

## M3 initial resume

`eve resume` now resumes the original operation from durable UUID, revision, manifest, allocation, resource and journal intent. Separate-process tests resume after Git interruption and a test fake proves one deployment is reconciled after an ambiguous create instead of another being created. Prepared resume is idempotent; failed/unknown or dirty states remain diagnostic. Repeating `create` over a prepared branch is exact and mutation-free; `--from` conflicts and incomplete branches route to resume.

Bounded `gc` is report-only by default. `gc --apply` verifies recorded worktree/admin absence before generating a new destroy intent, completes exact remote deletion/key purge and claim release, and refuses recreated paths.


Additive/value-only `sync` runs local and Git drift checks before any remote write, uploads only declared resource env keys, publishes generation 2 with fresh fingerprints, and reports `restart_required`. Tests cover absent bytecode no-ops, unmanaged content, drift/overwrite, remote additive values, and destruction after sync.
## M4 inspection

Read-only `eve list [--all]` reports registered repositories, live workspaces, public ports and public Convex metadata without creating locks, credentials, files or remote calls. CLI tests confirm the same workspace appears with its assigned port and disappears from the default report after exact destruction. Discovery/`init`/`doctor` remain unimplemented.
`eve doctor` verifies registry/Git/current file HMACs, endpoint availability, resource expiry and optional exact remote identity while leaving runtime/loader unread. Tests cover drift warnings and remote identity without environment/key queries.
`eve plan` independently evaluates the committed target before registration/mutation; CLI evidence confirms no registry database or worktrees are created and no ports/providers are exercised.

See [M1.md](M1.md), [M2.md](M2.md), [M3.md](M3.md), and [M4.md](M4.md) for scope. Broader topology sync, discovery/`init`/`doctor` and release packaging are not implemented.
