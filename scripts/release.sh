#!/bin/sh
# Build release artifacts. Run only from a reviewed release checkout.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

version=${EVE_VERSION:-}
if [ -z "$version" ]; then
  version=$(git describe --tags --exact-match HEAD 2>/dev/null) || {
    printf '%s\n' 'Set EVE_VERSION or build from an exact release tag.' >&2
    exit 1
  }
fi
version=${version#v}
if ! printf '%s\n' "$version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  printf '%s\n' 'Use a release version such as 0.1.0 or 0.1.0-rc.1.' >&2
  exit 1
fi

# -X can stamp a string variable, not a const. Apply version.patch first.
if ! grep -Eq '^var Version[[:space:]]*=' internal/cli/cli.go; then
  printf '%s\n' 'Change const Version to var Version in internal/cli/cli.go first.' >&2
  exit 1
fi

out=${EVE_DIST:-"$root/dist"}
case "$out" in
  /*) ;;
  *) out="$root/$out" ;;
esac
mkdir -p "$out"

# Resolve the package path so a future module-path rename does not break -X.
cli_package=$(go list -mod=readonly -f '{{.ImportPath}}' ./internal/cli)
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  goos=${target%/*}
  goarch=${target#*/}
  printf 'Building eve %s for %s\n' "$version" "$target"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
    -mod=readonly \
    -trimpath \
    -buildvcs=false \
    -ldflags="-s -w -X ${cli_package}.Version=${version}" \
    -o "$out/eve-${goos}-${goarch}" ./cmd/eve
done

# Smoke-test the matching host binary and detect broken version stamping.
host_os=$(go env GOHOSTOS)
host_arch=$(go env GOHOSTARCH)
host_binary="$out/eve-${host_os}-${host_arch}"
if [ ! -x "$host_binary" ]; then
  printf '%s\n' 'Release builds must run on a supported Linux/macOS amd64/arm64 host.' >&2
  exit 1
fi
actual=$("$host_binary" --version)
if [ "$actual" != "eve $version" ]; then
  printf 'Unexpected release version: %s\n' "$actual" >&2
  exit 1
fi

cd "$out"
set -- eve-linux-amd64 eve-linux-arm64 eve-darwin-amd64 eve-darwin-arm64
# shasum is available on typical macOS developer installations; prefer
# sha256sum when present. These are builder tools, not EVE runtime dependencies.
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$@" > SHA256SUMS.txt
elif command -v shasum >/dev/null 2>&1; then
  shasum -a 256 "$@" > SHA256SUMS.txt
else
  printf '%s\n' 'Install sha256sum or shasum on the release builder.' >&2
  exit 1
fi
printf 'Release artifacts: %s\n' "$out"
cat SHA256SUMS.txt
