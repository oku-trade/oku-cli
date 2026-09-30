#!/usr/bin/env bash
# Generate THIRD_PARTY_NOTICES for the modules actually linked into ./cmd/oku for the
# current GOOS/GOARCH/CGO_ENABLED (so darwin CGO builds include keybase/go-keychain).
# Uses only the Go toolchain's pinned module graph (go list -deps). It copies each module's
# LICENSE/COPYING/NOTICE files verbatim and FAILS if any linked module has none, so a
# release cannot silently ship without notices. The Go standard library license is included.
# Usage: scripts/third-party-notices.sh <out-file>
set -euo pipefail
cd "$(dirname "$0")/.."
out="${1:?out file}"
main="$(go list -m)"
mods="$(go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}}{{"\t"}}{{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}' ./cmd/oku | sort -u)"
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
(
  echo "Third-party notices for oku ($(go env GOOS)/$(go env GOARCH), CGO_ENABLED=$(go env CGO_ENABLED))"
  echo "The oku program itself ($main) is proprietary; these notices cover bundled dependencies."
  echo
  echo "================================================================"
  echo "Go standard library and runtime ($(go env GOVERSION))"
  echo "================================================================"
  cat "$(go env GOROOT)/LICENSE"
  echo
  missing=0
  while IFS=$'\t' read -r mod dir; do
    [[ -n "$mod" ]] || continue
    files=()
    while IFS= read -r f; do files+=("$f"); done < <(
      find "$dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) | LC_ALL=C sort)
    if (( ${#files[@]} == 0 )); then echo "no license file for $mod ($dir)" >&2; missing=1; continue; fi
    echo "================================================================"
    echo "$mod"
    echo "================================================================"
    for f in "${files[@]}"; do echo "--- $(basename "$f")"; cat "$f"; echo; done
  done <<<"$mods"
  exit $missing
) > "$tmp" || { echo "third-party notices incomplete; refusing" >&2; exit 1; }
mv "$tmp" "$out"; trap - EXIT
echo "wrote $out ($(grep -c '^================' "$out") headers)" >&2
