# Release packaging

**Version:** 0.1.0-rc.10 · **Status:** reviewable prerelease. The four supported core test hosts pass; live Convex/browser evidence is still Linux-scoped and therefore this is not a stable 0.1.0 claim.

## Install

EVE is published through plain GitHub Release artifacts, so release assets contain no hidden installer behavior. Install an exact prerelease with mise:

```sh
mise use -g github:danth3b0t/eve@0.1.0-rc.10
eve version
```

The raw artifacts are also available as `eve-linux-amd64`, `eve-linux-arm64`, `eve-darwin-amd64`, `eve-darwin-arm64`, and `SHA256SUMS.txt`. Checksums protect against transfer corruption only; they are not signatures. If downloading manually rather than through mise, restore the executable bit and verify the relevant SHA-256 line.

## Build

```sh
EVE_VERSION=0.1.0-rc.10 ./scripts/release.sh
# optional target root:
EVE_VERSION=0.1.0-rc.10 EVE_DIST=/path/to/artifacts ./scripts/release.sh
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
- GitHub Actions release run [34657137161](https://github.com/danth3b0t/eve/actions/runs/34657137161): `go mod verify`, the credential-free core suite, and `go vet -mod=readonly ./...` pass on `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15`, and `macos-15-intel`.
- Actions used reviewed full-length commit pins: `actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1` and `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`.
- Release workflow rebuilt all four binaries from tag `v0.1.0-rc.10`, uploaded them to a draft prerelease, and produced `SHA256SUMS.txt`; after draft review the prerelease was published without asset mutation.
- Downloaded Linux/amd64 asset checksum verification passed, and it reports `eve 0.1.0-rc.10`; `mise exec github:danth3b0t/eve@0.1.0-rc.10 -- eve version` also installed from GitHub and reported `eve 0.1.0-rc.10`:
  - `10aa7d4fbd3e735a427106f4b354f67fe5fb96cfee0b7670b38774793f441125` — `eve-linux-amd64`
  - `344e96b5c5275ccecf785f1530d08cfee4b5c1b1b6869e9347b44b65cd290c73` — `eve-linux-arm64`
  - `69c84fc11d8ea1714a6af1e040bd9211fea4301649a85035e38bd51a724a545a` — `eve-darwin-amd64`
  - `74c07d8b9430ad3b3075e02498f5a19d741b7d9dc0c6c35b16f1a896556a5fdd` — `eve-darwin-arm64`
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
