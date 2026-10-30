# Usage

### Multi-step source execution

Approve plus trade uses an atomic EIP-7702 `wallet_sendCalls` batch only when the chain supports 7702 and Privy's Kernel delegate has code on that chain. Otherwise it uses ordinary sequential transactions: exact-amount approve, wait for confirmations, recheck allowance, then trade. **An approve can confirm without the trade** on a sequential chain. Submit JSON reports `execution_mode: batch|sequential`. `swap status --intent ID` or `bridge status --intent ID` reports `next_step` when the approve is confirmed but the trade was not sent. Submit the sealed execution digest again to resume only the trade; never retry an ambiguous broadcast. The Privy Kernel implementation address, observed in its 2026-10-19 Arbitrum authorization, is `0xd6CEDDe84be40893d153Be9d467CD6aD37875b28`.

## Agent EVM transfers and master deposits

`oku wallet balances --chains 42161,8453 --tokens-file sweep.csv` reads a CSV manifest with `chain_id,token_address,amount` (optional header). Without `--chain`, `--chains` or `--all-chains`, it reads every chain listed in the CSV. Native rows use the zero address (or `0xEeee...EeE`) and are reported on the chain's native balance row, with `csv_amount` and `mismatch`. It validates and deduplicates token addresses, and reports live on-chain ERC-20 balances beside informational `csv_amount` with `mismatch` when they differ. The CSV amount is never used for spending. Without `--tokens-file`, only native balances are shown with a hint. Native balances use `eth_getBalance`; token balance, symbol and decimals are read in one Multicall3 `aggregate3` per selected chain. The manifest is the source of truth for candidate token addresses; no indexed holdings backend or log discovery is used. Use `--chains 42161,8453`, or `--all-chains` for every active Oku chain. Each chain is read independently: a chain that fails or has no RPC endpoint is listed under `errors` (`chain`, `code` of `NO_RPC` or `UPSTREAM_ERROR`, `message`) and left out of `chains` and `native`, while the other chains are still returned. The command fails only when no chain can be read. `send prepare`, `swap quote`/`prepare`, and `bridge quote`/`prepare` accept `--amount max` or `--amount all`, resolving from the latest onchain balance. Everywhere a token is accepted (`--from`, `--to`, `--token`, CSV rows), `native`, the zero address and the EIP-7528 `0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE` all mean the native asset, and native is sent to the backend as the zero address. Native inputs reserve gas using RPC gas-price and gas-limit estimates (floored at an 800k-gas route budget for swap and bridge) with a 1.5x buffer and L2 margin; `--gas-reserve` overrides that estimate. For swap and bridge, executable calldata is unavailable before the quote, so this preliminary estimate models a zero-value transfer and cannot guarantee route execution fees; use a larger explicit reserve for complex routes. Full-balance transfers can leave dust or fail for fee-on-transfer or rebasing tokens. Quotes may change before preparation or execution; route slippage remains capped at 300 bps. Swap quotes report `loss_bps` (priced market loss excluding the Oku fee), `high_impact` and `no_simulation`. `swap prepare` refuses when `loss_bps` exceeds `--max-loss-bps` (default 500), unless you pass `--allow-high-impact` after reviewing the quote. Small chains with thin pools or stale oracles often trip this. Only losses count: a quote priced above its input never trips it. Routes whose market returns no simulation are accepted: min-out is derived from the quoted output and slippage and is enforced by the swap calldata, with the OkuRouter target and calldata checks unchanged. `wallet balances --stables` adds each chain's configured stablecoins from chain-config.

`oku send prepare --chain 42161 --token native --amount 0.01` prepares an agent-wallet transfer to the enrolled master by default. For ERC-20 use `--token TOKEN_ADDRESS`; specify `--to RECIPIENT_ADDRESS` explicitly to send elsewhere. Review the returned recipient, amount, chain, calldata and digest, then run `oku send submit --intent ID --digest SHA`. Use `oku send status --intent ID` to check a receipt. Any EVM chain ID works. Common chains have a built-in public RPC; for others, or to use a better endpoint, set `OKU_RPC_URL_<CHAINID>` (e.g. `OKU_RPC_URL_42161`). Keep native gas (e.g. ETH on Arbitrum) in the agent wallet for sends and swaps.

