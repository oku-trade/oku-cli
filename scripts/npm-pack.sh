#!/usr/bin/env bash
# Build npm tarballs locally. Never publishes.
# Usage: scripts/npm-pack.sh <version> <out-dir> [os/arch=path/to/oku ...]
#   Always packs the launcher. Packs one platform package per os/arch=binary argument.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:?version (semver, no leading v)}"; OUT="${2:?out dir}"; shift 2
VERSION="${VERSION#v}"
mkdir -p "$OUT"; OUT="$(cd "$OUT" && pwd)"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT

npm_cpu() { case "$1" in amd64) echo x64;; arm64) echo arm64;; *) echo "bad arch $1" >&2; exit 2;; esac; }

# verify the tarball contains exactly the allowlisted files
check_allowlist() {
  local tgz="$1"; shift
  local got want
  got="$(tar -tzf "$tgz" | sort)"
  want="$(printf 'package/%s\n' "$@" | sort)"
  if [[ "$got" != "$want" ]]; then
    printf 'allowlist mismatch in %s\n got:\n%s\n want:\n%s\n' "$tgz" "$got" "$want" >&2
    exit 1
  fi
}

# launcher
mkdir -p "$work/cli/bin"
cp "$root/npm/cli/bin/oku.js" "$work/cli/bin/"; cp "$root/npm/README.md" "$work/cli/README.md"
node -e '
  const fs=require("fs"),[f,v]=process.argv.slice(1),p=JSON.parse(fs.readFileSync(f));
  p.version=v; for (const k of Object.keys(p.optionalDependencies)) p.optionalDependencies[k]=v;
  fs.writeFileSync(f,JSON.stringify(p,null,2)+"\n");' \
  "$(cp "$root/npm/cli/package.json" "$work/cli/package.json"; echo "$work/cli/package.json")" "$VERSION"
tgz="$(cd "$work/cli" && npm pack --silent --pack-destination "$OUT")"
check_allowlist "$OUT/$tgz" bin/oku.js README.md package.json
echo "$OUT/$tgz"

for spec in "$@"; do
  plat="${spec%%=*}"; bin="${spec#*=}"
  os="${plat%/*}"; cpu="$(npm_cpu "${plat#*/}")"
  d="$work/$os-$cpu"; mkdir -p "$d/bin"
  install -m755 "$bin" "$d/bin/oku"; cp "$root/npm/README.md" "$d/README.md"
  sed -e "s/@OS@/$os/g" -e "s/@CPU@/$cpu/g" -e "s/@VERSION@/$VERSION/g" \
    "$root/npm/platform/package.json.tmpl" > "$d/package.json"
  tgz="$(cd "$d" && npm pack --silent --pack-destination "$OUT")"
  check_allowlist "$OUT/$tgz" bin/oku README.md package.json
  echo "$OUT/$tgz"
done
