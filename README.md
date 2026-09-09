# EVE

**Prepare a connected worktree, write native configuration, exit.** Existing root development commands and leaf scripts remain unchanged.

Start with [docs/SPEC.md](docs/SPEC.md), the normative contract and implementation order.

## Status

**There is no EVE CLI yet.** M0's full Linux Bun/Turbo/Convex launch and management/key/expiry/cleanup paths have been exercised successfully. The live test still fails its missing project-development-default marker checks; macOS and regional URL validation remain open.

The first M1 slice implements strict Go manifest validation, restricted interpolation, public configuration resolution, and lossless in-memory dotenv editing. Durable state, Git lifecycle, allocation and safe file publication are still pending; M2–M5 have not started.

See [docs/M0.md](docs/M0.md) for live evidence and the runbook, and [docs/M1.md](docs/M1.md) for implemented boundaries and the remaining local-core work.

## Checks

```sh
go test ./...                  # offline core, contract and probe safety checks
go vet ./...
EVE_M0_NATIVE=1 go test ./tests/m0 -count=1 -v
```

The opt-in native test requires Go 1.27.0, Bun 1.4.2, Node, Git, and fixture dependency downloads. These are **test/application dependencies**, not proposed EVE runtime dependencies. Live tests are separately opt-in and create billable, expiring cloud resources; read the runbook first.

## Files

- [schemas/eve.schema.json](schemas/eve.schema.json): parsed v1 TOML schema; semantic checks remain required.
- [schemas/state-schema.sql](schemas/state-schema.sql): initial SQLite design.
- [examples/](examples/): manifests for applications already consuming the declared files/keys.
- [docs/VALIDATION.md](docs/VALIDATION.md): specification artifact checks and engineering status.
- [testdata/native-launch/](testdata/native-launch/): pinned, ordinary Bun/Turbo/Vite/Convex baseline.
- [testdata/provider-contracts/convex/](testdata/provider-contracts/convex/): reviewed API snapshots and provenance.
- [tests/m0/](tests/m0/): engineering probes, not production lifecycle code.
