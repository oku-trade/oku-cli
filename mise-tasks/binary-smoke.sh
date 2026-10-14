#!/usr/bin/env bash
#MISE description="Smoke-test an oku binary or npm launcher offline"
# Exercise an actual oku binary (or the npm-installed launcher) offline.
# Usage: mise run binary-smoke <path-to-oku-or-launcher>
# Commands that exist only after the agent-UX work lands (docs, skills, mcp serve) are
# checked when `oku help` lists them; once listed they must work.
set -euo pipefail
oku="${1:?binary}"
fail() { echo "FAIL: $*" >&2; exit 1; }
has() { grep -q "$1" <<<"$helptext"; }
t="$(mktemp -d)"; trap 'rm -rf "$t"' EXIT
export HOME="$t" XDG_CONFIG_HOME="$t/cfg" XDG_STATE_HOME="$t/state" XDG_DATA_HOME="$t/data"

"$oku" --json version | grep -q '"ok": true' || fail "version --json"
set +e; out="$("$oku" --json definitely-not-a-command 2>/dev/null)"; rc=$?; set -e
[[ $rc == 2 ]] || fail "--json usage error exit $rc (want 2)"
grep -q '"code": "USAGE"' <<<"$out" || fail "--json usage error body: $out"
helptext="$("$oku" help 2>&1)"

if has 'docs list'; then
  "$oku" docs list >/dev/null || fail "docs list"
  first="$("$oku" --json docs list | grep -oE '"(name|id|slug)": *"[^"]+"' | head -1 | sed -E 's/.*"([^"]+)"$/\1/')"
  if [[ -n "$first" ]]; then "$oku" docs show "$first" >/dev/null || fail "docs show $first"; fi
fi
if has 'skills list'; then "$oku" skills list >/dev/null || fail "skills list"; fi
if has 'mcp serve'; then
  req='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"oku-smoke","version":"0"}}}'
  resp="$( (printf '%s\n' "$req"; sleep 2) | timeout 10 "$oku" mcp serve 2>/dev/null | head -1 || true)"
  grep -q '"serverInfo"' <<<"$resp" || fail "mcp initialize: $resp"
fi
echo "binary smoke OK: $oku"
