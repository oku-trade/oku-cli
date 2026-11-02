# Releasing oku

This is a maintainer guide. Ordinary downloads use public GitHub Releases and require no token.

1. Run `make test`, including `make docs-check`, before tagging. Tag a semver version such as `v0.1.0`.
2. `.github/workflows/github-release.yml` builds and smoke-tests Linux amd64/arm64 and signed macOS arm64/amd64 archives. The `package-linux` job uses pinned nFPM to make `.deb` and `.rpm` packages from those Linux archives and installs amd64 packages inside Debian and Fedora containers. `publish` attaches all eight artifacts and a combined `SHA256SUMS` to the GitHub Release.
3. Stable tags run the `homebrew-tap`, `aur` and `npm` jobs after `publish`, in the protected `release` environment. Pre-release tags do not update those channels. Verify assets and checksums before announcing the release.
4. Test installs using `scripts/install.sh --version <tag>`, `oku version`, and the package manager paths in [the install guide](install.md).

The Homebrew job needs the `APP_ID` and `APP_PRIVATE_KEY` release secrets for the GitHub App with access to `oku-trade/homebrew-tap`. Without both, it logs a skip. The AUR job needs `AUR_SSH_PRIVATE_KEY` in the release environment for the `oku-bin` repository. Its SSH host key is pinned in `packaging/aur_known_hosts`. It logs a skip if the key is absent. `AUR_GIT_NAME` and `AUR_GIT_EMAIL` are optional repository variables for the commit identity.

The npm job requires repository variable `NPM_PUBLISH_ENABLED=true` and an npm trusted publisher configured for **each** of the five `@gfxlabs/oku-cli*` packages, pointing to `oku-trade/oku-cli`, `.github/workflows/github-release.yml` and the `release` environment. Permit `npm stage publish` on each trust relationship. It uses GitHub-hosted Ubuntu, Node 24, npm 11.19+, OIDC (`id-token: write`) and no npm token. It verifies the published release artifacts against `SHA256SUMS`, packs them and smokes the Linux launcher, then stages four platform packages before the launcher. The job logs a skip while the variable is not enabled. A `workflow_dispatch` run with `npm_dry_run=true` packs and checks but does not stage. npm trusted publishing cannot list staged versions with its trust token, so a rerun that encounters an already staged version fails visibly rather than claiming it succeeded. Already *published* versions are skipped.

**Staging is not publication.** A maintainer must inspect and approve the staged packages on npmjs.com. Approve all four platform packages before approving the launcher because it pins their exact version. Confirm registry provenance and installation after approval.

The macOS build uses Developer ID signing when the workflow is configured with a signing identity, otherwise it is ad-hoc signed. Notarization runs only when notary configuration is available. Check each run and artifact before claiming either status. Ad-hoc signing is not notarization.
