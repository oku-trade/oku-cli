# Releasing

## Build model

| Target | Built on | CGO | Why |
|---|---|---|---|
| linux/amd64, linux/arm64 | any Linux host (cross OK) | 0 | static, encrypted-file credential store |
| darwin/amd64, darwin/arm64 | native macOS host of that arch | 1 | macOS Keychain via `keybase/go-keychain` |

`mise run dist` (`mise-tasks/dist.sh`) refuses darwin targets unless run on a matching native macOS host
(exception: `OKU_DARWIN_CROSS=1` builds darwin/amd64 on an Apple Silicon Mac). By default
it builds both Linux targets on Linux, or the host target on macOS. Override with `PLATFORMS`.

```sh
make dist                                   # linux/amd64 + linux/arm64 (on Linux)
PLATFORMS=darwin/arm64 mise run dist        # on an Apple Silicon Mac
```

Archives: `dist/oku_<version>_<os>_<arch>.tar.gz` + `dist/SHA256SUMS`. Linux builds are
reproducible (`-trimpath -buildvcs=false`, empty build id, deterministic GNU tar keyed to
`SOURCE_DATE_EPOCH`). Darwin CGO builds use bsdtar and link system frameworks; treat them as
not yet verified reproducible. Ad-hoc linker signatures are not Developer ID signing or
notarization, which remain a separate release decision.

A Linux cross-compile is not native acceptance for arm64, and nothing but a native macOS run
exercises the Keychain.

## CI

- **Every push to `master` and every PR** (`.github/workflows/ci.yml`): one job on Ubicloud
  `ubicloud-standard-2` (linux/amd64) running `make test` (go vet, go test, docs-check),
  `go build ./cmd/oku` and the proto drift check (`sync-protos.sh --check`). PR runs cancel
  superseded runs. No race tests, macOS, arm, Nix or packaging here.
- **Dispatch only** (`.github/workflows/ci-full.yml`, Actions > ci-full > Run workflow):
  race tests on `ubicloud-standard-4`, the Nix flake build on `ubicloud-standard-2`, and
  (input `macos`) the disposable-keychain race + lock tests on the Namespace mac.
- **Releases only** (`.github/workflows/github-release.yml`): the full platform matrix, see
  below.

Runner labels: Ubicloud `ubicloud-standard-2` (x86) and `ubicloud-standard-2-arm` (arm64),
per https://www.ubicloud.com/docs/github-actions-integration/runner-types. macOS runs on
Namespace `namespace-profile-ios` (Apple Silicon, same profile as the retail app); set the
repo variable `OKU_MACOS_RUNNER` to override. Labels are listed in `.github/actionlint.yaml`.
`binary-smoke.sh`, `npm-pack.sh` and `npm-smoke.sh` are no longer run per push; the release
workflow still runs `binary-smoke.sh`.

## npm packages (candidate names, pending scope ownership check)

- `@oku-trade/cli`: Node launcher (`npm/cli/bin/oku.js`), `bin: oku`, no install scripts.
- `@oku-trade/cli-{linux,darwin}-{x64,arm64}`: one binary each, with `os`/`cpu` metadata,
  pinned as exact-version `optionalDependencies` of the launcher.

The launcher spawns the binary without a shell, inherits stdio (TTY preserved), forwards
SIGINT/SIGTERM/SIGHUP/SIGQUIT/SIGUSR1/SIGUSR2/SIGWINCH, and mirrors the exit code or re-raises
the child's terminating signal. If the platform package is missing it exits 1 with a message.
`OKU_BINARY_PATH` overrides binary resolution (used by tests).

```sh
mise run npm-pack 0.1.0 npm-dist linux/amd64=path/to/oku   # launcher + given platforms
mise run npm-smoke npm-dist 0.1.0                          # offline install + checks
```

`npm-pack.sh` fails unless each tarball contains exactly `package.json`, `README.md` and the
launcher/binary. `npm-smoke.sh` installs from local tarballs with `--offline` into a temp dir,
then checks `oku version` via bin link and `npx`, argv quoting, stdin, exit codes, signal
forwarding/re-raise, and the `--omit=optional` error. No registry is contacted.

## Publishing (GitHub Releases)

Binaries ship as GitHub Release assets on `oku-trade/oku-cli`. The repo is private, so
downloading needs repo access (a `gh` login or a token).

Cut a release from an up-to-date `master`:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

`.github/workflows/github-release.yml` then:

