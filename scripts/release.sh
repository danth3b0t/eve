#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out=${EVE_DIST:-"$root/dist"}
mkdir -p "$out"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
 goos=${target%/*}
 goarch=${target#*/}
 name="eve-${goos}-${goarch}"
 CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build \
  -mod=readonly \
  -trimpath \
  -buildvcs=false \
  -ldflags='-s -w' \
  -o "$out/$name" ./cmd/eve
done
cd "$out"
sha256sum eve-linux-amd64 eve-linux-arm64 eve-darwin-amd64 eve-darwin-arm64 | sort -k2 > SHA256SUMS.txt
printf '%s\n' "$out/SHA256SUMS.txt"
cat "$out/SHA256SUMS.txt"
