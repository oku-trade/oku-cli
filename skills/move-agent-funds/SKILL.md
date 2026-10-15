---
name: move-agent-funds
description: Move funds in the Oku Agent embedded EVM wallet with explicit journaled send, swap, bridge and master-bound deposit steps.
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
oku bridge prepare --chain 42161 --destination-chain 8453 --from USDC --to USDC --amount 10 --json
oku send prepare --chain 42161 --token 0x... --amount 10 --json
oku deposit address --asset usdc --source-chain eip155:42161 --json
oku deposit prepare --amount 10 --json
```

Resolve ambiguous token symbols with explicit contract addresses. Default destination is the agent wallet. Use an explicit `--recipient` only after checking the full EVM address and destination chain. Quotes are read-only but prepare writes a journal intent. Show the prepared recipient, chains, router, spender, minimum output, expiry and digest to the user before submitting. The agent needs enough gas on the source chain for approval and execution, and gas on the destination chain for subsequent transfers. Bridge settlement is not established by a successful source receipt alone.

## Submit and reconcile

```sh
oku swap submit --intent ID --digest SHA --json
oku bridge submit --intent ID --digest SHA --json
oku send submit --intent ID --digest SHA --json
oku deposit submit --intent ID --digest SHA --json
oku swap status --intent ID --json
oku bridge status --intent ID --json
```

Submitting signs and broadcasts with Privy, so ask for explicit approval first. A failed or timed-out broadcast may have succeeded. If the outcome is unknown, **never retry submit or create a replacement transfer automatically**. Inspect the journal and receipts with status and wait for reconciliation. Never treat a source bridge receipt as proof of destination delivery. MCP exposes submit only with `--allow-mutations`. Privy wallet spending permissions are independent of Hyperliquid agent trading limits.
