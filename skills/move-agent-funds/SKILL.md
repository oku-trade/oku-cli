---
name: move-agent-funds
description: Move Oku Agent EVM funds via send, swap, cross-chain bridge (including Circle CCTP destination claims) and master-bound deposit. Use for agent wallet balances, transfers, swaps, bridging and claiming bridged funds.
---

# Move Oku Agent Funds

Use `oku docs commands --json` to confirm the installed command and flags. The default API is production (`https://accounts.icarus.tools`); profiles retain their configured API URL, so confirm the active one with `oku profiles list`. Set `OKU_RPC_URL_<CHAINID>` to a trusted RPC on each chain used. Never treat the embedded EVM agent wallet as the Hyperliquid master account. Only the master can deposit to its own Hyperliquid account. Agent USDC must first be transferred to the master EOA on Arbitrum, then the master deposits from its own wallet. `oku deposit address` requests a master-bound HyperUnit deposit address for external funding. Never broadcast an agent transaction directly to the Hyperliquid bridge.

## Inspect, quote, prepare

```sh
oku wallet address --json
oku wallet balances --chain eip155:42161 --json
oku wallet fund --chain 42161 --token native --amount 0.01 --json
oku swap quote --chain 42161 --from USDC --to WETH --amount 10 --json
oku swap prepare --chain 42161 --from USDC --to WETH --amount 10 --json
oku send prepare --chain 42161 --token native --amount 0.01 --json
oku deposit address --asset usdc --source-chain eip155:42161 --json
oku deposit prepare --amount 10 --json
```

Resolve ambiguous token symbols with explicit contract addresses. Swap quote and prepare default to the enrolled agent wallet. Swap quotes use the backend `GetFlowstreamQuotes` long-poll, the same stream as the web app; allow up to about 8 seconds to collect routes. Prepare writes a journal intent. Show the recipient, chain, router, minimum output, expiry and digest before submitting. A swap intent expires about 3 minutes after prepare, so obtain approval and submit promptly; if it expires, prepare anew. Keep native ETH on Arbitrum (or native gas on the selected chain) in the agent wallet for swaps and sends. Gas estimates include a buffer but cannot guarantee L2 data fees.

Loss limits are part of the authorization, even in a pre-authorized batch. If a quote exceeds `--max-loss-bps`, skip it or ask for a changed limit. Never add `--allow-high-impact` or raise a limit merely because the trade is small, the absolute fee fits another budget, or the oracle appears inaccurate. Such an override requires explicit authorization.

Pass an authorized tighter limit explicitly as a positive `--max-loss-bps` value. Omitting the flag or passing `0` uses 500 bps, not zero tolerance, despite the help text showing `(default: 0)`.

When the input token is the chain's native gas token and the user requires a minimum native balance to remain afterward, the transaction's own gas is paid from that same balance. Size the input no higher than current balance minus the required floor minus estimated transaction gas minus an additional safety margin for L2 data fees, and never exceed the authorized amount. Take the gas estimate from the quote's `gas_usd` converted to native units, or from a fresh estimate. Do not size it as balance minus floor alone. If the remaining headroom cannot cover gas with margin, reduce the amount or stop and ask the user. After confirmation, re-read the balance and report any shortfall against the floor.

`oku wallet balances` can inspect specific ERC-20 holdings using a `--tokens-file` manifest (`chain_id,token_address,amount`). The `amount` column only sets the `mismatch` annotation and never filters rows. Tokens with a zero balance are omitted, so a requested token missing from a successful (`ok: true`) explicit manifest query has a zero balance. Its absence does not mean the balance equals the requested amount. A failed or malformed `balanceOf` read returns an error rather than omitting the row.

## Submit and reconcile

```sh
oku swap submit --intent ID --digest SHA --json
oku send submit --intent ID --digest SHA --json
oku deposit submit --intent ID --digest SHA --json
oku swap status --intent ID --json
oku send reconcile --intent ID --tx-hash TX_HASH --json
oku swap reconcile --intent ID --tx-hash TX_HASH --json
```

