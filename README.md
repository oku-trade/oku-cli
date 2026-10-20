# Oku Agent (`oku`)

`oku` is a Go command-line client for trading Hyperliquid perps through the Oku account service (`gfxcafe.oku.account.v2.PerpsService`), and for moving funds held by the Oku Agent's embedded EVM wallet (balances, sends, master-bound deposits and Oku Router swaps). It is built for people and coding agents. It prints human-readable tables by default and offers a versioned JSON contract (`oku.cli/v1`) with `--json`. It also ships offline docs (`oku docs`), bundled task skills (`oku skills`) and a stdio MCP server (`oku mcp serve`).

It supports Hyperliquid **mainnet** only. Agent-wallet EVM commands work on any EVM chain. Swaps work on every chain where Oku's swap markets are active and the shared `gfx-labs/chain-config` networks package (the same one the Oku backend uses) lists an Oku Router for a non-deprecated chain; the CLI takes the router address from that package and checks its code on chain before signing. Common chains have a built-in public RPC; for others set `OKU_RPC_URL_<CHAINID>`.

For user-operated authentication, run `oku auth login` in a terminal. Sign on the Oku authorization page with the wallet you want to use as the account. The CLI derives that account from Hyperliquid's approved agent and creates a `default` profile if none exists; no account flag is needed. For production, use `oku auth login --api-url https://accounts.icarus.tools` on a fresh configuration.

> **Status: pre-release / private alpha development.**
> - The repository is private. There is no published npm package, no hosted install script and no auto-update.
> - Staging acceptance on 2026-10-14 verified Privy login, signed order submission, order listing and cancel. This is not production acceptance.
> - Live Arbitrum checks on 2026-10-19 with small amounts verified agent-wallet send, master deposit transfer, a single-step swap and an atomic approve+swap batch. Transaction-based Oku bridge execution is available via `bridge prepare`, `submit`, `status`, and `reconcile`; bridge calldata and approvals are backend-built without independent CLI verification, and destination tracking is informational.
> - Accounts multi-app verification (account#530) and the authorize page (trade#3569) are merged. Production must use `https://oku.trade/agent/authorize`; staging currently uses `https://unitrade.lat.gfx.town/agent/authorize`.
> - Earlier release checks in [docs/validation.md](docs/validation.md) predate this auth model.
> - The command grammar may change.

## Agent wallet funds

The embedded agent wallet is a separate EVM wallet with its own balances and gas. Every transfer is a two-step prepare/submit with a reviewable digest:

```sh
oku wallet balances --chain 42161
oku swap quote   --chain 42161 --from USDC --to WETH --amount 1
oku swap prepare --chain 42161 --from USDC --to WETH --amount 1
oku swap submit  --intent ID --digest SHA     # within ~3 minutes of prepare
oku swap status  --intent ID
```

- Swap quotes use the same `GetFlowstreamQuotes` stream as the web app and only route through Oku Router with exact, verified calldata.
- When a token approval is needed, approve and swap are sent as **one atomic** Privy `wallet_sendCalls` batch (EIP-7702; the first batch upgrades the agent wallet to a Kernel smart account at the same address). An approval can never confirm without its swap.
- `send` transfers from the agent (to the master by default); `deposit prepare` only moves agent USDC to the master on Arbitrum. The master deposits to Hyperliquid itself; never send agent USDC to the Hyperliquid bridge.
- Keep native gas (ETH on Arbitrum) in the agent wallet, and point `OKU_RPC_URL_42161` (or the chain you use) at a full RPC: batch lookup needs recent historical state that public endpoints may refuse.
- `OUTCOME_UNKNOWN` means never resubmit; use `status` or `reconcile`. See [agent EVM transfers](docs/usage.md#agent-evm-transfers-and-master-deposits) and [security](docs/security.md).

## JSON input

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

- [docs/usage.md](docs/usage.md): commands, JSON contract, order lifecycle, risk actions, agent-wallet sends, deposits and swaps, draft repair, skills, MCP, journal
- [docs/commands.md](docs/commands.md): generated command and flag reference (`make docs`, `make docs-check`)
- [docs/security.md](docs/security.md): Privy signing model, credential storage, grant and agent revocation, agent-wallet swap verification and batch execution
- [docs/architecture.md](docs/architecture.md): code layout and how the CLI talks to Oku
- [docs/deployment.md](docs/deployment.md): backend/frontend/release prerequisites
- [docs/local-testing.md](docs/local-testing.md): local read-only checks and operator-led Privy login/trade guidance
- [docs/releasing.md](docs/releasing.md): native builds, npm packaging, checksums
- [docs/validation.md](docs/validation.md): what has actually been verified
- Agent skills: [skills/](skills/)
