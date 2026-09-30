#!/usr/bin/env bash
# Run a command with a disposable default Keychain on a fresh hosted macOS CI runner.
# Usage: OKU_KEYCHAIN_TEST_ACK=disposable-runner scripts/macos-keychain-test.sh <cmd> [args...]
# Refuses to run unless GITHUB_ACTIONS=true, CI=true and the acknowledgement are all set, so
# it never touches a developer's login keychain. It captures the default keychain and search
# list, creates a uniquely named keychain, makes it default for the subprocess, and the trap
# restores the originals and deletes only the keychain this script created.
# Exported to the subprocess:
#   OKU_TEST_KEYCHAIN           absolute path of the disposable keychain (password not exported)
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo "macOS only" >&2; exit 2; }
if [[ "${GITHUB_ACTIONS:-}" != true || "${CI:-}" != true || "${OKU_KEYCHAIN_TEST_ACK:-}" != disposable-runner ]]; then
  echo "refusing: requires GITHUB_ACTIONS=true CI=true OKU_KEYCHAIN_TEST_ACK=disposable-runner" >&2
  exit 2
fi
(( $# > 0 )) || { echo "usage: $0 <cmd> [args...]" >&2; exit 2; }

orig_default="$(security default-keychain -d user | sed -E 's/^ *"(.*)"$/\1/')"
orig_list=()
while IFS= read -r l; do orig_list+=("$(sed -E 's/^ *"(.*)"$/\1/' <<<"$l")"); done \
  < <(security list-keychains -d user)

kc="$HOME/Library/Keychains/oku-ci-$(date +%s)-$$-$RANDOM.keychain-db"
pw="$(openssl rand -hex 24)"
created=0
restore() {
  set +e
  security default-keychain -d user -s "$orig_default"
  security list-keychains -d user -s "${orig_list[@]}"
  # delete-keychain works on a locked keychain; fall back to removing our own file.
  if [[ $created == 1 ]]; then security delete-keychain "$kc" || rm -f -- "$kc"; fi
  [[ -e "$kc" ]] && echo "WARNING: failed to delete $kc" >&2
  echo "keychain restored: default=$(security default-keychain -d user)" >&2
}
trap restore EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

[[ ! -e "$kc" ]] || { echo "keychain path exists: $kc" >&2; exit 1; }
security create-keychain -p "$pw" "$kc"
created=1
security set-keychain-settings -lut 3600 "$kc"   # auto-lock after 1h, lock on sleep
security unlock-keychain -p "$pw" "$kc"
security list-keychains -d user -s "$kc" "${orig_list[@]}"
security default-keychain -d user -s "$kc"
echo "disposable keychain: $kc" >&2

set +e
# Child inherits GITHUB_ACTIONS/CI/OKU_KEYCHAIN_TEST_ACK (verified above) plus the path.
# The password is not exported. The child may leave the keychain locked; restore() and
# delete-keychain need no unlock.
OKU_TEST_KEYCHAIN="$kc" OKU_KEYCHAIN_TEST_ACK="$OKU_KEYCHAIN_TEST_ACK" "$@"
rc=$?
set -e
exit $rc
