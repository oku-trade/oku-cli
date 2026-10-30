#!/usr/bin/env bash
#MISE description="Regenerate Go code from the curated account API proto subset"
# The vendored proto files are a trimmed upstream subset (see proto/SOURCE.yaml).
# Update them deliberately when changing the upstream contract, then regenerate.
# Usage: mise run sync-protos [--check]
# Ordinary builds never need this: generated code in pkg/gen is committed.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
case "${1:-}" in
  "") CHECK=0 ;;
  --check) CHECK=1 ;;
  *) echo "usage: $0 [--check]" >&2; exit 2 ;;
esac

rm -rf pkg/gen
buf generate
gofmt -l pkg/gen >/dev/null

if [[ $CHECK -eq 1 ]]; then
  git diff --exit-code -- pkg/gen proto || { echo "generated code drift: run 'mise run sync-protos'" >&2; exit 1; }
  [[ -z "$(git status --porcelain -- pkg/gen)" ]] || { echo "untracked generated files" >&2; exit 1; }
fi
