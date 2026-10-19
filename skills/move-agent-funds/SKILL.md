---
name: move-agent-funds
description: Move agent EVM funds via send, swap and master-bound deposit; bridge quotes are read-only.
---

# Move Oku Agent Funds

Use `oku docs commands --json` to confirm the installed command and flags. Never treat the embedded EVM agent wallet as the Hyperliquid master account. Only the master can deposit to its own Hyperliquid account. Agent USDC must first be transferred to the master EOA on Arbitrum, then the master deposits from its own wallet. `oku deposit address` requests a master-bound HyperUnit deposit address for external funding. Never broadcast an agent transaction directly to the Hyperliquid bridge.

## Inspect, quote, prepare

```sh
oku wallet address --json
oku wallet balances --chain eip155:42161 --json
oku swap quote --chain 42161 --from USDC --to WETH --amount 10 --json
oku bridge quote --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10 --json
oku swap prepare --chain 42161 --from USDC --to WETH --amount 10 --json
oku send prepare --chain 42161 --token native --amount 0.01 --json
oku deposit address --asset usdc --source-chain eip155:42161 --json
oku deposit prepare --amount 10 --json
```

Resolve ambiguous token symbols with explicit contract addresses. Swap quote and prepare default to the enrolled agent wallet. Swap quotes use the backend `GetFlowstreamQuotes` long-poll, the same stream as the web app; allow up to about 8 seconds to collect routes. Bridge quote is informational; bridge execution is unavailable. Prepare writes a journal intent. Show the recipient, chain, router, minimum output, expiry and digest before submitting. A swap intent expires about 3 minutes after prepare, so obtain approval and submit promptly; if it expires, prepare anew. Keep native ETH on Arbitrum (or native gas on the selected chain) in the agent wallet for swaps and sends. Gas estimates include a buffer but cannot guarantee L2 data fees.

## Submit and reconcile

```sh
oku swap submit --intent ID --digest SHA --json
oku send submit --intent ID --digest SHA --json
oku deposit submit --intent ID --digest SHA --json
oku swap status --intent ID --json
oku send reconcile --intent ID --tx-hash TX_HASH --json
oku swap reconcile --intent ID --tx-hash TX_HASH --json
```

Submitting signs and broadcasts with Privy, so ask for explicit approval first. A swap requiring token approval sends **one atomic** Privy `wallet_sendCalls` batch `[approve exact amount to OkuRouter, swap]` via EIP-7702. The first batch upgrades the agent wallet to a Kernel smart account without changing its address; the approval cannot confirm without the swap. A single-step route (such as native ETH input) sends one normal transaction. Submit waits for the chain's confirmation depth: under a minute on fast L2s such as Arbitrum, several minutes on Ethereum, Polygon or BNB Chain; if it returns OUTCOME_UNKNOWN after broadcast, run status rather than resubmitting. Batch lookup requires a full RPC serving recent historical state: set `OKU_RPC_URL_<CHAINID>` (e.g. `OKU_RPC_URL_42161`) to a suitable endpoint; chains without a built-in RPC require it; the public default may return 403 or lack the needed state.

`INSUFFICIENT_GAS`, `INSUFFICIENT_BALANCE`, or `REJECTED` with **nothing broadcast** closes the intent: correct the problem and prepare a new one. On `OUTCOME_UNKNOWN`, **never retry submit or prepare a replacement**. Run `oku swap status --intent ID --json` (for a batch, this locates it by nonce and confirms its receipt), or `oku swap reconcile --intent ID --tx-hash TX_HASH --json` only with an independently recovered hash; use the corresponding `send` or `deposit` status for those intents. One unresolved intent blocks new onchain intents for the agent. MCP exposes submit and reconcile only with `--allow-mutations`. Privy wallet spending permissions are independent of Hyperliquid agent trading limits.
