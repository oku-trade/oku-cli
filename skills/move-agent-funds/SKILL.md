---
name: move-agent-funds
description: Move Oku Agent EVM funds via send, swap, cross-chain bridge (including Circle CCTP destination claims) and master-bound deposit. Use for agent wallet balances, transfers, swaps, bridging and claiming bridged funds.
---

# Move Oku Agent Funds

Use `oku docs commands --json` to confirm the installed command and flags. Never treat the embedded EVM agent wallet as the Hyperliquid master account. Only the master can deposit to its own Hyperliquid account. Agent USDC must first be transferred to the master EOA on Arbitrum, then the master deposits from its own wallet. `oku deposit address` requests a master-bound HyperUnit deposit address for external funding. Never broadcast an agent transaction directly to the Hyperliquid bridge.

## Inspect, quote, prepare

```sh
oku wallet address --json
oku wallet balances --chain eip155:42161 --json
oku swap quote --chain 42161 --from USDC --to WETH --amount 10 --json
oku swap prepare --chain 42161 --from USDC --to WETH --amount 10 --json
oku send prepare --chain 42161 --token native --amount 0.01 --json
oku deposit address --asset usdc --source-chain eip155:42161 --json
oku deposit prepare --amount 10 --json
```

Resolve ambiguous token symbols with explicit contract addresses. Swap quote and prepare default to the enrolled agent wallet. Swap quotes use the backend `GetFlowstreamQuotes` long-poll, the same stream as the web app; allow up to about 8 seconds to collect routes. Prepare writes a journal intent. Show the recipient, chain, router, minimum output, expiry and digest before submitting. A swap intent expires about 3 minutes after prepare, so obtain approval and submit promptly; if it expires, prepare anew. Keep native ETH on Arbitrum (or native gas on the selected chain) in the agent wallet for swaps and sends. Gas estimates include a buffer but cannot guarantee L2 data fees.

## Submit and reconcile

```sh
oku swap submit --intent ID --digest SHA --json
oku send submit --intent ID --digest SHA --json
oku deposit submit --intent ID --digest SHA --json
oku swap status --intent ID --json
oku send reconcile --intent ID --tx-hash TX_HASH --json
oku swap reconcile --intent ID --tx-hash TX_HASH --json
```

Submitting signs and broadcasts with Privy, so ask for explicit approval first. A transaction swap requiring token approval sends **one atomic** Privy `wallet_sendCalls` batch `[approve exact amount to OkuRouter, swap]` via EIP-7702. The first batch upgrades the agent wallet to a Kernel smart account without changing its address; the approval cannot confirm without the swap. A single-step route (such as native ETH input) sends one normal transaction. Submit waits for the chain's confirmation depth: under a minute on fast L2s such as Arbitrum, several minutes on Ethereum, Polygon or BNB Chain; if it returns OUTCOME_UNKNOWN after broadcast, run status rather than resubmitting. Chain RPC defaults to Oku's eRPC gateway, which serves the receipts and recent history that batch lookup needs; `OKU_RPC_URL_<CHAINID>` overrides it and is required only for chains unknown or deprecated in chain-config.

`INSUFFICIENT_GAS`, `INSUFFICIENT_BALANCE`, or `REJECTED` with **nothing broadcast** closes the intent: correct the problem and prepare a new one. On `OUTCOME_UNKNOWN`, **never retry submit or prepare a replacement**. Run `oku swap status --intent ID --json` (for a batch, this locates it by nonce and confirms its receipt), or `oku swap reconcile --intent ID --tx-hash TX_HASH --json` only with an independently recovered hash; use the corresponding `send` or `deposit` status for those intents. One unresolved intent blocks new onchain intents for the agent. MCP exposes submit and reconcile only with `--allow-mutations`. Privy wallet spending permissions are independent of Hyperliquid agent trading limits.

## Bridge between EVM chains

Bridges move agent funds from a source chain to a destination chain using any transaction-based route Oku quotes (Circle CCTP, Across, Relay, Squid, Bungee, Mayan and others). Intent-based and typed-data signing routes (for example Aori) are skipped.

```sh
oku bridge quote --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10 --json
oku bridge prepare --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10 --json
oku bridge submit --intent ID --digest SHA --json
oku bridge status --intent ID --json
oku bridge claim --intent ID --json        # only when status reports claim_required
oku bridge reconcile --intent ID --tx-hash TX_HASH --json
```

1. **Quote and prepare.** The recipient defaults to the agent wallet on the destination chain; `--recipient` overrides it. Show the user the route, amounts, minimum output, source and destination chains and recipient. **Bridge approvals and calldata are built by the backend and executed as returned, the same trust model as the Oku web app; unlike swaps, the CLI does not independently verify them.** The intent expires about 3 minutes after prepare.
Use `--route NAME` on quote and prepare to request CoW Swap, Aori or another specific backend route, and `--no-intent` to exclude intent-based routes. Intent routes sign the backend's first EIP-712 typed-data payload via Privy, send necessary exact backend-provided approvals as separate ordinary transactions (to Permit2 or a vault relayer as directed), then relay the signature off-chain. The agent sends **no swap or bridge transaction** on intent routes. A journal state of `submitted` means the solver order was relayed, not filled; look for `backend_order_id` and monitor `swap status` or `bridge status`, which use Oku order history for fills, expirations and refunds. A relay timeout/5xx is ambiguous and must never be retried. An intent may remain pending, expire or refund. Review the backend-provided typed-data and route carefully: unlike transaction swaps, intent signing and approval economics are not independently verified by the CLI.

2. **Submit** (explicit user approval required). For transaction bridges, an approval plus the bridge call go out as one atomic batch; a single call is one transaction. Submit confirms the **source** chain only.
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
