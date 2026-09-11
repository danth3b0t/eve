# EVE

**Prepare a connected worktree, write native configuration, exit.** Existing root development commands and leaf scripts remain unchanged.

Start with [docs/SPEC.md](docs/SPEC.md), the normative contract and implementation order.

## Status

Linux evidence now covers unchanged Bun/Turbo/Vite/Convex start/stop lifecycles, inherited defaults, finite expiry/cleanup, a 31-minute elapsed TTL, default and `aws-eu-west-1` backends, and real browser-rendered EVE configuration. Physical macOS/Apple ARM runtime remains the separate release gate; QEMU Linux/arm64 smoke evidence exists but is emulated.

The guarded CLI supports guided/incremental `init --convex`, `plan`, create phase timings, `path`, `status --refresh`, `resume`, additive and extra-endpoint `sync`, `list`, `doctor --remote`, bounded `gc`, `destroy`, and Convex credential login/status/logout. Interactive creation prompts for typed `yes` on a TTY while JSON remains non-interactive.

Interactive creation shows a preview and asks for typed `yes`; automation uses flags/JSON without prompting:

```sh
eve --version
eve create --yes feature/payments       # branch worktree, reservations, native files
eve init [--convex --project team:project [--profile name]] [--write --yes]
eve init --update [--convex [--backend-path path]] [--write --yes]
eve keys [--json]                         # committed interpolation inventory
eve plan feature/payments
eve path feature/payments
eve status feature/payments [--refresh]
eve resume feature/payments
eve sync feature/payments          # stop the ordinary project launcher first
eve destroy --yes feature/payments    # stop the ordinary project launcher first
eve list [--all]
eve doctor [feature/payments] [--remote]
eve gc [--apply]
eve auth convex login --project team:slug [--token-stdin]
eve auth convex logout [--profile name]
```

Repeating `create` on a prepared branch returns its existing workspace without mutation; incomplete operations require `resume`.
Interactive auth hides input by default; `--token-stdin` remains the automation path.

`init --convex` discovers backend consumers from exact file/key evidence without executing dotenv values or project code. It writes an ordinary reviewable v1 manifest; updates reuse the committed manifest and preserve existing declarations. Convex development defaults remain the source for shared backend defaults and secrets—EVE does not clone them into the manifest. Create JSON includes phase timings for intent, reservations, Git, provider calls, file staging, publication and total preparation.
`eve keys` is the offline reference for humans and LLM agents: it reports exactly the workspace/resource/service interpolation variables supported by the committed manifest, with source and description, while opening no state or cloud connection.
Use `--discard-changes` to authorize discarding reviewed user work, and `--assume-stopped` only after separately assessing an occupied claimed port. A listening process is never killed or identified by port.
The repository requires a committed `eve.toml` whose existing applications already consume the declared destinations/keys.

See [docs/M0.md](docs/M0.md) for evidence and the live runbook, [docs/M1.md](docs/M1.md) for local-core behavior, [docs/M2.md](docs/M2.md) for Convex provider behavior, [docs/M3.md](docs/M3.md) for resume/recovery boundaries, and [docs/M4.md](docs/M4.md) for inspection/diagnostic boundaries.
Release artifacts and uninstall boundaries are documented in [docs/RELEASE.md](docs/RELEASE.md) and [docs/UNINSTALL.md](docs/UNINSTALL.md).

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
