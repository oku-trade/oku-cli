#!/usr/bin/env bash
#MISE description="Reproducible multi-platform release archives in dist/"
# Local reproducible multi-platform build. No publishing, no network uploads.
# Linux: CGO_ENABLED=0 (cross-compilable). Darwin: CGO_ENABLED=1 (macOS Keychain via
# keybase/go-keychain), which requires building on a native macOS host of the target arch.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="${OUT:-dist}"
host="$(go env GOOS)/$(go env GOARCH)"
if [[ -z "${PLATFORMS:-}" ]]; then
  PLATFORMS="linux/amd64 linux/arm64"
  [[ "$(go env GOOS)" == darwin ]] && PLATFORMS="$host"
fi
# Short commit of the source tree; deterministic for a given checkout, so reproducibility holds.
COMMIT="${COMMIT:-$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}"
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || echo 0)}"
mkdir -p "$OUT"
for p in $PLATFORMS; do
  os="${p%/*}"; arch="${p#*/}"
  cgo=0
  cross_env=()
  if [[ "$os" == darwin ]]; then
    cgo=1
    if [[ "$p" != "$host" ]]; then
      # Opt-in: darwin/amd64 cross-compiled with CGO on an Apple Silicon host (clang -arch x86_64
      # against the same macOS SDK). Used by the release workflow; no Intel runner needed.
      if [[ "${OKU_DARWIN_CROSS:-}" == 1 && "$p" == darwin/amd64 && "$host" == darwin/arm64 ]]; then
        cross_env=(CC="clang -arch x86_64" CGO_CFLAGS="-arch x86_64" CGO_LDFLAGS="-arch x86_64" SDKROOT="$(xcrun --sdk macosx --show-sdk-path)")
      else
        echo "refusing $p on $host: darwin builds need CGO on a native macOS $arch host (or OKU_DARWIN_CROSS=1 for darwin/amd64 on darwin/arm64)" >&2
        exit 2
      fi
    fi
  fi
  name="oku_${VERSION}_${os}_${arch}"
  echo "building $name (CGO_ENABLED=$cgo)" >&2
  rm -f "$OUT/$name.tar.gz"
  env ${cross_env[@]+"${cross_env[@]}"} CGO_ENABLED=$cgo GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid= -X main.version=${VERSION} -X github.com/oku-trade/oku-cli/pkg/buildinfo.Commit=${COMMIT}" \
    -o "$OUT/$name/oku" ./cmd/oku
  if [[ "$os" == darwin ]]; then
    if [[ -n "${OKU_CODESIGN_IDENTITY:-}" ]]; then
      # Developer ID with hardened runtime and secure timestamp, both required for notarization.
      codesign --force --sign "$OKU_CODESIGN_IDENTITY" --options runtime --timestamp \
        --identifier com.oku-trade.oku "$OUT/$name/oku"
    else
      # Ad-hoc signature only (integrity/arm64 launch requirement). NOT Developer ID, NOT notarized.
      codesign --force --sign - "$OUT/$name/oku"
    fi
    codesign --verify --strict --verbose=2 "$OUT/$name/oku"
    if [[ -n "${OKU_NOTARY_KEY_PATH:-}" ]]; then
      # Bare Mach-O binaries cannot be stapled; Gatekeeper fetches the ticket online.
      ditto -c -k "$OUT/$name/oku" "$OUT/$name.notarize.zip"
      xcrun notarytool submit "$OUT/$name.notarize.zip" --wait --timeout 20m \
        --key "$OKU_NOTARY_KEY_PATH" --key-id "${OKU_NOTARY_KEY_ID:?}" --issuer "${OKU_NOTARY_ISSUER:?}"
      rm -f "$OUT/$name.notarize.zip"
    fi
  fi
  cp README.md "$OUT/$name/" 2>/dev/null || true
  cp LICENSE "$OUT/$name/LICENSE"
  # Required: license notices for bundled deps of this exact target (fails if incomplete).
  CGO_ENABLED=$cgo GOOS="$os" GOARCH="$arch" "$(dirname "$0")/third-party-notices.sh" "$OUT/$name/THIRD_PARTY_NOTICES"
  # GNU tar flags for determinism; bsdtar on macOS lacks --sort, so fall back.
  if tar --version 2>/dev/null | grep -q GNU; then
    tar --sort=name --mtime="@${SOURCE_DATE_EPOCH}" --owner=0 --group=0 --numeric-owner \
      -C "$OUT" -cf - "$name" | gzip -n > "$OUT/$name.tar.gz"
  else
    tar --uid 0 --gid 0 -C "$OUT" -cf - "$name" | gzip -n > "$OUT/$name.tar.gz"
  fi
  rm -rf "${OUT:?}/$name"
done
"$(dirname "$0")/checksums.sh" "$OUT"