`oku deposit prepare --amount 10` prepares **only** an Arbitrum native-USDC transfer from the agent to the profile master EOA. After reviewing it, run `oku deposit submit --intent ID --digest SHA` and `oku deposit status --intent ID`. This does **not** deposit into Hyperliquid. The master must deposit from its own wallet on the Oku web deposit page (`https://oku.trade/perps`). For a master-bound HyperUnit address use `oku deposit address --asset ASSET --source-chain CHAIN` and fund that address externally. Never transfer USDC from the agent to the Hyperliquid bridge: that credits the agent, not the master.

A timeout or ambiguous Privy broadcast becomes `OUTCOME_UNKNOWN`. Never repeat `submit` or prepare a replacement. Inspect `status` and the source-chain explorer. For an approval+swap batch, `oku swap status --intent ID` can locate a transaction by its recorded agent nonce even if Privy returned no onchain hash, then confirm its receipt. Alternatively, `oku swap reconcile --intent ID --tx-hash TX_HASH` verifies an independently recovered hash against the journal. An unresolved intent blocks new onchain intents for the same agent on the same chain until resolved. Different chains may proceed concurrently, and a confirmed bridge source does not block its source chain while destination delivery remains pending. The Privy OAuth `eth_sendTransaction` request uses top-level `caip2` (such as `eip155:42161`) and `params.transaction` with `chain_id`; approval+swap uses `wallet_sendCalls`.

`INSUFFICIENT_GAS`, `INSUFFICIENT_BALANCE`, or `REJECTED` with “nothing broadcast” means the preflight intent is closed. Correct the cause, then prepare a fresh intent. A post-broadcast receipt lookup error requires `status`, never resubmission.

> `oku --help`, `oku docs commands` and the [generated command reference](commands.md) describe the urfave/cli v3 command tree of the binary you run. `make docs-check` detects drift between the committed reference and the tree.

## Output contract

- `--json` writes exactly one JSON document to **stdout**. Diagnostics go to **stderr**.
- Envelope fields: `schema_version` (`oku.cli/v1`), `request_id`, `command`, `ok`, `data`, and `error` (`code`, `message`, `retryable`, optional `details`) or `null`.
- Quantities and prices are decimal strings.
- Perps trading supports Hyperliquid **mainnet** only; swaps, bridges and sends use `--chain`. Trading requires a profile with an account. In automation, always pass `--profile` explicitly.

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

- **Linux (default):** a plain JSON file at `~/.config/oku/credentials/privy/<profile>.json`, mode 0600, readable only by your user. There is no passphrase, so agents, MCP and scripts can use it as soon as you log in. Like an SSH key without a passphrase, anyone who can read the file can act as your agent until you revoke it.
- **Linux, opt-in desktop keyring:** `oku auth login --credential-store keyring` (or `OKU_CREDENTIAL_STORE=keyring`) stores the credential in the freedesktop Secret Service (GNOME Keyring, KWallet, KeePassXC) instead, encrypted at rest with your login password. The choice is saved per profile. Move an existing profile with `oku auth migrate --to keyring` or back with `--to file`. If the keyring is unavailable the command fails; it never falls back to the file.
- **macOS:** the native Keychain. Official macOS builds need CGO and are built natively.

  If the login Keychain is locked, an interactive command can invoke `security unlock-keychain` to ask for the password in the user's own terminal, then retry once. After a binary update, macOS may ask whether the new binary can use the credential. Choose **Always Allow** to trust that binary. For headless, `--json`, MCP and Service calls there is no prompt: `KEYCHAIN_LOCKED`, `KEYCHAIN_ACCESS_DENIED` and `KEYCHAIN_CANCELED` are distinct JSON errors (exit 3). Run `oku auth status --verify` in a graphical terminal to approve an updated binary, or unlock in the user's terminal with `security unlock-keychain ~/Library/Keychains/login.keychain-db`. `oku doctor` diagnoses access without prompting. In SSH without a GUI, approve the new binary in a local graphical session first. macOS cannot migrate credentials to the Linux file store.
