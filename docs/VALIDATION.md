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

See [M1.md](M1.md) for precise scope. There is still no EVE CLI, Git lifecycle/publication engine or production provider adapter; the remaining milestones are not marked complete.
