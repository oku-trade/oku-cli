#!/usr/bin/env bash
#MISE description="Write and verify SHA256SUMS for local archives"
# Write and verify SHA-256 checksums for local archives.
set -euo pipefail
OUT="${1:-dist}"
cd "$OUT"
if command -v sha256sum >/dev/null; then sum=(sha256sum); else sum=(shasum -a 256); fi
"${sum[@]}" -- *.tar.gz > SHA256SUMS
"${sum[@]}" -c SHA256SUMS >&2
