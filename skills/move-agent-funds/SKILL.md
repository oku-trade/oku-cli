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
oku send prepare --chain 42161 --token 0x... --amount 10 --json
oku deposit address --asset usdc --source-chain eip155:42161 --json
oku deposit prepare --amount 10 --json
```

Resolve ambiguous token symbols with explicit contract addresses. Swap quote and prepare default to the enrolled agent wallet. Bridge quote is informational; bridge execution is unavailable. Prepare writes a journal intent. Show the recipient, chain, router, minimum output, expiry and digest before submitting. Gas estimates include a buffer but cannot guarantee L2 data fees.

## Submit and reconcile

```sh
oku swap submit --intent ID --digest SHA --json
oku send submit --intent ID --digest SHA --json
oku deposit submit --intent ID --digest SHA --json
oku swap status --intent ID --json
oku send reconcile --intent ID --tx-hash 0x... --json
oku swap reconcile --intent ID --tx-hash 0x... --json
```

Submitting signs and broadcasts with Privy, so ask for explicit approval first. If the outcome is unknown, **never retry submit**. Reconcile a recovered hash only after verifying its source and exact transaction against the journal. MCP exposes submit and reconcile only with `--allow-mutations`. Privy wallet spending permissions are independent of Hyperliquid agent trading limits.
