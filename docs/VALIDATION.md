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

Real Git/SQLite tests kill a subprocess after partial or complete image sets but before SQL acknowledgment. Complete sets reconcile from their original objects; incomplete sets remain unresolved. Tests also cover corruption with restored timestamps, key initialization concurrency, private permissions/link/type checks, bounded reads and value-free metadata/formatting. Linux native/race and CGo-disabled checks pass. Publication, drift-aware sync, snapshot cleanup and power-loss recovery remain unproven.

See [M1.md](M1.md) for precise scope and remaining gates. There is still no EVE CLI, native-file publication engine, complete destroy/recovery lifecycle or production provider adapter.
