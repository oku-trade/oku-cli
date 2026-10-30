#!/usr/bin/env bash
#MISE description="Install packed npm tarballs offline and exercise the launcher"
# Install locally packed tarballs into a disposable prefix and exercise the launcher.
# Usage: mise run npm-smoke <dir-with-tarballs> <version>
# Uses only local files (no registry fetches: --offline). Never publishes.
set -euo pipefail
DIR="$(cd "${1:?tarball dir}" && pwd)"; VERSION="${2:?version}"; VERSION="${VERSION#v}"
os="$(node -p process.platform)"; cpu="$(node -p process.arch)"
launcher="$DIR/gfxlabs-oku-cli-$VERSION.tgz"
plat="$DIR/gfxlabs-oku-cli-$os-$cpu-$VERSION.tgz"
[[ -f "$launcher" && -f "$plat" ]] || { echo "missing $launcher or $plat" >&2; exit 1; }
t="$(mktemp -d)"; trap 'rm -rf "$t"' EXIT
export npm_config_cache="$t/cache" npm_config_audit=false npm_config_fund=false npm_config_update_notifier=false
fail() { echo "FAIL: $*" >&2; exit 1; }

# 1. full install: platform tarball satisfies the exact-version optionalDependency
mkdir "$t/app"; (cd "$t/app" && npm init -y >/dev/null && npm install --offline --no-save "$plat" "$launcher" >/dev/null)
oku="$t/app/node_modules/.bin/oku"
[[ -x "$oku" ]] || fail "bin link missing"
# other platform packages must not be installed
got_pkgs="$(cd "$t/app/node_modules/@gfxlabs" && ls | LC_ALL=C sort | tr '\n' ' ')"
[[ "$got_pkgs" == "oku-cli oku-cli-$os-$cpu " ]] || fail "unexpected packages: $got_pkgs (want oku-cli oku-cli-$os-$cpu)"
"$(dirname "$0")/binary-smoke.sh" "$oku" || fail "binary smoke through launcher"
(cd "$t/app" && npx --offline --no-install oku --help >/dev/null) || fail "npx"

# 2. argv/stdin/exit preservation, using a stub binary via OKU_BINARY_PATH
stub="$t/stub.sh"
cat > "$stub" <<'S'
#!/bin/sh
for a in "$@"; do printf '[%s]' "$a"; done; printf '\n'
cat
trap 'exit 42' TERM
if [ "${STUB_SLEEP:-}" = 1 ]; then sleep 30 & wait; fi
exit "${STUB_EXIT:-0}"
S
chmod +x "$stub"
out="$(printf 'in-data' | OKU_BINARY_PATH="$stub" "$oku" 'a b' '$(x)' '' '*' ';')"
[[ "$out" == $'[a b][$(x)][][*][;]\nin-data' ]] || fail "argv/stdin: $out"
set +e; OKU_BINARY_PATH="$stub" STUB_EXIT=7 "$oku" </dev/null >/dev/null; rc=$?; set -e
[[ $rc == 7 ]] || fail "exit code $rc"
# signal forwarding: SIGTERM to launcher reaches child (child exits 42)
OKU_BINARY_PATH="$stub" STUB_SLEEP=1 "$oku" </dev/null >/dev/null & pid=$!
sleep 1; kill -TERM $pid; set +e; wait $pid; rc=$?; set -e
[[ $rc == 42 ]] || fail "signal forwarding rc=$rc"
# child killed by signal: launcher re-raises (128+15)
cat > "$stub" <<'S'
#!/bin/sh
kill -TERM $$
S
set +e; OKU_BINARY_PATH="$stub" "$oku" </dev/null; rc=$?; set -e
[[ $rc == 143 ]] || fail "signal re-raise rc=$rc"

# 3. optional deps omitted -> clear error, exit 1
mkdir "$t/noopt"; (cd "$t/noopt" && npm init -y >/dev/null && npm install --offline --no-save --omit=optional "$launcher" >/dev/null 2>&1 || true)
set +e; err="$("$t/noopt/node_modules/.bin/oku" version 2>&1)"; rc=$?; set -e
[[ $rc == 1 && "$err" == *"is not installed"* ]] || fail "missing-platform: rc=$rc $err"
echo "npm smoke OK ($os-$cpu $VERSION)"
