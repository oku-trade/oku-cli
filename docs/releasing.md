# Releasing

## Build model

| Target | Built on | CGO | Why |
|---|---|---|---|
| linux/amd64, linux/arm64 | any Linux host (cross OK) | 0 | static, encrypted-file credential store |
| darwin/amd64, darwin/arm64 | native macOS host of that arch | 1 | macOS Keychain via `keybase/go-keychain` |

`scripts/build.sh` refuses darwin targets unless run on a matching native macOS host. By default
it builds both Linux targets on Linux, or the host target on macOS. Override with `PLATFORMS`.

```sh
make dist                                   # linux/amd64 + linux/arm64 (on Linux)
PLATFORMS=darwin/arm64 ./scripts/build.sh   # on an Apple Silicon Mac
```

Archives: `dist/oku_<version>_<os>_<arch>.tar.gz` + `dist/SHA256SUMS`. Linux builds are
reproducible (`-trimpath -buildvcs=false`, empty build id, deterministic GNU tar keyed to
`SOURCE_DATE_EPOCH`). Darwin CGO builds use bsdtar and link system frameworks; treat them as
not yet verified reproducible. Ad-hoc linker signatures are not Developer ID signing or
notarization, which remain a separate release decision.

A Linux cross-compile is not native acceptance for arm64, and nothing but a native macOS run
exercises the Keychain.

## CI

`.github/workflows/ci.yml` runs on every push/PR (no branch filter, so `master` or `main`
both work) across four GitHub-hosted native runners: `ubuntu-24.04`, `ubuntu-24.04-arm`,
`macos-15-intel`, `macos-15`. Each job asserts it is on the native host, runs `make test`,
builds the archive, runs the packaged binary, and packs + smoke-installs the npm tarballs.
Confirm label availability and minutes budget for the organization (private repos bill
macOS minutes at a multiplier).

## npm packages (candidate names, pending scope ownership check)

- `@oku-trade/cli`: Node launcher (`npm/cli/bin/oku.js`), `bin: oku`, no install scripts.
- `@oku-trade/cli-{linux,darwin}-{x64,arm64}`: one binary each, with `os`/`cpu` metadata,
  pinned as exact-version `optionalDependencies` of the launcher.

The launcher spawns the binary without a shell, inherits stdio (TTY preserved), forwards
SIGINT/SIGTERM/SIGHUP/SIGQUIT/SIGUSR1/SIGUSR2/SIGWINCH, and mirrors the exit code or re-raises
the child's terminating signal. If the platform package is missing it exits 1 with a message.
`OKU_BINARY_PATH` overrides binary resolution (used by tests).

```sh
scripts/npm-pack.sh 0.1.0 npm-dist linux/amd64=path/to/oku   # launcher + given platforms
scripts/npm-smoke.sh npm-dist 0.1.0                          # offline install + checks
```

`npm-pack.sh` fails unless each tarball contains exactly `package.json`, `README.md` and the
launcher/binary. `npm-smoke.sh` installs from local tarballs with `--offline` into a temp dir,
then checks `oku version` via bin link and `npx`, argv quoting, stdin, exit codes, signal
forwarding/re-raise, and the `--omit=optional` error. No registry is contacted.

## Publishing (disabled)

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

Validated locally on linux/amd64 only (pack + offline install smoke with the real binary). The
four-runner CI matrix and the release workflow have not been run yet.