1. rejects tags that are not semver (`vMAJOR.MINOR.PATCH` or `vMAJOR.MINOR.PATCH-pre`),
2. runs `make test` and `dist.sh` on `ubicloud-standard-2` (linux/amd64),
   `ubicloud-standard-2-arm` (linux/arm64) and one Namespace mac (darwin/arm64 natively,
   darwin/amd64 cross-compiled with CGO via `clang -arch x86_64` and `OKU_DARWIN_CROSS=1`;
   `make test` runs on the three native hosts only), with `VERSION` set to the tag and the commit stamped, so
   `oku version` prints `Oku Agent v0.1.0 (<commit>, <os>/<arch>)`,
3. runs `binary-smoke.sh` on each packaged binary and checks the version string, static
   linking (Linux) or `lipo -archs`, Security.framework + ad-hoc signature (macOS). The
   darwin/amd64 binary is executed under `arch -x86_64` only if Rosetta works on the runner;
   otherwise the run logs that the exec smoke was skipped,
4. requires all four archives, writes one combined `SHA256SUMS`, and creates the release
   with notes generated from commits since the previous tag. Tags with a `-` suffix
   (`v0.2.0-rc.1`, `v0.2.0-beta.1`) are marked pre-release.

5. for stable tags only, the `homebrew-tap` job renders `Formula/oku.rb` with
   `mise-tasks/brew-formula.sh` from the release's `SHA256SUMS` and pushes it to
   `oku-trade/homebrew-tap`. It authenticates with the org GitHub App (`APP_ID` /
   `APP_PRIVATE_KEY` secrets, token scoped to the tap repo) and skips if those secrets are
   missing. Users install with `brew install oku-trade/tap/oku`, which works only while
   the release assets are publicly downloadable.

Only the publish job gets `contents: write`. Assets:

- `oku_<tag>_linux_amd64.tar.gz`, `oku_<tag>_linux_arm64.tar.gz`
- `oku_<tag>_darwin_amd64.tar.gz`, `oku_<tag>_darwin_arm64.tar.gz`
- `SHA256SUMS`

Each archive holds `oku_<tag>_<os>_<arch>/{oku,README.md,THIRD_PARTY_NOTICES}`.

To rebuild an existing tag (e.g. after a flaky runner), run the workflow manually with the
`tag` input. If the release already exists, its assets are replaced (`--clobber`).

### Installing a release

```sh
# needs gh logged in with access to oku-trade/oku-cli, or GITHUB_TOKEN set
gh api -H 'Accept: application/vnd.github.raw' repos/oku-trade/oku-cli/contents/scripts/install.sh | sh
sh scripts/install.sh --version v0.1.0 --dir ~/bin   # from a checkout, pinned version
```

`scripts/install.sh` detects os/arch, downloads with `gh release download` (or curl +
`GITHUB_TOKEN` via the API asset URL), verifies the SHA-256 against `SHA256SUMS`, installs to
`~/.local/bin/oku`, clears the macOS quarantine attribute and prints `oku version`. No sudo.
`--from-dir DIR` installs from a local directory with an archive and `SHA256SUMS` (testing).

To upgrade an existing install, run `oku self-update` (`--check` only compares,
`--version vX.Y.Z` pins a release). It resolves the latest non-draft release through the
GitHub API with the same token order as `install.sh` (`GITHUB_TOKEN`/`GH_TOKEN`, else
`gh auth token`), verifies the archive against `SHA256SUMS`, checks that the new binary's
`version` matches the tag, then atomically renames it over the running executable. It
refuses mise, Nix, Homebrew and source builds. Default timeout is 2m (`--timeout`, max 10m).
`OKU_GITHUB_API_URL` overrides the API base for tests only (https, or http on loopback).
Keep the archive names, the `oku_<tag>_<os>_<arch>/oku` layout and `SHA256SUMS` stable so
older binaries can still upgrade.

### Installing with mise

