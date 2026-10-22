# Usage

## Agent EVM transfers and master deposits

`oku wallet balances` lists agent-wallet ERC-20 holdings via WalletService.AddressHoldings and refreshes discovered token balances onchain. Use `--chains 42161,8453` for several chains, or `--all-chains` for common EVM chains. Indexed discovery can miss new or obscure tokens; `onchain_tokens` is fresher than indexed amounts. `send prepare`, `swap quote`/`prepare`, and `bridge quote`/`prepare` accept `--amount max` or `--amount all`, resolving from the latest onchain balance. Native inputs retain 0.001 native units by default (0.01 on Ethereum); override with `--gas-reserve`. This reserve cannot guarantee execution fees, especially L2 data fees. Full-balance transfers can leave dust or fail for fee-on-transfer or rebasing tokens. Quotes may change before preparation or execution; route slippage remains capped at 300 bps.

`oku send prepare --chain 42161 --token native --amount 0.01` prepares an agent-wallet transfer to the enrolled master by default. For ERC-20 use `--token TOKEN_ADDRESS`; specify `--to RECIPIENT_ADDRESS` explicitly to send elsewhere. Review the returned recipient, amount, chain, calldata and digest, then run `oku send submit --intent ID --digest SHA`. Use `oku send status --intent ID` to check a receipt. Any EVM chain ID works. Common chains have a built-in public RPC; for others, or to use a better endpoint, set `OKU_RPC_URL_<CHAINID>` (e.g. `OKU_RPC_URL_42161`). Keep native gas (e.g. ETH on Arbitrum) in the agent wallet for sends and swaps.

`oku deposit prepare --amount 10` prepares **only** an Arbitrum native-USDC transfer from the agent to the profile master EOA. After reviewing it, run `oku deposit submit --intent ID --digest SHA` and `oku deposit status --intent ID`. This does **not** deposit into Hyperliquid. The master must deposit from its own wallet on the Oku web deposit page (`https://oku.trade/perps`). For a master-bound HyperUnit address use `oku deposit address --asset ASSET --source-chain CHAIN` and fund that address externally. Never transfer USDC from the agent to the Hyperliquid bridge: that credits the agent, not the master.

A timeout or ambiguous Privy broadcast becomes `OUTCOME_UNKNOWN`. Never repeat `submit` or prepare a replacement. Inspect `status` and the source-chain explorer. For an approval+swap batch, `oku swap status --intent ID` can locate a transaction by its recorded agent nonce even if Privy returned no onchain hash, then confirm its receipt. Alternatively, `oku swap reconcile --intent ID --tx-hash TX_HASH` verifies an independently recovered hash against the journal. An unresolved intent blocks new onchain intents for the agent until resolved. The Privy OAuth `eth_sendTransaction` request follows Privy's published wallet RPC schema (top-level `caip2` such as `eip155:42161`, plus `params.transaction` with `chain_id`); it has been exercised live on Arbitrum, as has the `wallet_sendCalls` batch used for approval+swap.

`INSUFFICIENT_GAS`, `INSUFFICIENT_BALANCE`, or `REJECTED` with “nothing broadcast” means the preflight intent is closed. Correct the cause, then prepare a fresh intent. A post-broadcast receipt lookup error requires `status`, never resubmission.

> `oku --help`, `oku docs commands` and the [generated command reference](commands.md) describe the urfave/cli v3 command tree of the binary you run. `make docs-check` detects drift between the committed reference and the tree.

## Output contract

- `--json` writes exactly one JSON document to **stdout**. Diagnostics go to **stderr**.
- Envelope fields: `schema_version` (`oku.cli/v1`), `request_id`, `command`, `ok`, `data`, and `error` (`code`, `message`, `retryable`, optional `details`) or `null`.
- Quantities and prices are decimal strings.
- Only Hyperliquid **mainnet** is supported. Trading requires a profile with an account. In automation, always pass `--profile` explicitly.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | ok |
| 1 | internal error |
| 2 | usage error |
| 3 | auth (missing/locked/revoked credentials, or missing scope) |
| 4 | not found |
| 5 | upstream (Oku/exchange) error |
| 6 | `OUTCOME_UNKNOWN`: reconcile, do not resubmit |
| 7 | conflict / rejected |

