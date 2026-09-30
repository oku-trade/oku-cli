#!/usr/bin/env bash
# Vendor the account API proto closure used by the CLI and regenerate Go code.
# Usage: ACCOUNT_REPO=/path/to/account scripts/sync-protos.sh [--check]
# Ordinary builds never need this: generated code in pkg/gen is committed.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CHECK=0; [[ "${1:-}" == "--check" ]] && CHECK=1

if [[ $CHECK -eq 0 ]]; then
  : "${ACCOUNT_REPO:?set ACCOUNT_REPO to a local oku-trade/account checkout}"
  FILES=(
    gfxcafe/oku/account/v1/validate.proto
    gfxcafe/oku/account/v2/validate.proto
    gfxcafe/oku/account/v2/perps.proto
    gfxcafe/oku/account/v2/cli_device.proto
  )
  rm -rf proto/gfxcafe proto/buf
  for f in "${FILES[@]}"; do
    mkdir -p "proto/$(dirname "$f")"
    cp "$ACCOUNT_REPO/proto/$f" "proto/$f"
  done
  mkdir -p proto/buf/validate
  cp "$ACCOUNT_REPO/third_party/protovalidate/buf/validate/validate.proto" proto/buf/validate/
  cp "$ACCOUNT_REPO/third_party/protovalidate/LICENSE" proto/buf/validate/LICENSE
  {
    echo "upstream: github.com/oku-trade/account"
    echo "commit: $(git -C "$ACCOUNT_REPO" rev-parse HEAD)"
    echo "protovalidate_version: $(cat "$ACCOUNT_REPO/third_party/protovalidate/VERSION")"
    echo "files:"
    for f in "${FILES[@]}" buf/validate/validate.proto; do echo "  - $f"; done
  } > proto/SOURCE.yaml
fi

rm -rf pkg/gen
buf generate
gofmt -l pkg/gen >/dev/null

if [[ $CHECK -eq 1 ]]; then
  git diff --exit-code -- pkg/gen proto || { echo "generated code drift: run scripts/sync-protos.sh" >&2; exit 1; }
  [[ -z "$(git status --porcelain -- pkg/gen)" ]] || { echo "untracked generated files" >&2; exit 1; }
fi
