---
name: move-agent-funds
description: Move agent EVM funds via send, swap, backend-built transaction bridges and master-bound deposit.
---

# Move Oku Agent Funds

Use `oku docs commands --json` to confirm the installed command and flags. Never treat the embedded EVM agent wallet as the Hyperliquid master account. Only the master can deposit to its own Hyperliquid account. Agent USDC must first be transferred to the master EOA on Arbitrum, then the master deposits from its own wallet. `oku deposit address` requests a master-bound HyperUnit deposit address for external funding. Never broadcast an agent transaction directly to the Hyperliquid bridge.

## Inspect, quote, prepare

```sh
oku wallet address --json
oku wallet balances --chain eip155:42161 --json
oku swap quote --chain 42161 --from USDC --to WETH --amount 10 --json
oku bridge quote --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10 --json
oku bridge prepare --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10 --json
oku swap prepare --chain 42161 --from USDC --to WETH --amount 10 --json
oku send prepare --chain 42161 --token native --amount 0.01 --json
oku deposit address --asset usdc --source-chain eip155:42161 --json
oku deposit prepare --amount 10 --json
```

Resolve ambiguous token symbols with explicit contract addresses. Swap quote and prepare default to the enrolled agent wallet. Swap quotes use the backend `GetFlowstreamQuotes` long-poll, the same stream as the web app; allow up to about 8 seconds to collect routes. Transaction-based Oku bridges support quote, prepare, submit, status and reconcile. Only the source chain needs RPC; intent-based and typed-data routes are skipped. **Bridge approvals and calldata come from the backend and are not independently verified by this CLI, unlike swaps.** Inspect the route and recipient carefully before approving. A bridge intent expires about 3 minutes after prepare. Multiple calls execute atomically; `bridge status` reports destination delivery for every bridge from Oku's order history (filled with the destination tx, awaiting_claim, refunded, failed, expired or pending); `unknown` only means history was unavailable, not that funds are lost. Prepare writes a journal intent. Show the recipient, chain, router, minimum output, expiry and digest before submitting. A swap intent expires about 3 minutes after prepare, so obtain approval and submit promptly; if it expires, prepare anew. Keep native ETH on Arbitrum (or native gas on the selected chain) in the agent wallet for swaps and sends. Gas estimates include a buffer but cannot guarantee L2 data fees.

## Submit and reconcile

```sh
oku swap submit --intent ID --digest SHA --json
oku bridge submit --intent ID --digest SHA --json
oku bridge status --intent ID --json
oku bridge claim --intent ID --json  # only when status says claim_required
oku bridge reconcile --intent ID --tx-hash TX_HASH --json
oku send submit --intent ID --digest SHA --json
oku deposit submit --intent ID --digest SHA --json
oku swap status --intent ID --json
oku send reconcile --intent ID --tx-hash TX_HASH --json
oku swap reconcile --intent ID --tx-hash TX_HASH --json
```

Circle bridges may require a claim after the source confirms. When bridge status reports `claim_required`, fund the agent with native gas on the destination chain and run `oku bridge claim --intent ID`. The CLI trusts the backend claim calldata exactly as the Oku web app does. A claim has its own durable journal intent and is never sent twice: if submit could not confirm it (for example an RPC outage), rerun `oku bridge claim --intent ID`, which confirms the recorded transaction instead of resending.

Submitting signs and broadcasts with Privy, so ask for explicit approval first. A swap requiring token approval sends **one atomic** Privy `wallet_sendCalls` batch `[approve exact amount to OkuRouter, swap]` via EIP-7702. The first batch upgrades the agent wallet to a Kernel smart account without changing its address; the approval cannot confirm without the swap. A single-step route (such as native ETH input) sends one normal transaction. Submit waits for the chain's confirmation depth: under a minute on fast L2s such as Arbitrum, several minutes on Ethereum, Polygon or BNB Chain; if it returns OUTCOME_UNKNOWN after broadcast, run status rather than resubmitting. Chain RPC defaults to Oku's eRPC gateway, which serves the receipts and recent history that batch lookup needs; `OKU_RPC_URL_<CHAINID>` overrides it and is required only for chains unknown or deprecated in chain-config.

`INSUFFICIENT_GAS`, `INSUFFICIENT_BALANCE`, or `REJECTED` with **nothing broadcast** closes the intent: correct the problem and prepare a new one. On `OUTCOME_UNKNOWN`, **never retry submit or prepare a replacement**. Run `oku swap status --intent ID --json` (for a batch, this locates it by nonce and confirms its receipt), or `oku swap reconcile --intent ID --tx-hash TX_HASH --json` only with an independently recovered hash; use the corresponding `send` or `deposit` status for those intents. One unresolved intent blocks new onchain intents for the agent. MCP exposes submit and reconcile only with `--allow-mutations`. Privy wallet spending permissions are independent of Hyperliquid agent trading limits.