Submitting signs and broadcasts with Privy. The user's concrete action and amount may authorize a matching prepared action after preview review; confirm inferred values and any differences in amount, destination, route, slippage or loss. A standing consolidation instruction is bounded by its named plan and must pause on high impact or high loss. A transaction route with approval uses an atomic `wallet_sendCalls` batch only on chains supporting EIP-7702 with a deployed Kernel delegate. Otherwise it submits exact approval and trade sequentially, so approval can confirm without trade. `status` shows `next_step` at a safe resume boundary; submit the same sealed execution digest again to send only the trade. `OUTCOME_UNKNOWN` still requires status or reconcile before any retry. A single-step route (such as native ETH input) sends one normal transaction. Submit waits for the chain's confirmation depth; if it returns OUTCOME_UNKNOWN after broadcast, run status rather than resubmitting. Set `OKU_RPC_URL_<CHAINID>` for each chain; batch lookup requires historical account state and full block transactions from the chosen provider.

`INSUFFICIENT_GAS`, `INSUFFICIENT_BALANCE`, or `REJECTED` with **nothing broadcast** closes the intent: correct the problem and prepare a new one. `REJECTED: no eligible route` lists per-route reasons in `error.details.routes`; with `retryable: true` (the CLI already re-quoted once), wait 30-60s and quote and prepare once more before skipping, and include those reasons when reporting. On `OUTCOME_UNKNOWN`, **never retry submit or prepare a replacement**. Run `oku swap status --intent ID --json` (for a batch, this locates it by nonce and confirms its receipt), or `oku swap reconcile --intent ID --tx-hash TX_HASH --json` only with an independently recovered hash; use the corresponding `send` or `deposit` status for those intents. An unresolved intent blocks new onchain intents for the agent on that chain only. Other chains remain available; a confirmed bridge source no longer gates its source chain. MCP exposes submit and reconcile only with `--allow-mutations`. Privy wallet spending permissions are independent of Hyperliquid agent trading limits.

## Swap exit orders (take-profit, stop-loss, trailing stop)

```sh
oku swap orders create --chain 42161 --token TOKEN_ADDRESS --all --stop-loss-pct 20 --trailing-stop-pct 10 --json
oku swap orders list --json
oku swap orders watch --once --dry-run --json
oku swap orders cancel --order ID --json
oku swap pnl --chain 42161 --json
```

Exits are client-side and **only fire while `oku swap orders watch` runs**. Tell the user this, and do not describe an order as protecting the position unless a watcher is running. Each order sells the token for native.

- **Cost basis.** Take-profit and stop-loss are relative to the cost basis: confirmed journal buys (`basis_source: journal`, an upper bound from minimum outputs), or `--basis NATIVE` when the journal has no buy. A trailing stop is relative to the highest value seen since creation.
- **Selling.** A triggered sell uses the same prepare and submit guardrails as a manual swap. Creating an order and running watch are both fund movements under the user's authorization.
- **Stop-loss is not a guaranteed price.** Checks run once per interval on oracle prices and the sell is a market swap, so on a fast drop it can fill well below the stop. If the quote then exceeds `--max-loss-bps`, the sell is refused and the order stays `active`, retrying every check without using an attempt. Tell the user this, and never suggest `--allow-high-impact` to make a stop fire.
- **`--all`.** Triggers are valued on the balance recorded at creation, but the sell uses the full balance held at trigger time.
- **Dry run.** Use `--dry-run` to show what would trigger without trading.
- **Failed sells.** A sell that could not be prepared or was never broadcast re-arms the order, up to 3 attempts. A `failed` order with `needs_reconcile: true` had an unknown submit outcome. Run `oku swap status --intent SELL_INTENT_ID`, and never recreate or retry the sell before it resolves.

## Bridge between EVM chains

Bridges move agent funds from a source chain to a destination chain using transaction routes (Circle CCTP, Across, Relay, Squid, Bungee, Mayan and others) or intent routes such as Aori.

```sh
oku bridge quote --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10 --json
oku bridge prepare --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10 --json
oku bridge submit --intent ID --digest SHA --json
oku bridge status --intent ID --json
oku bridge status --all --json          # list unsettled bridges, or omit --intent
oku bridge claim --ready --json         # claim eligible bridges sequentially, continue on errors
oku bridge claim --intent ID --json        # only when status reports claim_required
oku bridge reconcile --intent ID --tx-hash TX_HASH --json
```

