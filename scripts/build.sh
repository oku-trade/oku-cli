#!/usr/bin/env bash
# Local reproducible multi-platform build. No publishing, no network uploads.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="${OUT:-dist}"
PLATFORMS="${PLATFORMS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"
export CGO_ENABLED=0
# Reproducibility: fixed mtime source, trimmed paths, no VCS stamping variance.
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || echo 0)}"
mkdir -p "$OUT"
rm -f "$OUT"/oku_*.tar.gz "$OUT"/SHA256SUMS
for p in $PLATFORMS; do
  os="${p%/*}"; arch="${p#*/}"
  name="oku_${VERSION}_${os}_${arch}"
  echo "building $name" >&2
  GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid= -X main.version=${VERSION}" \
    -o "$OUT/$name/oku" ./cmd/oku
  cp README.md "$OUT/$name/" 2>/dev/null || true
  tar --sort=name --mtime="@${SOURCE_DATE_EPOCH}" --owner=0 --group=0 --numeric-owner \
    -C "$OUT" -cf - "$name" | gzip -n > "$OUT/$name.tar.gz"
  rm -rf "${OUT:?}/$name"
done
"$(dirname "$0")/checksums.sh" "$OUT"
