# Oku Agent (`oku`)

`oku` is a Go command-line client for trading Hyperliquid perps through the Oku account service (`gfxcafe.oku.account.v2.PerpsService`). It is built for people and coding agents. It prints human-readable tables by default and offers a versioned JSON contract (`oku.cli/v1`) with `--json`. It also ships offline docs (`oku docs`), bundled task skills (`oku skills`) and a stdio MCP server (`oku mcp serve`).

It supports Hyperliquid **mainnet** only.

For user-operated authentication, run `oku auth login` in a terminal. Sign on the Oku authorization page with the wallet you want to use as the account. The CLI derives that account from Hyperliquid's approved agent and creates a `default` profile if none exists; no account flag is needed. For production, use `oku auth login --api-url https://accounts.icarus.tools` on a fresh configuration.

> **Status: pre-release / private alpha development.**
> - The repository is private. There is no published npm package, no hosted install script and no auto-update.
> - Staging acceptance on 2026-10-14 verified Privy login, signed order submission, order listing and cancel. This is not production acceptance.
> - Accounts multi-app verification (the account service) and the authorize page (the web app) are merged. Production must use `https://oku.trade/agent/authorize`; staging currently uses `https://app.staging.invalid/agent/authorize`.
> - Earlier release checks in [docs/validation.md](docs/validation.md) predate this auth model.
> - The command grammar may change.

For agent-authored orders, inspect `oku docs schema --command 'orders prepare'`, write a JSON object of command options, and run `oku orders prepare --input order.json --json`. Global options remain CLI flags. See [JSON input](docs/usage.md#json-input).

## Install (from source)

Building requires Go (the version is in `go.mod`).

- **Linux** builds with `CGO_ENABLED=0`.
- **macOS** needs CGO and a native macOS host for the Keychain credential provider. Cross-compiling Darwin from Linux is refused by `mise run dist`.

```sh
git clone --branch master <this repository> && cd oku-cli
make build          # bin/oku; or: make install (to $HOME/.local/bin/oku)
./bin/oku version
./bin/oku docs list # offline documentation bundled in the binary
```

## Install (Nix)

The repo is a flake, so Nix builds the pinned Go toolchain and dependencies for you. Requires Nix with flakes enabled (`--extra-experimental-features 'nix-command flakes'` if they are not on by default).

```sh
git clone --branch master <this repository> && cd oku-cli

# run without installing
nix run . -- version

# ephemeral shell with oku on $PATH
nix shell . --command oku version

# build a ./result symlink
nix build .            # ./result/bin/oku version

# install into your user profile
nix profile add .#oku  # then: oku version   (older Nix: nix profile install .#oku)
```

Development shell with Go, `gopls`, and `make`:

```sh
nix develop            # then: make build && go vet ./... && make docs-check
```

Uninstall the profile install with `nix profile remove oku`. Installing the named `.#oku` attribute (rather than plain `.`) is what makes the profile entry match the binary name; `nix profile list` shows the current entries. Once this repository is reachable by URL you can skip the clone and use `nix run github:oku-trade/oku-cli/master -- version` or add it as a flake input:

```nix
inputs.oku-cli.url = "github:oku-trade/oku-cli/master";
# then: environment.systemPackages = [ inputs.oku-cli.packages.${system}.default ];
```


## npm (not published)

The release tooling packs a thin launcher `@oku-trade/cli` plus four exact-version platform packages `@oku-trade/cli-{linux,darwin}-{x64,arm64}`. There are no postinstall scripts or downloads.

- Names, scope ownership, visibility and license (`UNLICENSED`) are pending owner decisions.
- Publishing is manual and gated. See [docs/releasing.md](docs/releasing.md).
- No provenance is produced, because the source repository is private.

## Uninstall

1. Run `oku skills uninstall --target <agent>` for any installed skills.
2. Run `oku auth logout` (then separately revoke the Hyperliquid agent with the master wallet).
3. Run `make uninstall` (or `rm "$(command -v oku)"`).

Removing the binary does **not** revoke Oku sessions or the exchange delegated signer. It also leaves the journal in place. See [docs/security.md](docs/security.md).

## Docs

All of these except `validation.md` (repository-only evidence, not embedded) are also available offline via `oku docs show <name>`:

- [docs/usage.md](docs/usage.md): commands, JSON contract, order lifecycle, risk actions, draft repair, skills, MCP, journal
- [docs/commands.md](docs/commands.md): generated command and flag reference (`make docs`, `make docs-check`)
- [docs/security.md](docs/security.md): Privy signing model, credential storage, grant and agent revocation
- [docs/architecture.md](docs/architecture.md): code layout and how the CLI talks to Oku
- [docs/deployment.md](docs/deployment.md): backend/frontend/release prerequisites
- [docs/local-testing.md](docs/local-testing.md): local read-only checks and operator-led Privy login/trade guidance
- [docs/releasing.md](docs/releasing.md): native builds, npm packaging, checksums
- [docs/validation.md](docs/validation.md): what has actually been verified
- Agent skills: [skills/](skills/)