`mise use -g github:oku-trade/oku-cli` installs from these releases (asset autodetection works with the `oku_<tag>_<os>_<arch>.tar.gz` names; mise strips the archive's top-level directory). The repo is private, so mise needs a token: set `[settings.github] credential_command = 'gh auth token --hostname "$MISE_CREDENTIAL_HOST"'` in `~/.config/mise/config.toml` or export `MISE_GITHUB_TOKEN`. mise's default `minimum_release_age` of 24h hides a brand new release from `latest`; pin `@<version>` to install it immediately. Verified against v0.0.1 on 2026-10-22 with mise 2026.10.15. Keep the archive names and layout stable so this keeps working.

### macOS signing caveat

Darwin binaries are only ad-hoc signed. They are not Developer ID signed or notarized.
Downloads through `gh` or curl usually carry no quarantine attribute and just run. A file
downloaded through a browser is quarantined and Gatekeeper blocks it; fix with:

```sh
xattr -d com.apple.quarantine ~/.local/bin/oku
```

or right-click the binary in Finder and choose Open once.

### Developer ID signing and notarization

The darwin release job signs with Developer ID (hardened runtime, secure timestamp,
identifier `com.oku-trade.oku`) when `MACOS_DEVELOPER_ID_P12` is set, and notarizes when
`APPLE_NOTARY_API_KEY_P8` is also set. Without them it falls back to ad-hoc signing.

| Secret | Value |
|---|---|
| `MACOS_DEVELOPER_ID_P12` | base64 of the exported "Developer ID Application" cert + private key (.p12) |
| `MACOS_DEVELOPER_ID_P12_PASSWORD` | the .p12 export password |
| `APPLE_NOTARY_API_KEY_P8` | App Store Connect API key file contents (.p8) |
| `APPLE_NOTARY_KEY_ID` | that key's ID |
| `APPLE_NOTARY_ISSUER_ID` | the App Store Connect issuer ID |

Bare binaries cannot be stapled, so Gatekeeper checks the notarization ticket online on first run.

### npm publishing (disabled)

`.github/workflows/release.yml` is `workflow_dispatch` only and does nothing unless the repo
variable `NPM_PUBLISH_ENABLED` is `true`. It rebuilds all four targets natively, smoke-tests
them, then (in the `npm-release` environment, which should require reviewers) publishes the
four platform packages followed by the launcher. `dry_run` defaults to true. It uses npm
trusted publishing (OIDC, npm >= 11.5.1). The source repo is private, so npm produces **no
provenance**; none is requested or claimed.

Before enabling, the owner must decide: package scope ownership and names, visibility
(`publishConfig.access` is `restricted`), license (`UNLICENSED` now), a trusted-publisher
config for each of the five packages, and signing/notarization. Publishing public binaries
reveals the distributed software even though the source stays private.

## Status

Validated locally on linux/amd64 only (pack, offline install, binary and launcher smoke with the real
binary). The four-runner CI matrix, Nix CI job and release workflows have not been run yet.
`github-release.yml` is actionlint-clean, and `scripts/install.sh` was tested locally against a
linux/amd64 archive (`--from-dir`, plus the curl path against a stubbed API). The first tag push
is its first real run.

## Hardening notes

- **Smoke coverage** (`mise run binary-smoke`, run on the raw binary and through the npm
  launcher): `--json version`, `--json` usage-error envelope with exit code 2, and, once
  listed by `oku help`, `docs list`/`docs show`, `skills list`, and an MCP `initialize`
  over stdio via `mcp serve`.
- **macOS signing**: `dist.sh` ad-hoc signs darwin binaries (`codesign --sign -`) and runs
  `codesign --verify --strict`. That proves the signature is intact. It is not Developer ID
  signing or notarization, so Gatekeeper still quarantines downloads.
- **Release artifacts**: each platform uploads a separate directory with its own
  `SHA256SUMS.<platform>`. Publish verifies all four manifests and requires the four
  launcher tarballs to be byte-identical (`npm pack` output is deterministic), then publishes
  the linux-x64 copy.
- **Visibility**: `publishConfig.access: restricted` is the safe default only. Private source
  does not require private binaries, so the owner may choose `public` explicitly.
- **Nix**: `ci-full.yml` (dispatch only) builds `.#default`. After any `go.mod`/`go.sum` change, refresh `vendorHash`
  (set `lib.fakeHash`, build, copy the `got:` hash). Without system Nix access, a scratch
  store works: `nix --store "local?root=$DIR" build "path:$PWD#default"`.
- **Third-party notices**: `UNLICENSED` covers only Oku's own code. `mise run third-party-notices`
  walks the pinned module graph actually linked into `cmd/oku` for the exact
  GOOS/GOARCH/CGO target (darwin includes `keybase/go-keychain`). It copies each module's
  LICENSE/NOTICE files verbatim plus the Go standard library license, and fails if any
  module has no license file. `dist.sh` puts `THIRD_PARTY_NOTICES` in every archive,
  `npm-pack.sh` refuses to pack a platform binary without it, and `release.yml` blocks
  publish unless every archive and platform tarball contains it. The launcher is dependency-free
  JS and carries no third-party code. Nobody has done a legal review of the notice contents.
- **Nix job** (`ci-full.yml`): this repo's Actions policy allows only GitHub-owned, verified or allowlisted
  actions, so the job installs `nix-bin` from Ubuntu 24.04's own archive (Nix 2.18.1) rather
  than using a third-party action or a `curl | sh` installer. It runs independently and never
  gates anything. If Ubuntu's Nix can't build the pinned flake, treat the Nix gate as
  blocked. Using a newer Nix installer means adding an action to the allowlist, which needs
  operator approval.
