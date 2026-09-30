#!/usr/bin/env bash
# Write and verify SHA-256 checksums for local archives.
set -euo pipefail
OUT="${1:-dist}"
cd "$OUT"
sha256sum -- *.tar.gz > SHA256SUMS
sha256sum -c SHA256SUMS >&2