- **Upgrading from a passphrase-protected credential:** older releases encrypted the Linux file with a passphrase. Commands now refuse that file with `AUTH_REQUIRED` and ask you to run `oku auth unlock` once in your own terminal. It asks for the old passphrase (or reads `OKU_PASSPHRASE_FILE`), converts the file in place and never asks again. Afterwards you can delete the passphrase file. `oku auth logout` also works on an unconverted file but cannot revoke its session or grant.
- See [security](security.md#credential-storage-by-platform).

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

Only command-specific options belong in the object, without `--`. Keep `--profile`, `--json`, and other globals as flags. No positional arguments or command flags may accompany `--input`. Unknown keys, wrong types, trailing values, and inputs over 1 MiB fail with USAGE. Commands without flags and interactive login/logout do not accept it. `drafts repair --stdin` cannot share stdin with `--input -`.

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

oku auth login                          # production API by default; sign at the authorize page Privy returns
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
oku orders intents [--unresolved] [--state S] [--limit N]   # local journal only
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

After `oku bridge status --intent ID`, if `claim_required` is true, run `oku bridge claim --intent ID --json`. Use `oku bridge status --all --json` (or omit `--intent`) to list unsettled bridge intents, including destination status and claim intent IDs. Bridges that were only prepared, never submitted, and whose quote expired moved no funds, so they are hidden. Add `--include-expired` to show them, labeled `expired`. `claim --ready` and the per-chain gate ignore them. Use `oku bridge claim --ready --json` to process claimable bridges sequentially across destination chains; it continues after per-intent failures and never resends a recorded claim. Some Circle routes require a destination-chain claim. The CLI reads the agent wallet's bridge order history, sends the backend-provided `claimTo` and `claimCalldata` as one transaction on the destination chain, waits for confirmation, then reports the transaction to the backend. Claim calldata is trusted exactly as in the Oku web app, not independently verified. The agent must hold native gas and have `OKU_RPC_URL_<CHAINID>` configured on the destination chain. Claims are separately journaled and never resent: rerunning `bridge claim` after an unconfirmed claim checks its recorded transaction, confirms it and reports it to Oku.

The embedded Privy wallet is an EVM wallet, **not** the Hyperliquid master account. Use `wallet address` to confirm both addresses. `wallet balances` reads native gas and recommended token balances on the selected EVM chain. To fund the agent from an external wallet, run `oku wallet fund --chain 42161 --token native --amount 0.01` or `oku wallet fund --token usdc --no-open`. It prints an EIP-681 payment URI, terminal QR code and Oku funding page link; `--json` returns `agent`, `chain`, `token`, `amount`, `payment_uri`, and `web_url`. Omit `--amount` for an unspecified amount. The browser opens only in an interactive terminal unless `--no-open` is set; `OKU_WEB_URL` overrides the web origin. The command never signs or sends funds. On insufficient gas or token balance, ask the user to fund the agent, then prepare a fresh intent. `--chain` accepts any supported EVM chain ID (default 42161). Swaps work on any chain where Oku's swap markets are active; `swap quote` reports `REJECTED` for a chain without active markets. CoW Swap may be unavailable below about $25 notional when a flat fee applies; `--route cowswap` surfaces the backend rejection reason. When no route is eligible, `REJECTED` lists a short reason per route (for example `amount_mismatch: got X want Y`, `simulation_failed: ...`, `not_answered`) in the message and in `error.details.routes`. If the backend answered but every route was filtered, the CLI re-quotes once (`error.details.attempts`) and marks the error `retryable: true`; wait 30-60s and retry once more. Missing markets or bridges for the chain stay `retryable: false`.

```sh
oku swap quote --chain 42161 --from USDC --to WETH --amount 10 --slippage-bps 50
oku swap prepare --chain 42161 --from USDC --to WETH --amount 10
oku swap submit --intent ID --digest SHA
oku swap status --intent ID
oku swap reconcile --intent ID --tx-hash TX_HASH # only for unknown outcome with an independently recovered hash

oku bridge quote --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10
```

Amounts are exact input decimals. The slippage default is 50 bps, maximum 300 bps. Token symbols resolve through TokenService; when ambiguous, specify an address. Swap quote and prepare default to the enrolled agent address using credential metadata without unlocking. Without enrolled credentials, a quote requires explicit `--recipient`. Quotes use the backend `GetFlowstreamQuotes` long-poll, as the web app does, and can take up to about 8 seconds to collect routes. Prepare journals a digest binding chain, OkuRouter, recipient, exact input and minimum output; the intent expires about 3 minutes after prepare, so review and submit promptly. Intent-based CoW-like swaps and Aori-like bridges are also available. Use `--route NAME` on quote or prepare to select a specific router/bridge, or `--no-intent` to exclude typed-data routes. The JSON quote and prepared journal record include `intent_based`. Intent submission signs the backend's first EIP-712 typed-data request with Privy, sends only necessary backend-provided approvals as ordinary transactions (never `wallet_sendCalls`), then relays the signed request via `GetExecutionInfo`. No onchain swap or bridge transaction is sent by the agent. The order ID in `backend_order_id` is the solver's intent ID; `submitted` means relayed, **not filled**. `swap status` and `bridge status` read Oku order history and show pending, filled, failed, expired, or refunded, including fill hashes and an explorer URL when available. A timeout during relay leaves `unknown`: never retry submit; use status to find the order. Solvers may not fill, and an order can expire or refund. Intent signing and approval calls trust the backend exactly as the web app does; unlike transaction swap routes, the CLI does not independently verify EIP-712 economic fields or route calldata. Approval spenders may be a Permit2 contract or a vault relayer. Review the route before submission.

Transaction-based swap submission still verifies the non-permit OkuRouter selector and decoded arguments, and exact-amount router approval. When approval is needed, execution is atomic only when EIP-7702 and the Privy Kernel implementation are available. Otherwise approval and trade execute sequentially, with the partial-approval risk described above. A single-step route (such as native ETH input) sends one normal transaction. Submit waits for the chain's confirmation depth (under a minute on fast L2s, several minutes on Ethereum, Polygon or BNB Chain). Routes go only to that chain's OkuRouter as listed in the shared chain-config networks package, and submit checks it has contract code. Quoted output and simulation are checked; the minimum in aggregator calldata is backend-built and not independently decoded. See security.md. Transaction-based Oku bridges support `bridge quote`, `bridge prepare --chain SOURCE --destination-chain DEST --from TOKEN --to TOKEN --amount AMOUNT`, `bridge submit --intent ID --digest SHA`, `bridge status --intent ID`, and `bridge reconcile --intent ID --tx-hash HASH`. Only source-chain RPC is required. The backend builds bridge approvals and transaction calldata; unlike transaction swaps, the CLI does not independently verify bridge economic effects, recipient, target contract or approval limit. Multiple calls use atomic `wallet_sendCalls` where available, otherwise sequential `eth_sendTransaction`; one call uses `eth_sendTransaction`. `bridge status` reports destination delivery for every bridge from Oku's order history (`filled` with the destination transaction, `awaiting_claim`, `refunded`, `failed`, `expired` or `pending`).

Unknown broadcasts must never be resubmitted. Start with `swap status --intent ID`: for an approval batch it searches by nonce and confirms by receipt even if no hash was returned. If a hash is recovered independently, use `swap reconcile --intent ID --tx-hash TX_HASH` (or `send reconcile` for a send) to match the exact journaled call(s) and wait for confirmations. Batch lookup needs historical account state and full block transactions from the user-configured `OKU_RPC_URL_<CHAINID>` provider. Gas reserves include estimated gas plus 30% at the fee cap, but L2 data fees and Privy fee choices remain uncertain.

## Swap exit orders (take-profit, stop-loss, trailing stop)

`oku swap orders` adds **client-side** exits for an EVM token position held by the agent wallet. When one fires, the CLI sells the token for the chain's native asset. **Exits only fire while `oku swap orders watch` is running.** Nothing is placed with Oku, a DEX or a solver, so if the watcher is stopped, asleep, offline or out of gas, no exit happens.

```sh
oku swap orders create --chain 42161 --token 0x912CE59144191C1204E64559FE8253a0e49E6548 --all \
  --take-profit-pct 50 --stop-loss-pct 20 --trailing-stop-pct 10 [--slippage-bps 50] [--basis NATIVE]
oku swap orders list [--state active|triggered|executing|filled|failed|cancelled]
oku swap orders cancel --order ID
oku swap orders watch [--interval 15s] [--once] [--dry-run]
```

- **Order setup.** `create` needs `--amount X` or `--all` and at least one of `--take-profit-pct`, `--stop-loss-pct` or `--trailing-stop-pct`. `--all` records the current token balance as the position size. Triggers are always valued on that recorded size, but at trigger time `--all` sells the full balance then held, with the normal gas reserve and max/all handling. If the balance changed after creation, the amount sold differs from the amount valued.
- **Cost basis.** Take-profit and stop-loss are measured against the native cost of the position:
  - By default the cost comes from the same average-cost book as `oku swap pnl` (below): the agent's own journaled swaps of the token, net of tracked sells, labeled `basis_source: journal`. Swaps confirmed by this version record the amount actually received; older swaps without a recorded fill use their minimum output, which can overstate the entry price.
  - `--basis NATIVE` sets it explicitly (`basis_source: override`). It is required when the journal has no confirmed buy.
  - A trailing stop alone needs no basis. It measures from the highest value seen since the order was created (`peak_value`), which only moves up.
- **Pricing.** Each check reads oracle prices with `TokenService/GetTokenPrice`, which creates no backend state, and values the position in native as token USD price divided by native USD price. This is an oracle valuation, not an executable quote. The sell is prepared only after an exit fires.
- **Selling.** The sell goes through exactly the same path as `swap prepare` and `swap submit`: OkuRouter calldata verification, the `--max-loss-bps` high-impact refusal (default 500 bps), the 0-300 bps slippage cap, quote expiry and the per-chain lock. Intent-based routes are excluded.
- **Fast drops.** A stop is checked only once per `--interval`, against an oracle price, and the sell is a market swap. On a fast drop the price can fall well past the stop before the next check, and the sell then fills at the price available, not at the stop level. A crash that also makes the quote exceed `--max-loss-bps` is refused like any manual swap: the order stays `active` and retries every check (these refusals do not count as attempts), so it sells only once liquidity recovers or the order is cancelled. A stop-loss is not a guaranteed exit price.
- **One sell at a time.** At most one sell is in flight per check, and one watcher runs per profile. A second `watch` is refused.
- **States.** An order moves `active` -> `triggered` -> `executing` -> `filled` or `failed` (or back to `active` when nothing was signed), and records `trigger`, `last_price`, `last_value`, `peak_value` and `sell_intent_id`. `cancel` works only on `active` orders and is refused once an order has triggered.
- **Failed sells.**
  - If the sell cannot be prepared (no route, balance), or was prepared but never broadcast (for example a submit refused for gas), nothing was signed. The order returns to `active`, retries with a new sell intent ID (`ORDER-sell-N`), and fails after 3 attempts. A high-impact refusal returns the order to `active` without using an attempt.
  - A submit with an unknown outcome moves the order to `failed` with `needs_reconcile: true` and is **never retried**. Run `oku swap status --intent SELL_INTENT_ID`.
- **Watch options.**
  - `--once` runs one check and exits.
  - `--dry-run` reports prices and `would_trigger` without preparing, signing or changing the journal.
  - Without `--once`, watch logs each check to stderr and exits on Ctrl-C or when no active orders remain.
  - `--timeout` applies to each request, not the whole loop.
  - Over MCP, `swap_orders_watch` requires `once: true`. Create, cancel and watch are mutation tools; list is a read.

## Swap PnL

```sh
oku swap pnl [--chain 42161] [--token TOKEN_ADDRESS] [--json]
```

`swap pnl` reports realized and unrealized profit and loss per token for swaps **the agent made**, computed from the local journal. It never signs or writes on chain. It reads token decimals and balances over RPC and prices through `TokenService/GetTokenPrice`. Tokens moved by swaps or transfers outside the agent are ignored. Without `--chain`, it reports every chain that has agent swaps in the journal.

- **Which swaps count.** Confirmed transaction swaps whose sender and recipient are the agent. Intent swaps are not counted: a submitted intent can still expire, refund or stay pending at the backend, and its fill is not journaled. Prepared, in-flight, unknown, failed and abandoned swaps are skipped. Swaps to another recipient are counted under `skipped.other_recipient`.
- **Actual fill.** When a swap confirms (on `swap submit`, `swap status` or `swap reconcile`), the CLI reads its receipts and records the amount the agent received: ERC-20 `Transfer` logs into the agent, or OkuRouter's swap event for native output. It also records the gas paid, including any L1 data fee the receipt reports. Swaps journaled before this have no receipt fill and use the quoted minimum output. Their rows are marked `approximate` (`~` in the table).
- **Average cost.** A buy (native -> token) adds quantity and cost. A sell (token -> native) removes quantity at the current average cost: realized += proceeds - average cost x quantity. A token -> token swap is a sell of the input and a buy of the output at the input's oracle native value, recorded when the fill is recorded. Without that price, the input's cost basis carries over to the output and nothing is realized (approximate). Quantities sold beyond what the agent's swaps bought are reported as `untracked_sold` and do not realize.
- **Gas.** `fees_native` is the swap gas attributed to the token: the bought token of a native -> token swap, the sold token of a token -> native swap, and the output token of a token -> token swap. **Realized PnL is net of that gas** (`realized_includes_gas: true`). `fees_unknown_swaps` counts swaps whose gas was not recorded.
- **Unrealized.** Held x oracle price - remaining cost basis. With no price, `unrealized_native`, `value_native` and the matching total fields are omitted (`?` in the table), never 0. A token fully sold has value and unrealized 0.
- **Exact math.** Amounts are big rationals until output. Native values print with up to 18 decimals and USD with 2, rounded half away from zero.

JSON `data` fields:

| Field | Meaning |
| --- | --- |
| `profile`, `agent` | active profile and its agent wallet |
| `method` | text summary of the calculation |
| `realized_includes_gas` | always `true`: realized PnL is net of swap gas |
| `chains[].chain_id`, `native_symbol`, `native_price_usd` | chain and its native oracle USD price (omitted without a price) |
| `chains[].tokens[]` | one row per token held or traded: `token`, `symbol`, `decimals` (null when unknown), `held`, `wallet_balance`, `avg_cost_native` (per whole token, omitted when nothing is held), `cost_basis_native`, `price_native`, `value_native`, `value_usd`, `unrealized_native`, `unrealized_usd`, `realized_native`, `realized_usd`, `fees_native`, `fees_usd`, `fees_unknown_swaps`, `untracked_sold`, `untracked_bought`, `swaps`, `approximate`, `price_known` |
| `chains[].total` | `cost_basis_native`, `value_native`, `value_usd`, `unrealized_native`, `unrealized_usd`, `realized_native`, `realized_usd`, `fees_native`, `fees_usd`, `approximate`. Value and unrealized are omitted when any row's is unknown |
| `chains[].skipped` | `other_recipient` and `invalid` journal swaps left out |
| `chains[].warnings` | RPC or price failures that left fields unknown |

Amounts are decimal strings in token units (`held`) or native units (`*_native`). Over MCP, `swap_pnl` is a read tool.

## Agent-to-master deposits

`oku deposit prepare --amount N` prepares an Arbitrum USDC transfer **from agent to master EOA**, not a deposit to Hyperliquid. The master then deposits from its own wallet. `oku deposit address --asset ... --source-chain ...` reads a HyperUnit address explicitly bound to the master for external funding. Never send agent-held USDC directly to the HL bridge.

## Agent skills

Twelve skills are bundled:

- `learn-oku`
- `ensure-hyperliquid-authentication`
- `inspect-hyperliquid-account`
- `open-hyperliquid-position`
- `manage-hyperliquid-orders`
- `close-hyperliquid-position`
- `manage-hyperliquid-risk`
- `move-agent-funds`
- `consolidate-agent-funds`: consolidate agent-wallet balances from optional CSV, supplied list or live discovery into user-chosen chain/token targets
- `coinbase-deposit-targets`: native USDC targets when the user wants to end on or send to a Coinbase deposit
- `reconcile-hyperliquid-actions`
- `troubleshoot-macos-keychain`

Their source is the repository's `skills/` directory.

- **Install locations:** jcode `~/.jcode/skills`, Claude Code `~/.claude/skills`, Codex `~/.agents/skills`, or any `--dir`.
- `install` never overwrites files that differ unless you pass `--force`. `uninstall` only removes files that match the bundled version unless you pass `--force`.
- `oku skills install` works from the installed binary without accessing the source repository.

## MCP server

```sh
oku mcp serve                     # read tools only
oku mcp serve --allow-mutations   # also prepare/submit/cancel/reconcile/abandon and positions leverage/margin
```

The server runs over stdio. Stdout carries only MCP protocol frames, and logs go to stderr. `--json` is refused.

- **Tools:** read-only by default. Mutation tools are exposed only with `--allow-mutations`. Login, logout and profile edits are never exposed.
- **Resources:** bundled docs and skills are available as `oku://docs/...` and `oku://skills/...`. There is also a local `drafts_repair` tool.
- **Profile:** `oku --profile NAME mcp serve` pins the server to that profile. Tools use it, and a tool call naming a different profile is refused. Start a separate server for another profile. Without a launch profile, tools use a per-call `profile`, then `$OKU_PROFILE`, then the config default.
- **Login:** the server never prompts. Run `oku auth login` in a terminal first; the server can use the credential from then on. A credential from an older passphrase-protected release returns an auth error until the user runs `oku auth unlock` once.
- **Authorization:** a digest proves payload integrity, not user authorization. A clear user instruction to trade, close, cancel or change risk, or a standing instruction for a defined batch, authorizes the corresponding submits within that scope. Clients must prepare and validate each payload against the authorized account, market, side, size, price/slippage and risk bounds, but need not ask for another confirmation of every preview. Ask when required inputs are missing or an action would exceed the authorized scope. Re-preparing an expired or definitively rejected action does not require another confirmation if all original bounds still hold. Agent EVM fund movements under the `consolidate-agent-funds` skill follow its standing authorization and guardrails. Unknown outcomes follow the same reconcile-never-resend rules as the CLI.

## Local journal

Every intent's state is kept in `journal.sqlite` under the state directory (`oku config path`). Unknown outcomes survive restarts and block new exposure for the same account across all profiles and API origins until reconciled.

## Order states

HTTP success is not a fill. On a timeout or connection loss after submit, the CLI exits 6 with `OUTCOME_UNKNOWN`. Use `oku orders status --intent ID` or `oku orders reconcile`. **Never place a replacement until the original is resolved.**
