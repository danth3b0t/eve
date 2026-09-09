# EVE

**Prepare a connected worktree, write native configuration, exit.** Existing root development commands and leaf scripts remain unchanged.

Start with [docs/SPEC.md](docs/SPEC.md), the normative contract and implementation order.

## Status

**There is no EVE CLI yet.** M0's full Linux Bun/Turbo/Convex live test passes, including inherited development defaults, native launch, keys/expiry and exact cleanup. macOS and regional URL acceptance remain open.

M1 includes configuration handling, protected SQLite state, durable TCP allocation, journaled Git creation, file preflight, and protected image staging with HMAC fingerprints. Journaled application-file publication and CLI integration are next; M2–M5 remain pending.

See [docs/M0.md](docs/M0.md) for live evidence and the runbook, and [docs/M1.md](docs/M1.md) for implemented boundaries and the remaining local-core work.

## Checks

```sh
go test ./...                  # offline core, contract and probe safety checks
go vet ./...
EVE_M0_NATIVE=1 go test ./tests/m0 -count=1 -v
```

Core tests require Go and Git. The opt-in native test additionally requires Bun 1.4.2, Node and fixture dependency downloads. EVE's runtime requires Git, not the Go compiler, Bun, Node or application dependencies. Live tests are separately opt-in and create billable, expiring cloud resources; read the runbook first.

## Files

- [schemas/eve.schema.json](schemas/eve.schema.json): parsed v1 TOML schema; semantic checks remain required.
- [schemas/state-schema.sql](schemas/state-schema.sql): embedded initial SQLite schema used by `internal/state`.
- [examples/](examples/): manifests for applications already consuming the declared files/keys.
- [docs/VALIDATION.md](docs/VALIDATION.md): specification artifact checks and engineering status.
- [testdata/native-launch/](testdata/native-launch/): pinned, ordinary Bun/Turbo/Vite/Convex baseline.
- [testdata/provider-contracts/convex/](testdata/provider-contracts/convex/): reviewed API snapshots and provenance.
- [tests/m0/](tests/m0/): engineering probes, not production lifecycle code.
