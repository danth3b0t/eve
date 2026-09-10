# Release packaging

**Version:** 0.1.0 · **Status:** source-level packaging foundation. Linux/amd64 is the only fully executed runtime platform. All four architecture artifacts build with CGo disabled; macOS and arm64 tests still need real hosts before publication.

Only Linux/amd64 should be presented as a supported runtime until the physical-host gates pass. `eve-linux-arm64`, `eve-darwin-amd64` and `eve-darwin-arm64` are preview artifacts, not supported binaries.
## Build

```sh
./scripts/release.sh
# optional target root:
EVE_DIST=/path/to/artifacts ./scripts/release.sh
```

The script uses `CGO_ENABLED=0`, `-mod=readonly`, `-trimpath`, `-buildvcs=false` and stripped linker output. It creates:

```text
dist/eve-linux-amd64
dist/eve-linux-arm64
dist/eve-darwin-amd64
dist/eve-darwin-arm64
dist/SHA256SUMS.txt
```

Do not commit `dist/` or generated symbols, and do not serve artifacts from a mutable build directory. Verify checksums after controlled network/disk transfer, not as a security signature. There is no updater, telemetry, implicit network call, background service or package-manager magic.

## Runtime requirements

Git is the only required external executable for core EVE operations. Applications still need their ordinary runtimes/package managers (Bun/Node/etc.) managed outside EVE. Live Convex operations need the authorized environment/profile.

## Current release evidence

- Source tests: full native, race, CGo-disabled and documentation suites pass on Linux/amd64.
- Native fixture: unchanged Bun/Turbo/Vite/Convex launch succeeds; real browser validation passes with the optional `agent-browser` harness only.
- Remote: default region and `aws-eu-west-1` production lifecycle passes with exact cleanup.
- Binaries: static ELF Linux amd64/arm64 and Mach-O macOS amd64/arm64 build with the expected Go module set. Only the Linux/amd64 artifact was executed (`eve 0.1.0`).
- Linux/arm64: release binary responds with `eve 0.1.0` under QEMU; state/files/lifecycle CGo-disabled tests pass. This is emulated evidence with explicitly skipped self-exec helpers, not hardware arm64 validation.

## Remaining publication gates

- Execute the actual macOS amd64/arm64 and Linux arm64 artifacts, including filesystem/locking behavior.
- Run the full native/browser Convex lifecycle on every platform that will be advertised as supported.
- Exercise one externally owned existing monorepo with an explicitly authorized live credential; the local Kairo checkout currently has unowned in-flight changes and no EVE credential profile, so ignored Convex state was not scraped.
- Obtain explicit signatures/notarization policy if required; checksums are only integrity metadata.
- Complete uninstall cleanup evidence and honest support documentation.
