#!/bin/sh
# Install oku from a GitHub Release of oku-trade/oku-cli (private repo: needs access).
#
#   scripts/install.sh [--version vX.Y.Z] [--dir DIR] [--from-dir DIR]
#
# Download order: `gh release download` (uses your gh login), else curl + GITHUB_TOKEN
# against the GitHub API. --from-dir installs from a local directory holding the
# archive + SHA256SUMS (used for testing; no network).
# Verifies SHA256 against SHA256SUMS, installs to ~/.local/bin/oku by default. No sudo.
set -eu

REPO="${OKU_REPO:-oku-trade/oku-cli}"
VERSION=""
DIR="${HOME}/.local/bin"
FROM_DIR=""

usage() {
  sed -n '2,9p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'
}
die() { echo "install.sh: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#*=}"; shift ;;
    --dir) [ $# -ge 2 ] || die "--dir needs a value"; DIR="$2"; shift 2 ;;
    --dir=*) DIR="${1#*=}"; shift ;;
    --from-dir) [ $# -ge 2 ] || die "--from-dir needs a value"; FROM_DIR="$2"; shift 2 ;;
    --from-dir=*) FROM_DIR="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

case "$VERSION" in
  ""|v*) ;;
  *) VERSION="v$VERSION" ;;
esac

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac
# Rosetta: prefer the native arm64 build on Apple Silicon.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
  arch=arm64
fi

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t oku-install)"
trap 'rm -rf "$tmp"' EXIT INT TERM

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else die "need sha256sum or shasum"; fi
}

api_get() { # url [accept] -> stdout
  curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" \
    -H "Accept: ${2:-application/vnd.github+json}" \
    -H "X-GitHub-Api-Version: 2022-11-28" "$1"
}

# Resolve the release JSON via the API (curl path only).
release_json() {
  if [ -n "$VERSION" ]; then api_get "https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
  else api_get "https://api.github.com/repos/${REPO}/releases/latest"; fi
}

# Extract the API URL of a named asset from release JSON (no jq dependency).
asset_url() { # json-file asset-name
  # shellcheck disable=SC2020 # intentional: split JSON on , { }
  tr ',{}' '\n\n\n' < "$1" | sed 's/" *: *"/":"/' | awk -v want="\"name\":\"$2\"" '
    /"url":"https:\/\/api.github.com\/repos\/.*\/releases\/assets\// { u=$0 }
    { l=$0; sub(/^[ \t]*/, "", l); sub(/[ \t]*$/, "", l) }
    l == want { sub(/.*"url":"/, "", u); sub(/".*/, "", u); print u; exit }'
}

if [ -n "$FROM_DIR" ]; then
  [ -f "$FROM_DIR/SHA256SUMS" ] || die "$FROM_DIR/SHA256SUMS not found"
  if [ -z "$VERSION" ]; then
    f=""
    for c in "$FROM_DIR"/oku_*_"${os}_${arch}".tar.gz; do [ -f "$c" ] && f="$c"; done
    [ -n "$f" ] || die "no oku_*_${os}_${arch}.tar.gz in $FROM_DIR"
    VERSION="$(basename "$f" | sed "s/^oku_//; s/_${os}_${arch}\.tar\.gz\$//")"
  fi
  asset="oku_${VERSION}_${os}_${arch}.tar.gz"
  cp "$FROM_DIR/SHA256SUMS" "$FROM_DIR/$asset" "$tmp/" || die "missing $FROM_DIR/$asset"
elif command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  if [ -z "$VERSION" ]; then
    VERSION="$(gh release view -R "$REPO" --json tagName -q .tagName)" || die "could not find latest release of $REPO"
  fi
  asset="oku_${VERSION}_${os}_${arch}.tar.gz"
  echo "downloading $asset via gh" >&2
  gh release download "$VERSION" -R "$REPO" -D "$tmp" -p "$asset" -p SHA256SUMS
elif [ -n "${GITHUB_TOKEN:-}" ] && command -v curl >/dev/null 2>&1; then
  release_json > "$tmp/release.json" || die "GitHub API request failed (token lacks access to $REPO, or no such release)"
  if [ -z "$VERSION" ]; then
    VERSION="$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp/release.json" | head -n 1)"
    [ -n "$VERSION" ] || die "could not determine latest release tag"
  fi
  asset="oku_${VERSION}_${os}_${arch}.tar.gz"
  echo "downloading $asset via GitHub API" >&2
  for name in "$asset" SHA256SUMS; do
    url="$(asset_url "$tmp/release.json" "$name")"
    [ -n "$url" ] || die "release $VERSION has no asset $name"
    api_get "$url" application/octet-stream > "$tmp/$name" || die "download of $name failed"
  done
else
  die "need either an authenticated gh CLI (gh auth login) or GITHUB_TOKEN + curl; $REPO is private"
fi

want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")"
[ -n "$want" ] || die "$asset not listed in SHA256SUMS"
got="$(sha256 "$tmp/$asset")"
[ "$want" = "$got" ] || die "checksum mismatch for $asset (want $want, got $got)"
echo "sha256 ok: $asset" >&2

tar -xzf "$tmp/$asset" -C "$tmp"
src="$tmp/oku_${VERSION}_${os}_${arch}/oku"
[ -f "$src" ] || die "archive does not contain oku_${VERSION}_${os}_${arch}/oku"

mkdir -p "$DIR"
cp "$src" "$DIR/oku.tmp.$$"
chmod 755 "$DIR/oku.tmp.$$"
mv -f "$DIR/oku.tmp.$$" "$DIR/oku"
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$DIR/oku" 2>/dev/null || true
fi

echo "installed $DIR/oku" >&2
"$DIR/oku" version
case ":${PATH}:" in
  *":$DIR:"*) ;;
  *) echo "note: $DIR is not on your PATH; add: export PATH=\"$DIR:\$PATH\"" >&2 ;;
esac
