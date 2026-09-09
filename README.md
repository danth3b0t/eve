# EVE

**Prepare a connected worktree, write native configuration, exit.** Existing root development commands and leaf scripts remain unchanged.

Start with [docs/SPEC.md](docs/SPEC.md), the normative contract and implementation order.

## Status

M0's full Linux Bun/Turbo/Convex live test passes, including inherited development defaults, native launch, keys/expiry and exact cleanup. macOS and regional URL acceptance remain open.

The local-only EVE CLI now supports guarded `create`, `path`, `status`, and `destroy`. Two real worktrees pass unchanged Bun/Turbo/Vite frontend launch tests; stopping one normally and destroying it leaves the other running and preserves its Git branch/source checkout. Production Convex integration and M3–M5 remain pending.

Only explicit mutations are supported, and interactive approval is not implemented in this slice:

```sh
eve create --yes feature/payments       # branch worktree, reservations, native files
eve path feature/payments
eve status feature/payments
eve destroy --yes feature/payments    # stop the ordinary project launcher first
```

Use `--discard-changes` to authorize discarding reviewed user work, and `--assume-stopped` only after separately assessing an occupied claimed port. A listening process is never killed or identified by port.
The repository requires a committed `eve.toml` whose existing applications already consume the declared destinations/keys.

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