1. **Quote and prepare.** The recipient defaults to the agent wallet on the destination chain; `--recipient` overrides it. Show the user the route, amounts, minimum output, source and destination chains and recipient. **Bridge approvals and calldata are built by the backend and executed as returned, the same trust model as the Oku web app; unlike swaps, the CLI does not independently verify them.** The intent expires about 3 minutes after prepare.
Use `--route NAME` on quote and prepare to request CoW Swap, Aori or another specific backend route, and `--no-intent` to exclude intent-based routes. Intent routes sign the backend's first EIP-712 typed-data payload via Privy, send necessary exact backend-provided approvals as separate ordinary transactions (to Permit2 or a vault relayer as directed), then relay the signature off-chain. The agent sends **no swap or bridge transaction** on intent routes. A journal state of `submitted` means the solver order was relayed, not filled; look for `backend_order_id` and monitor `swap status` or `bridge status`, which use Oku order history for fills, expirations and refunds. A relay timeout/5xx is ambiguous and must never be retried. An intent may remain pending, expire or refund. Review the backend-provided typed-data and route carefully: unlike transaction swaps, intent signing and approval economics are not independently verified by the CLI.

CoW Swap can reject quotes below about $25 notional when a flat fee applies. Use `--route cowswap` to surface the backend reason; do not retry a failed quote automatically. On `INSUFFICIENT_GAS` or `INSUFFICIENT_BALANCE`, run `oku wallet fund --chain CHAIN --token native|usdc|TOKEN_ADDRESS` and ask the user to pay through the displayed QR or web page. This command never signs or transfers; omit `--amount` if the amount is unspecified, and use `--no-open` to avoid opening the browser.

2. **Submit** (requires user approval: explicit per intent, or a standing consolidation instruction under `consolidate-agent-funds` with its guardrails). For transaction bridges, an approval plus the bridge call go out as one atomic batch; a single call is one transaction. Submit confirms the **source** chain only.
3. **Status.** `bridge status` reports destination delivery for every bridge from Oku's order history (the web app's source), in `destination_fill.status`:
   - `pending`: in flight. Circle Standard typically takes 15-20 minutes; Circle Fast and most other bridges take seconds to a few minutes.
   - `filled`: delivered; `fill_tx` is the destination transaction and `destination_settled` is true.
   - `awaiting_claim`: a destination claim is required (see below); `claim_required` is true.
   - `refunded`, `failed`, `expired`: the bridge did not deliver; report it to the user, never re-bridge automatically.
   - `unknown`: order history was unavailable; retry later. It does not mean funds are lost.

### Circle CCTP claims

Circle routes (`circleStandard`, `circleFast`) burn USDC on the source chain and **mint on the destination chain only when someone submits Circle's attestation there**. A relayer may do this automatically, but often nobody does and the order becomes `awaiting_claim`, exactly when the Oku web app shows its "Claim Bridge" button. Expect this for Circle routes and check for it:

1. Poll `oku bridge status --intent ID --json` until `destination_fill.status` is `filled` or `claim_required` is `true`.
2. If `claim_required` is true, make sure the agent holds **native gas on the destination chain** (for example a little ETH on Base). The claim is sent from the agent wallet on the destination chain, so it fails with `INSUFFICIENT_GAS` otherwise; ask the user to fund it.
3. Run `oku bridge claim --intent ID --json` (the original bridge intent ID). It reads the claim transaction from Oku's order history, sends it on the destination chain, waits for confirmation and reports it to Oku (`FinishClaim`). Results: `claim confirmed`, `already claimed` (a relayer or someone else minted first; success, nothing sent), or `no claim needed` (not claimable yet or already delivered).
4. Confirm with `bridge status`: it should show `filled` with the claim transaction as `fill_tx`.

Claims are journaled like other onchain intents and are **never sent twice**. Running `bridge claim` early or repeatedly is safe. If a claim broadcast but could not be confirmed (for example an RPC outage), rerun `oku bridge claim --intent ID`; it confirms the recorded transaction instead of resending. The claim calldata comes from the backend and is trusted as in the web app. An unclaimed Circle transfer is not lost: Oku re-requests the attestation after 24 hours, and the claim can be submitted later.
