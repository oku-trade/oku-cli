#!/usr/bin/env bash
# Compare generated AUR metadata without requiring makepkg or an Arch host.
set -euo pipefail
pkg="${1:?PKGBUILD}"; info="${2:?SRCINFO}"
# The generated PKGBUILD is trusted repository output, not untrusted user input.
source "$pkg"
grep -Fxq "# Maintainer: ${AUR_GIT_NAME:-oku release bot} <${AUR_GIT_EMAIL:-oku-release-bot@users.noreply.github.com}>" "$pkg"
for completion in 'bash-completion/completions/oku' 'zsh/site-functions/_oku' 'fish/vendor_completions.d/oku.fish'; do
  grep -Fq "\$pkgdir/usr/share/$completion" "$pkg"
done
grep -Fxq "pkgname = $pkgname" "$info"
for field in pkgver pkgrel pkgdesc url; do
  grep -Fxq "$(printf '\t%s = %s' "$field" "${!field}")" "$info"
done
for field in arch license provides conflicts options source_x86_64 source_aarch64 sha256sums_x86_64 sha256sums_aarch64; do
  declare -n values="$field"
  for value in "${values[@]}"; do
    grep -Fxq "$(printf '\t%s = %s' "$field" "$value")" "$info"
  done
  test "$(grep -c "^[[:space:]]*${field} = " "$info")" = "${#values[@]}"
done
printf 'AUR metadata agrees\n'
