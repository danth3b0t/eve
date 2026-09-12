# Release packaging

**Version:** 0.1.0-rc.11 · **Status:** reviewable prerelease. The four supported core test hosts pass; live Convex/browser evidence is still Linux-scoped and therefore this is not a stable 0.1.0 claim.

## Install

EVE is published through plain GitHub Release artifacts, so release assets contain no hidden installer behavior. Install an exact prerelease with mise:

```sh
mise use -g github:danth3b0t/eve@0.1.0-rc.11
eve version
```

The raw artifacts are also available as `eve-linux-amd64`, `eve-linux-arm64`, `eve-darwin-amd64`, `eve-darwin-arm64`, and `SHA256SUMS.txt`. Checksums protect against transfer corruption only; they are not signatures. If downloading manually rather than through mise, restore the executable bit and verify the relevant SHA-256 line.

## Build

```sh
EVE_VERSION=0.1.0-rc.11 ./scripts/release.sh
# optional target root:
EVE_VERSION=0.1.0-rc.11 EVE_DIST=/path/to/artifacts ./scripts/release.sh
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

## `latest` alias policy

GitHub's normal `latest` release notation is metadata, not a mutable Git tag: the GitHub REST `releases/latest` endpoint returns the newest published **full** release and excludes drafts/prereleases. EVE retains immutable `vMAJOR.MINOR.PATCH[-suffix]` tags and never moves them after publication.

For compatibility tooling that follows tags or `mise github:...@latest`, `.github/workflows/latest-alias.yml` maintains two deliberate aliases: the floating Git tag `latest` and a mutable full GitHub Release with the same tag/assets. It runs only after an immutable release is actually published (or by workflow-dispatch recovery), verifies the immutable asset checksums, deletes only the previous `latest` alias release, force-moves the Git tag, and uploads identical assets to a fresh alias release with the canonical prerelease link in its notes. It never retags version tags or alters immutable release assets. Concurrency is serialized so clients do not see two competing alias releases.

Because the alias itself is published as a full release, GitHub's `releases/latest` endpoint and default `mise` resolution can return it even while the linked canonical release is still a preview. That is a deliberate documented alias, not a stable-support claim.

## Runtime requirements

Git is the only required external executable for core EVE operations. Applications still need their ordinary runtimes/package managers (Bun/Node/etc.) managed outside EVE. Live Convex operations need the authorized environment/profile.

## Current prerelease evidence

- License: MIT.
- Source tests: full native, race, CGo-disabled and documentation suites pass on Linux/amd64.
- GitHub Actions release run [34660867789](https://github.com/danth3b0t/eve/actions/runs/34660867789): `go mod verify`, the credential-free core suite, and `go vet -mod=readonly ./...` pass on `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15`, and `macos-15-intel`.
- Actions used reviewed full-length commit pins: `actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1` and `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`.
- Release workflow rebuilt all four binaries from tag `v0.1.0-rc.11`, uploaded them to a draft prerelease, and produced `SHA256SUMS.txt`; after draft review the prerelease was published without asset mutation. Alias workflow [34661390465](https://github.com/danth3b0t/eve/actions/runs/34661390465) then moved the documented floating `latest` tag to the immutable tag object.
- Downloaded Linux/amd64 asset checksum verification passed, and it reports `eve 0.1.0-rc.11`; `mise exec github:danth3b0t/eve@0.1.0-rc.11 -- eve version` also installed from GitHub and reported `eve 0.1.0-rc.11`:
  - `39c295b064b7a1f23230a7d7667eebf408faa6427becbf07aa45beba74925da2` — `eve-linux-amd64`
  - `ffcf802213fd431af142e9796c3dcfaea3a69697252fefde051eaa3e9e529408` — `eve-linux-arm64`
  - `ff4826bcbe24359e95b0b8a7f41b9ad7918e75af01ca55da23d5f3d6cc986eda` — `eve-darwin-amd64`
  - `e877f4326a0b6d82234d700a1135226eda5093042a433434ebe27581d1a62361` — `eve-darwin-arm64`
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
