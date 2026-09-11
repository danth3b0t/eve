# Release packaging

**Version:** 0.1.0-rc.5 · **Status:** reviewable prerelease. The four supported core test hosts pass; live Convex/browser evidence is still Linux-scoped and therefore this is not a stable 0.1.0 claim.

## Install

EVE is published through plain GitHub Release artifacts, so release assets contain no hidden installer behavior. Install an exact prerelease with mise:

```sh
mise use -g github:danth3b0t/eve@0.1.0-rc.5
eve version
```

The raw artifacts are also available as `eve-linux-amd64`, `eve-linux-arm64`, `eve-darwin-amd64`, `eve-darwin-arm64`, and `SHA256SUMS.txt`. Checksums protect against transfer corruption only; they are not signatures. If downloading manually rather than through mise, restore the executable bit and verify the relevant SHA-256 line.

## Build

```sh
EVE_VERSION=0.1.0-rc.5 ./scripts/release.sh
# optional target root:
EVE_VERSION=0.1.0-rc.5 EVE_DIST=/path/to/artifacts ./scripts/release.sh
```

The script uses `CGO_ENABLED=0`, `-mod=readonly`, `-trimpath`, `-buildvcs=false` and stripped linker output. It creates:

```text
dist/eve-linux-amd64
dist/eve-linux-arm64
dist/eve-darwin-amd64
dist/eve-darwin-arm64
dist/SHA256SUMS.txt
```

Do not commit `dist/` or generated symbols, and do not serve artifacts from a mutable build directory. There is no updater, telemetry, implicit network call, background service or package-manager magic.

## Runtime requirements

Git is the only required external executable for core EVE operations. Applications still need their ordinary runtimes/package managers (Bun/Node/etc.) managed outside EVE. Live Convex operations need the authorized environment/profile.

## Current prerelease evidence

- License: MIT.
- Source tests: full native, race, CGo-disabled and documentation suites pass on Linux/amd64.
- GitHub Actions release run [34625441310](https://github.com/danth3b0t/eve/actions/runs/34625441310): `go mod verify`, the credential-free core suite, and `go vet -mod=readonly ./...` pass on `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15`, and `macos-15-intel`.
- Actions used reviewed full-length commit pins: `actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1` and `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`.
- Release workflow rebuilt all four binaries from tag `v0.1.0-rc.5`, uploaded them to a draft prerelease, and produced `SHA256SUMS.txt`.
- Downloaded Linux/amd64 asset checksum verification passed, and it reports `eve 0.1.0-rc.5`:
  - `c2015d2f129030d6b8446e713ff801b8f24c85ec270711d6637caeb0a2789100` — `eve-linux-amd64`
  - `a91f45d888c44947457b7e031590e4ddc21f87d4b6ba7ea4a06509825d01bada` — `eve-linux-arm64`
  - `8d5fd9b71c5b4d46cf6ccb114445162ddfca13b9ee1117c1383288919969e945` — `eve-darwin-amd64`
  - `2cf962cd2d1839f470fdb5cb86dfd637816a8c7e0856fe03c156a328777e1d33` — `eve-darwin-arm64`
- Cobra transparency: full help routes, read-only bounded context, Bash/Zsh completion, dynamic workspace/profile/Git candidate degradation, shared `effects`, targeted GC scopes/outcomes, and create/sync/resume/destroy dry-runs are covered in the core suite.
- Guided Convex onboarding: backend-only/resource-only manifests, local/custom URL key import without values, reviewed incremental manifest updates, provider validation reuse, defaults-only remote-env behavior, and redacted create phase timings are covered in the core suite.
- Native fixture: unchanged Bun/Turbo/Vite/Convex launch succeeds; real browser validation passes with the optional `agent-browser` harness only.
- Remote Convex: default region and `aws-eu-west-1` production lifecycle passes with exact cleanup from the pre-release Linux validation suite.

## Remaining stable-release gates

- Promote only after an explicit stable release decision and a fresh immutable version/tag; do not retag or reuse assets between releases.
- Run the full native/browser Convex lifecycle on every platform that will be advertised as stable-support.
- Exercise one externally owned existing monorepo with an explicitly authorized live credential.
- Decide signing/notarization policy; checksums are only integrity metadata.
- Complete uninstall cleanup evidence and honest support documentation.
