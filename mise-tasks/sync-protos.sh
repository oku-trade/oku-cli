#!/usr/bin/env bash
#MISE description="Vendor account API protos and regenerate pkg/gen"
# Vendor the account API proto closure used by the CLI and regenerate Go code.
# Usage: ACCOUNT_REPO=/path/to/account mise run sync-protos [--check]
# Ordinary builds never need this: generated code in pkg/gen is committed.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CHECK=0; [[ "${1:-}" == "--check" ]] && CHECK=1

if [[ $CHECK -eq 0 ]]; then
  : "${ACCOUNT_REPO:?set ACCOUNT_REPO to a local oku-trade/account checkout}"
  FILES=(
    gfxcafe/oku/account/v1/validate.proto
    gfxcafe/oku/account/v1/auth.proto
    gfxcafe/oku/account/v1/common.proto
    gfxcafe/oku/account/v1/orderhistory.proto
    gfxcafe/oku/account/v2/validate.proto
    gfxcafe/oku/account/v2/perps.proto
    gfxcafe/oku/account/v2/cli_device.proto
    gfxcafe/oku/account/v2/orders.proto
    gfxcafe/oku/account/v2/simpleswap.proto
    gfxcafe/oku/account/v2/bridge.proto
    gfxcafe/oku/account/v2/token.proto
    gfxcafe/oku/account/v2/onchain.proto
    gfxcafe/oku/account/v2/portfolio.proto
  )
  rm -rf proto/gfxcafe proto/buf
  for f in "${FILES[@]}"; do
    mkdir -p "proto/$(dirname "$f")"
    if [[ -f "$ACCOUNT_REPO/proto/$f" ]]; then
      cp "$ACCOUNT_REPO/proto/$f" "proto/$f"
    elif [[ "$f" == gfxcafe/oku/account/v2/cli_device.proto || "$f" == gfxcafe/oku/account/v1/auth.proto || "$f" == gfxcafe/oku/account/v1/common.proto ]]; then
      git show "HEAD:proto/$f" > "proto/$f"
    else
      echo "missing upstream proto: $f" >&2; exit 1
    fi
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
  git diff --exit-code -- pkg/gen proto || { echo "generated code drift: run 'mise run sync-protos'" >&2; exit 1; }
  [[ -z "$(git status --porcelain -- pkg/gen)" ]] || { echo "untracked generated files" >&2; exit 1; }
fi