## Credentials

- **Linux:** an encrypted local file. You unlock it with an interactive masked TTY prompt. A successful unlock (including login) is cached in the kernel user keyring for 8 hours, like `sudo`, so agents and MCP can use the credential without prompting. `oku auth unlock [--for 2h]` refreshes it, `oku auth lock` forgets it, `OKU_UNLOCK_TTL=0` disables caching. The cache never touches disk and is cleared on reboot. For unattended servers, `OKU_PASSPHRASE_FILE=<path>` pointing at a mode-0600 file still works. Never pass the passphrase as an argument. A short, mismatched, or wrong passphrase re-prompts (up to 5 tries). At login you may leave the passphrase empty to store the credential unprotected; this asks twice and is insecure, since anyone who can read the file can sign as your agent.
- **macOS:** the native Keychain. No passphrase is requested. Official macOS builds need CGO and are built natively.
- Neither platform has a plaintext fallback. See [security](security.md#credential-storage-by-platform).

## Global flags

- `--profile NAME`: defaults to `$OKU_PROFILE` or the config default.
- `--network NAME`: only `mainnet` is supported, and it is checked against the profile.
- `--account 0x..`: for read commands.
- `--json`.
- `--timeout DUR`: default 15s, max 2m.
- `--input FILE`: one JSON object of command-specific long options, or `-` for stdin.

## JSON input

Discover the exact keys and JSON types with `oku docs commands --json`. Read the draft 2020-12 schema with `oku docs schema --command 'orders prepare'`; `oku docs schema --all --json` returns every command schema. These schemas are generated from the same flag declarations used by the CLI, and MCP serves them as `oku://schemas/<command-path-dashed>` resources.

```sh
cat > order.json <<'JSON'
{"market":"BTC","side":"buy","type":"limit","size":"0.0002","price":"60000","tif":"gtc"}
JSON
oku orders prepare --input order.json --json
oku orders prepare --input - --json <<'JSON'
{"market":"BTC","side":"buy","type":"limit","size":"0.0002","price":"60000"}
JSON
```

Only command-specific options belong in the object, without `--`. Keep `--profile`, `--json`, and other globals as flags. No positional arguments or command flags may accompany `--input`. Unknown keys, wrong types, trailing values, and inputs over 1 MiB fail with USAGE. Commands without flags and interactive login/logout do not accept it. `drafts repair --stdin` cannot share stdin with `--input -`. Prefer a file path when a passphrase prompt is needed; headless stdin requires the user to configure `OKU_PASSPHRASE_FILE` securely.

Global flags work before or after a command. Use `--` to end flag parsing.

## Built-in documentation (offline)

These documents are embedded in the binary. Commands and docs need no network access, credentials or config:

```sh
oku docs list                       # names: architecture, commands, deployment, local-testing, releasing, security, usage
oku docs show security
oku docs search leverage [--limit N]  # default 50, 0 = unlimited
oku docs commands                   # one line per command of this binary
oku docs commands --markdown        # full reference; identical to docs/commands.md
```

## Shell completion

Completion scripts are available without credentials or network access. Generate one with `oku completion bash`, `oku completion zsh`, or `oku completion fish`.

- Zsh and fish scripts use urfave/cli generation.
- Bash uses a safe array-based wrapper around urfave/cli's candidate protocol, because the stock bash template evaluates user input.
- The scripts call `oku --generate-shell-completion` when completing. This only lists command names and flags, and never runs auth, order or read handlers.
- Nothing edits your shell startup files automatically.

For the current shell session (with `oku` on `$PATH`), enable zsh's `compinit` first:

```sh
# bash
source <(oku completion bash)
# zsh
autoload -Uz compinit && compinit
source <(oku completion zsh)
# fish
oku completion fish | source
```

To make completion persistent, save the output and source it from a shell startup file yourself.

## Commands

See [the generated reference](commands.md) for every command and flag. Common examples:

```sh
oku version
oku doctor
oku config path | config show          # no secrets printed

oku auth login                          # sign with your account wallet at https://oku.trade/agent/authorize
                                        # with no profiles, creates and selects profile "default"
oku auth status [--verify]
oku auth logout                         # best-effort session/grant revoke, deletes local credential

oku profiles add NAME --account 0x.. [--api-url URL] [--default]
oku profiles list | use NAME | remove NAME

oku markets list
oku markets quote COIN
oku markets funding COIN [--since 24h]
oku account summary
oku positions list
oku orders list
oku fills list

oku orders prepare --market BTC --side buy|sell --type limit|market --size <dec> \
  [--price <dec>] [--tif gtc|ioc|alo] [--slippage-bps N] [--reduce-only] \
  [--expires 5m] [--intent-id ID]
#   no exchange submission; may read current market data. Returns intent_id and digest.
#   --price for limit orders. Market orders default to --slippage-bps 50 (0.5%).
#   A market order is sent as an IOC limit at exactly the previewed worst price, so price
#   moves between prepare and submit cannot make it fail; it fills at the best available
#   price up to that bound. Bare symbols such as RKLB resolve to HIP-3 markets (xyz:RKLB).
oku orders submit --intent ID --digest SHA
oku orders status --intent ID
oku orders reconcile
oku orders cancel --market COIN --order-id OID [--intent-id ID]
oku orders abandon --intent ID          # only for never-signed intents

oku skills list
oku skills install   --target jcode|claude|codex | --dir PATH [--force]
oku skills uninstall --target jcode|claude|codex | --dir PATH [--force]
```

Example error with no profile configured: `orders prepare` prints `{"ok":false,"error":{"code":"PROFILE_NOT_FOUND",...}}` and exits 2.

`--digest` binds submission to the exact prepared intent. If anything changes, prepare again. There is no per-order web approval. This is **trusted-host delegated mode** (see `security.md`).

## Leverage and isolated margin

```sh
oku positions leverage --market BTC --leverage N --mode cross|isolated [--intent-id ID] [--expires 5m]
oku positions margin   --market BTC --amount <USDC, +add / -remove, ≤6 decimals> [--intent-id ID] [--expires 5m]
oku orders submit --intent ID --digest SHA
```

Both commands only **prepare** an intent. Submission goes through `orders submit` like any other intent.

- Submission needs an active Oku session and the approved embedded Hyperliquid agent; risk changes can affect real funds.
- Leverage must be an integer from 1 to the market maximum. Isolated-only markets refuse cross mode.
- A leverage change and an order are two separate exchange actions, not one atomic trade. Only a server `completed` state counts as success.

## Draft repair (local only)

```sh
oku drafts repair --stdin < draft.json
```

This turns malformed order-draft JSON (code fences, single quotes, unquoted keys) into a strictly validated candidate. The output includes `repaired`, `candidate` and `prepare_args`.

- It does no networking, signing or journal writes, and it never prepares or submits.
- Limits: 16 KiB input, nesting depth 4, one flat object.
- It rejects duplicate keys, unknown fields, invalid decimals and missing required financial fields. It never fills in missing market, side, size, price or leverage.
- Review the candidate, then run `orders prepare` yourself. The resulting digest is what gets approved.

## Agent EVM swaps and bridges

After `oku bridge status --intent ID`, if `claim_required` is true, run `oku bridge claim --intent ID --json`. Some Circle routes require a destination-chain claim. The CLI reads the agent wallet's bridge order history, sends the backend-provided `claimTo` and `claimCalldata` as one transaction on the destination chain, waits for confirmation, then reports the transaction to the backend. Claim calldata is trusted exactly as in the Oku web app, not independently verified. The agent must hold native gas on the destination chain (the default eRPC gateway covers chain-config chains). Claims are separately journaled and never resent: rerunning `bridge claim` after an unconfirmed claim checks its recorded transaction, confirms it and reports it to Oku.

The embedded Privy wallet is an EVM wallet, **not** the Hyperliquid master account. Use `wallet address` to confirm both addresses. `wallet balances` reads native gas and recommended token balances on the selected EVM chain. To fund the agent from an external wallet, run `oku wallet fund --chain 42161 --token native --amount 0.01` or `oku wallet fund --token usdc --no-open`. It prints an EIP-681 payment URI, terminal QR code and Oku funding page link; `--json` returns `agent`, `chain`, `token`, `amount`, `payment_uri`, and `web_url`. Omit `--amount` for an unspecified amount. The browser opens only in an interactive terminal unless `--no-open` is set; `OKU_WEB_URL` overrides the web origin. The command never signs or sends funds. On insufficient gas or token balance, ask the user to fund the agent, then prepare a fresh intent. `--chain` accepts any supported EVM chain ID (default 42161). Swaps work on any chain where Oku's swap markets are active; `swap quote` reports `REJECTED` for a chain without active markets. CoW Swap may be unavailable below about $25 notional when a flat fee applies; `--route cowswap` surfaces the backend rejection reason.

```sh
oku swap quote --chain 42161 --from USDC --to WETH --amount 10 --slippage-bps 50
oku swap prepare --chain 42161 --from USDC --to WETH --amount 10
oku swap submit --intent ID --digest SHA
oku swap status --intent ID
oku swap reconcile --intent ID --tx-hash TX_HASH # only for unknown outcome with an independently recovered hash

oku bridge quote --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10
```

Amounts are exact input decimals. The slippage default is 50 bps, maximum 300 bps. Token symbols resolve through TokenService; when ambiguous, specify an address. Swap quote and prepare default to the enrolled agent address using credential metadata without unlocking. Without enrolled credentials, a quote requires explicit `--recipient`. Quotes use the backend `GetFlowstreamQuotes` long-poll, as the web app does, and can take up to about 8 seconds to collect routes. Prepare journals a digest binding chain, OkuRouter, recipient, exact input and minimum output; the intent expires about 3 minutes after prepare, so review and submit promptly. Intent-based CoW-like swaps and Aori-like bridges are also available. Use `--route NAME` on quote or prepare to select a specific router/bridge, or `--no-intent` to exclude typed-data routes. The JSON quote and prepared journal record include `intent_based`. Intent submission signs the backend's first EIP-712 typed-data request with Privy, sends only necessary backend-provided approvals as ordinary transactions (never `wallet_sendCalls`), then relays the signed request via `GetExecutionInfo`. No onchain swap or bridge transaction is sent by the agent. The order ID in `backend_order_id` is the solver's intent ID; `submitted` means relayed, **not filled**. `swap status` and `bridge status` read Oku order history and show pending, filled, failed, expired, or refunded, including fill hashes and an explorer URL when available. A timeout during relay leaves `unknown`: never retry submit; use status to find the order. Solvers may not fill, and an order can expire or refund. Intent signing and approval calls trust the backend exactly as the web app does; unlike transaction swap routes, the CLI does not independently verify EIP-712 economic fields or route calldata. Approval spenders may be a Permit2 contract or a vault relayer. Review the route before submission.

Transaction-based swap submission still verifies the non-permit OkuRouter selector and decoded arguments, and exact-amount router approval. When approval is needed, one atomic Privy `wallet_sendCalls` batch `[approve exact amount to OkuRouter, swap]` executes via EIP-7702. On the first batch the agent wallet upgrades to a Kernel smart account without changing its address, and a partial approve-only outcome cannot happen. A single-step route (such as native ETH input) sends one normal transaction. Submit waits for the chain's confirmation depth (under a minute on fast L2s, several minutes on Ethereum, Polygon or BNB Chain). Routes go only to that chain's OkuRouter as listed in the shared chain-config networks package, and submit checks it has contract code. Quoted output and simulation are checked; the minimum in aggregator calldata is backend-built and not independently decoded. See security.md. Transaction-based Oku bridges support `bridge quote`, `bridge prepare --chain SOURCE --destination-chain DEST --from TOKEN --to TOKEN --amount AMOUNT`, `bridge submit --intent ID --digest SHA`, `bridge status --intent ID`, and `bridge reconcile --intent ID --tx-hash HASH`. Only source-chain RPC is required. The backend builds bridge approvals and transaction calldata; unlike transaction swaps, the CLI does not independently verify bridge economic effects, recipient, target contract or approval limit. Multiple calls run atomically through `wallet_sendCalls`; one call uses `eth_sendTransaction`. `bridge status` reports destination delivery for every bridge from Oku's order history (`filled` with the destination transaction, `awaiting_claim`, `refunded`, `failed`, `expired` or `pending`).

Unknown broadcasts must never be resubmitted. Start with `swap status --intent ID`: for an approval batch it searches by nonce and confirms by receipt even if no hash was returned. If a hash is recovered independently, use `swap reconcile --intent ID --tx-hash TX_HASH` (or `send reconcile` for a send) to match the exact journaled call(s) and wait for confirmations. Batch lookup needs recent historical state, which the default Oku eRPC gateway serves; an `OKU_RPC_URL_<CHAINID>` override must too. Gas reserves include estimated gas plus 30% at the fee cap, but L2 data fees and Privy fee choices remain uncertain. Sends, deposits, single-step swaps and approval+swap batches have been exercised live on Arbitrum with small amounts; other chains have not.

## Agent-to-master deposits

`oku deposit prepare --amount N` prepares an Arbitrum USDC transfer **from agent to master EOA**, not a deposit to Hyperliquid. The master then deposits from its own wallet. `oku deposit address --asset ... --source-chain ...` reads a HyperUnit address explicitly bound to the master for external funding. Never send agent-held USDC directly to the HL bridge.

## Agent skills

Nine task skills are bundled:

- `learn-oku`
- `ensure-hyperliquid-authentication`
- `inspect-hyperliquid-account`
- `open-hyperliquid-position`
- `manage-hyperliquid-orders`
- `close-hyperliquid-position`
- `manage-hyperliquid-risk`
- `move-agent-funds`
- `reconcile-hyperliquid-actions`

Their source is the repository's `skills/` directory.

- **Install locations:** jcode `~/.jcode/skills`, Claude Code `~/.claude/skills`, Codex `~/.agents/skills`, or any `--dir`.
- `install` never overwrites files that differ unless you pass `--force`. `uninstall` only removes files that match the bundled version unless you pass `--force`.
- `npx skills add https://github.com/oku-trade/oku-cli/tree/master` needs Git access to the private repository and selects the default `master` branch. `oku skills install` works from the binary alone.

## MCP server

```sh
oku mcp serve                     # read tools only
oku mcp serve --allow-mutations   # also prepare/submit/cancel/reconcile/abandon and positions leverage/margin
```

The server runs over stdio. Stdout carries only MCP protocol frames, and logs go to stderr. `--json` is refused.

- **Tools:** read-only by default. Mutation tools are exposed only with `--allow-mutations`. Login, logout and profile edits are never exposed.
- **Resources:** bundled docs and skills are available as `oku://docs/...` and `oku://skills/...`. There is also a local `drafts_repair` tool.
- **Profile:** `oku --profile NAME mcp serve` pins the server to that profile. Tools use it, and a tool call naming a different profile is refused. Start a separate server for another profile. Without a launch profile, tools use a per-call `profile`, then `$OKU_PROFILE`, then the config default.
- **Login:** the server never prompts. Run `oku auth login` in a terminal first. On Linux, the login (or `oku auth unlock`) keeps the credential unlocked for 8 hours in the kernel keyring; after that, tools return an auth error until the user runs `oku auth unlock` again.
- **Approval:** a digest proves payload integrity, not human approval. Clients must get explicit user approval before calling `orders_submit`. Unknown outcomes follow the same reconcile-never-resend rules as the CLI.

## Local journal

Every intent's state is kept in `journal.sqlite` under the state directory (`oku config path`). Unknown outcomes survive restarts and block new exposure for the same account across all profiles and API origins until reconciled.

## Order states

HTTP success is not a fill. On a timeout or connection loss after submit, the CLI exits 6 with `OUTCOME_UNKNOWN`. Use `oku orders status --intent ID` or `oku orders reconcile`. **Never place a replacement until the original is resolved.**
