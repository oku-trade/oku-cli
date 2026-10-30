# Oku Agent (`oku`)

Swap and bridge across 30+ chains and trade perps on Oku, from your terminal or your AI agent.

- **Swap** any token on 30+ chains at the best available rate. Oku compares DEX aggregators and intent solvers, and transaction swaps settle through the Oku Router.
- **Bridge** across 30+ EVM chains with Oku's bridge meta-aggregator. The agent picks the route with the best net value, or the one you name.
- **Trade perps** on Hyperliquid with Oku: market and limit orders, leverage, margin, positions and fills.
- **Built for agents.** Every command has a versioned JSON contract (`oku.cli/v1`), and the binary ships offline docs (`oku docs`), task skills (`oku skills`) and an MCP server (`oku mcp serve`). Mutations use a prepare/submit flow: review the preview against the user's authorized action before submitting, and ask for confirmation if anything is ambiguous or differs.

`oku` is a non-custodial Go CLI. It trades from an embedded agent wallet that you approve on oku.trade, and talks to the Oku account service (`gfxcafe.oku.account.v2.PerpsService`) for perps. It prints human-readable tables by default and JSON with `--json`.

Perps trading supports Hyperliquid **mainnet** only. Agent-wallet EVM commands (send, swap, bridge) work on EVM chains when supported by the backend and configured with an RPC. Swaps require an active Oku market and an Oku Router in the shared `gfx-labs/chain-config` networks package; the CLI checks router code on chain before signing. Set `OKU_RPC_URL_<CHAINID>` to an RPC you trust for each chain you use. Some transaction reconciliation requires historical account state and full block transactions; choose a provider that serves the required history.

For user-operated authentication, run `oku auth login` in a terminal. Sign on the Oku authorization page with the wallet you want to use as the account. The CLI derives that account from Hyperliquid's approved agent and creates a `default` profile if none exists; no account flag is needed. The default API is production (`https://accounts.icarus.tools`). Existing profiles keep the API URL they were created with; `--api-url` remains available for custom deployments.

