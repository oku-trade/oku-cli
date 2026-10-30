# Oku Agent (`oku`)

Swap and bridge across 30+ chains and trade perps on Oku, from your terminal or your AI agent.

- **Swap** any token on 30+ chains at the best available rate. Oku compares DEX aggregators and intent solvers, and transaction swaps settle through the Oku Router.
- **Bridge** across 30+ EVM chains with Oku's bridge meta-aggregator. The agent picks the route with the best net value, or the one you name.
- **Trade perps** on Hyperliquid with Oku: market and limit orders, leverage, margin, positions and fills.
- **Built for agents.** Every command has a versioned JSON contract (`oku.cli/v1`), and the binary ships offline docs (`oku docs`), task skills (`oku skills`) and an MCP server (`oku mcp serve`). Every trade is prepared and shown for review before it is signed.
- **Telegram bot.** `oku telegram serve` runs a private bot for allowlisted Telegram user IDs, with menus for wallet, perps, swap, send, bridge and exit orders, and a Confirm step before every action (see [usage](docs/usage.md#telegram-bot)).

`oku` is a non-custodial Go CLI. It trades from an embedded agent wallet that you approve on oku.trade, and talks to the Oku account service (`gfxcafe.oku.account.v2.PerpsService`) for perps. It prints human-readable tables by default and JSON with `--json`.

Perps trading supports Hyperliquid **mainnet** only. Agent-wallet EVM commands (send, swap, bridge) work on any EVM chain. Swaps work on every chain where Oku's swap markets are active and the shared `gfx-labs/chain-config` networks package (the same one the Oku backend uses) lists an Oku Router for a non-deprecated chain; the CLI takes the router address from that package and checks its code on chain before signing. Common chains have a built-in public RPC; for others set `OKU_RPC_URL_<CHAINID>`.

For user-operated authentication, run `oku auth login` in a terminal. Sign on the Oku authorization page with the wallet you want to use as the account. The CLI derives that account from Hyperliquid's approved agent and creates a `default` profile if none exists; no account flag is needed. The default API is production (`https://accounts.icarus.tools`). For staging, create the profile with `oku auth login --api-url https://accounts.lat.gfx.town` (or `oku profiles add NAME --api-url https://accounts.lat.gfx.town`). Existing profiles keep the API URL they were created with.

> **Status: pre-release / private alpha development.**
> - The repository is private. Prebuilt binaries are published as GitHub Releases (repo access required). There is no published npm package and no auto-update.
> - Staging acceptance on 2026-10-14 verified Privy login, signed order submission, order listing and cancel. This is not production acceptance.
> - Live checks with small amounts verified agent-wallet send, master deposit transfer, a single-step swap and an atomic approve+swap batch on Arbitrum (2026-10-19), and a Circle CCTP bridge Arbitrum to Base including the destination claim (2026-10-20). Transaction-based Oku bridge execution is available via `bridge prepare`, `submit`, `status`, `claim`, and `reconcile`; bridge calldata and approvals are backend-built without independent CLI verification, and destination claims require agent native gas on the destination chain.
> - Accounts multi-app verification (account#530) and the authorize page (trade#3569) are merged. The authorize page URL comes from the Privy app configuration (the device-grant `verification_uri`), not from the CLI or Accounts. Since 2026-10-22 the built-in Oku Trade Agents app returns `https://oku.trade/agent/authorize`.
> - Earlier release checks in [docs/validation.md](docs/validation.md) predate this auth model.
> - The command grammar may change.

## Agent wallet funds

The embedded agent wallet is a separate EVM wallet with its own balances and gas. Every transfer is a two-step prepare/submit with a reviewable digest:

```sh
oku wallet balances --chain 42161
oku wallet fund --chain 42161 --token native --amount 0.01 # payment QR and web link, no signing
oku swap quote   --chain 42161 --from USDC --to WETH --amount 1
oku swap prepare --chain 42161 --from USDC --to WETH --amount 1
oku swap submit  --intent ID --digest SHA     # within ~3 minutes of prepare
oku swap status  --intent ID
oku swap orders create --chain 42161 --token TOKEN --all --stop-loss-pct 20   # client-side exit
oku swap orders watch                          # exits only fire while this runs
oku swap pnl                                   # realized/unrealized PnL of the agent's swaps
```

- Transaction swap quotes use the same `GetFlowstreamQuotes` stream as the web app and retain OkuRouter calldata verification.
- Intent-based swaps and bridges (such as CoW and Aori) are selectable by net value or `--route NAME`; `--no-intent` excludes them. They sign the backend's EIP-712 order, send any necessary approvals as ordinary transactions, then relay off-chain without an agent swap/bridge transaction. `submitted` is not a fill. Check order history via `swap status` or `bridge status`; solver execution may expire or refund. Intent typed-data and approvals trust Oku's backend as the web app does, not the transaction swap's OkuRouter verifier.
- When a transaction swap token approval is needed, approve and swap are sent as **one atomic** Privy `wallet_sendCalls` batch (EIP-7702; the first batch upgrades the agent wallet to a Kernel smart account at the same address). An approval can never confirm without its swap.
- `send` transfers from the agent (to the master by default); `deposit prepare` only moves agent USDC to the master on Arbitrum. The master deposits to Hyperliquid itself; never send agent USDC to the Hyperliquid bridge.
- Keep native gas (ETH on Arbitrum) in the agent wallet. Chain RPC defaults to Oku's eRPC gateway (`https://erpc.lat.gfx.town/main/<chain>`); override any chain with `OKU_RPC_URL_<CHAINID>`.
- `OUTCOME_UNKNOWN` means never resubmit; use `status` or `reconcile`. See [agent EVM transfers](docs/usage.md#agent-evm-transfers-and-master-deposits) and [security](docs/security.md).

## JSON input

For agent-authored orders, inspect `oku docs schema --command 'orders prepare'`, write a JSON object of command options, and run `oku orders prepare --input order.json --json`. Global options remain CLI flags. See [JSON input](docs/usage.md#json-input).

## Install (prebuilt binary)

Releases on this repo carry `oku_<version>_<os>_<arch>.tar.gz` for linux/darwin amd64/arm64 plus `SHA256SUMS`. You need access to the private repo and either the `gh` CLI logged in (`gh auth login`) or a `GITHUB_TOKEN`.

```sh
# latest release to ~/.local/bin/oku (verifies SHA256SUMS, no sudo)
gh api -H 'Accept: application/vnd.github.raw' repos/oku-trade/oku-cli/contents/scripts/install.sh | sh
# options: sh install.sh --version v0.1.0 --dir ~/bin
```

Manual install (Apple Silicon Mac shown; use `darwin_amd64`, `linux_amd64` or `linux_arm64` as needed):

```sh
V=$(gh release view -R oku-trade/oku-cli --json tagName -q .tagName)
gh release download "$V" -R oku-trade/oku-cli -p "oku_${V}_darwin_arm64.tar.gz" -p SHA256SUMS
shasum -a 256 -c --ignore-missing SHA256SUMS
tar -xzf "oku_${V}_darwin_arm64.tar.gz"
mkdir -p ~/.local/bin && mv "oku_${V}_darwin_arm64/oku" ~/.local/bin/oku
oku version
```

macOS binaries are ad-hoc signed only (not Developer ID signed or notarized). Files fetched with `gh` or curl normally run as is. If you downloaded through a browser and macOS blocks it, run `xattr -d com.apple.quarantine ~/.local/bin/oku` (or right-click the binary in Finder and choose Open). Maintainers: see [docs/releasing.md](docs/releasing.md) for cutting a release.

### Upgrade

```sh
oku self-update --check   # compare the running version with the latest release
oku self-update           # download, verify SHA256SUMS and replace this binary in place
oku self-update --version v0.1.0   # install a specific release (downgrades allowed)
```

`self-update` uses `GITHUB_TOKEN`/`GH_TOKEN`, else `gh auth token`, and never needs sudo. It only replaces binaries installed by `install.sh` or a release tarball: mise, Nix and Homebrew installs are refused (use `mise up github:oku-trade/oku-cli` and so on), and so are source builds (`git pull && make install`). By default it never runs on its own.

#### Opt-in auto-update

```sh
oku autoupdate enable [--interval 24h] [--no-skills]   # off by default
oku autoupdate status    # setting, last check, last result, log path
oku autoupdate disable
```

When enabled, any `oku` command checks whether the interval (default 24h, minimum 1h) has passed since the last check. If so it records the check and starts a detached `oku autoupdate run` in the background, so the command you ran is never delayed or changed. That job does exactly what `oku self-update` does, then, if the binary changed, refreshes oku skills installed under the standard jcode/claude/codex skill directories. Skill directories containing user-edited oku skill files are left alone. Output goes to `autoupdate.log` in the state dir. Set `OKU_NO_AUTO_UPDATE=1` to skip background checks for a shell or CI job.

## Install (mise)

[mise](https://mise.jdx.dev) installs `oku` straight from this repo's GitHub Releases with its `github` backend. It picks the archive for your OS and arch, checks it against the release digest, and puts `oku` on your `PATH`.

The repo is private, so mise needs a GitHub token. If you use the `gh` CLI, let mise ask it (on macOS `gh` keeps its token in Keychain, where mise can't read it directly). Add this to `~/.config/mise/config.toml`:

```toml
[settings.github]
credential_command = 'gh auth token --hostname "$MISE_CREDENTIAL_HOST"'
```

Or export `MISE_GITHUB_TOKEN` (a fine-grained token with Contents: read on `oku-trade/oku-cli` is enough). Check which token mise will use with `mise token github`.

Then install it globally:

```sh
mise use -g github:oku-trade/oku-cli      # latest release
mise use -g github:oku-trade/oku-cli@0.0.1  # or pin a version (tag without the v)
oku version
```

- mise hides releases younger than its `minimum_release_age` (24h by default), so a release cut today needs an explicit `@<version>` until tomorrow.
- Upgrade with `mise up github:oku-trade/oku-cli`, list versions with `mise ls-remote github:oku-trade/oku-cli`.
- To pin it for one project, drop `-g` and mise writes it to that project's `mise.toml`.

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

- [docs/usage.md](docs/usage.md): commands, JSON contract, order lifecycle, risk actions, agent-wallet sends, deposits and swaps, draft repair, skills, MCP, journal
- [docs/commands.md](docs/commands.md): generated command and flag reference (`make docs`, `make docs-check`)
- [docs/security.md](docs/security.md): Privy signing model, credential storage, grant and agent revocation, agent-wallet swap verification and batch execution
- [docs/telemetry.md](docs/telemetry.md): opt-in usage telemetry (off by default, `DO_NOT_TRACK=1` respected): exact fields, enable and disable
- [docs/architecture.md](docs/architecture.md): code layout and how the CLI talks to Oku
- [docs/deployment.md](docs/deployment.md): backend/frontend/release prerequisites
- [docs/local-testing.md](docs/local-testing.md): local read-only checks and operator-led Privy login/trade guidance
- [docs/releasing.md](docs/releasing.md): native builds, npm packaging, checksums
- [docs/validation.md](docs/validation.md): what has actually been verified
- Agent skills: [skills/](skills/)