> **Status: pre-release.** Commands may change. Prebuilt binaries are distributed as GitHub Releases. npm publishing is a separate decision. Automatic updates are opt-in and disabled by default. Transaction-based bridges support prepare, submit, status, claim and reconcile; destination claims require native gas on the destination chain.

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
- Keep native gas (ETH on Arbitrum) in the agent wallet. Configure your own trusted chain RPC with `OKU_RPC_URL_<CHAINID>` before sending, swapping or bridging on that chain.
- `OUTCOME_UNKNOWN` means never resubmit; use `status` or `reconcile`. See [agent EVM transfers](docs/usage.md#agent-evm-transfers-and-master-deposits) and [security](docs/security.md).

## JSON input

For agent-authored orders, inspect `oku docs schema --command 'orders prepare'`, write a JSON object of command options, and run `oku orders prepare --input order.json --json`. Global options remain CLI flags. See [JSON input](docs/usage.md#json-input).

## Install (prebuilt binary)

Public releases carry `oku_<version>_<os>_<arch>.tar.gz` for linux/darwin amd64/arm64 plus `SHA256SUMS`. No GitHub token is needed for ordinary downloads.

```sh
# latest release to ~/.local/bin/oku (verifies SHA256SUMS, no sudo)
curl -fsSLO https://raw.githubusercontent.com/oku-trade/oku-cli/master/scripts/install.sh
sh install.sh # options: --version v0.1.0 --dir ~/bin
```

Manual install (Apple Silicon Mac shown; use `darwin_amd64`, `linux_amd64` or `linux_arm64` as needed):

```sh
V=$(curl -fsSL https://api.github.com/repos/oku-trade/oku-cli/releases/latest | awk -F '"' '/"tag_name"/ {print $4; exit}')
curl -fsSLO "https://github.com/oku-trade/oku-cli/releases/download/${V}/oku_${V}_darwin_arm64.tar.gz"
curl -fsSLO "https://github.com/oku-trade/oku-cli/releases/download/${V}/SHA256SUMS"
shasum -a 256 -c --ignore-missing SHA256SUMS
tar -xzf "oku_${V}_darwin_arm64.tar.gz"
mkdir -p ~/.local/bin && mv "oku_${V}_darwin_arm64/oku" ~/.local/bin/oku
oku version
```

macOS signing depends on release configuration: the workflow uses Developer ID signing and notarization when configured, otherwise ad-hoc signing. Check a release's artifacts before assuming either status. If macOS blocks a download, verify the source and checksum before following the operating system's instructions. Maintainers: see [docs/releasing.md](docs/releasing.md).

### Upgrade

```sh
oku self-update --check   # compare the running version with the latest release
oku self-update           # download, verify SHA256SUMS and replace this binary in place
oku self-update --version v0.1.0   # install a specific release (downgrades allowed)
```

`self-update` uses public release downloads anonymously by default; `GITHUB_TOKEN`/`GH_TOKEN` are optional for API rate limits. It never needs sudo. It refuses mise, Nix, Homebrew and source builds. By default it never runs on its own.

`SHA256SUMS` checks the archive against the manifest published in the same GitHub release. It is not an independent signature: update trust depends on the repository's release permissions and the integrity of GitHub's release delivery. `--version` can install an older release; check the tag before using it.

#### Opt-in auto-update

```sh
oku autoupdate enable [--interval 24h] [--no-skills]   # off by default
oku autoupdate status    # setting, last check, last result, log path
oku autoupdate disable
```

When enabled, any `oku` command checks whether the interval (default 24h, minimum 1h) has passed since the last check. If so it records the check and starts a detached `oku autoupdate run` in the background, so the command you ran is never delayed or changed. That job does exactly what `oku self-update` does, then, if the binary changed, refreshes oku skills installed under the standard jcode/claude/codex skill directories. Skill directories containing user-edited oku skill files are left alone. Output goes to `autoupdate.log` in the state dir. Set `OKU_NO_AUTO_UPDATE=1` to skip background checks for a shell or CI job.

## Install (mise)

[mise](https://mise.jdx.dev) installs `oku` straight from this repo's GitHub Releases with its `github` backend. It picks the archive for your OS and arch, checks it against the release digest, and puts `oku` on your `PATH`.

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

- Publishing requires confirmation of package scope ownership and trusted-publisher configuration. The packages are configured for public access under AGPL-3.0-only.
- Publishing is manual and gated. See [docs/releasing.md](docs/releasing.md).
- Check the publishing workflow and registry metadata for provenance before making a provenance claim.

## Uninstall

1. Run `oku skills uninstall --target <agent>` for any installed skills.
2. Run `oku auth logout` (then separately revoke the Hyperliquid agent with the master wallet).
3. Run `make uninstall` (or `rm "$(command -v oku)"`).

Removing the binary does **not** revoke Oku sessions or the exchange delegated signer. It also leaves the journal in place. See [docs/security.md](docs/security.md).

## Docs

The following user docs are available offline via `oku docs show <name>`:

- [docs/usage.md](docs/usage.md): commands, JSON contract, order lifecycle, risk actions, agent-wallet sends, deposits and swaps, draft repair, skills, MCP, journal
- [docs/commands.md](docs/commands.md): generated command and flag reference (`make docs`, `make docs-check`)
- [docs/security.md](docs/security.md): Privy signing model, credential storage, grant and agent revocation, agent-wallet swap verification and batch execution
- [docs/telemetry.md](docs/telemetry.md): opt-in usage telemetry (off by default, `DO_NOT_TRACK=1` respected): exact fields, enable and disable
- [docs/architecture.md](docs/architecture.md): code layout and how the CLI talks to Oku
- Maintainers: [docs/releasing.md](docs/releasing.md) is in the repository, not embedded in the binary.
- Agent skills: [skills/](skills/)

## License

Copyright 2026 GFX Labs, Inc. Oku CLI is licensed under the GNU Affero General Public License v3.0 only (AGPL-3.0-only). See [LICENSE](LICENSE). Dependency licenses and notices are included in release archives as `THIRD_PARTY_NOTICES`; some dependencies contain components under other licenses, including LGPL-3.0 portions of go-ethereum.
