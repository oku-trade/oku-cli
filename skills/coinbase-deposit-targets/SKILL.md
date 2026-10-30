---
name: coinbase-deposit-targets
description: Use only when the user wants to end on, or send to, a Coinbase deposit. Identify native USDC target chains and verify the selected Coinbase deposit network before any send.
---

# Coinbase deposit targets

Use this table as the acceptable target set for `oku://skills/consolidate-agent-funds` only when the user asks for Coinbase-depositable assets. These are **Circle-issued native USDC** contracts, not merely tokens with the symbol USDC. The preference notes guide route planning, not a mandate to bridge funds already on an acceptable chain. Circle publishes the contract addresses at https://developers.circle.com/stablecoins/usdc-contract-addresses. Coinbase requires the receiving network to match the network selected on the deposit screen: https://help.coinbase.com/en/coinbase/trading-and-funding/cryptocurrency-trading-pairs/steps-to-receive-crypto.

| Chain | ID | Native USDC contract | Preference |
| --- | --- | --- | --- |
| Base | 8453 | `0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913` | Preferred destination (low gas, Circle CCTP) |
| Arbitrum | 42161 | `0xaf88d065e77c8cC2239327C5EDb3A432268e5831` | Preferred destination (low gas, Circle CCTP) |
| Optimism | 10 | `0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85` | Stay if already there |
| Polygon | 137 | `0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359` | Stay if already there |
| Ethereum | 1 | `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48` | Only if sole reachable target or clearly better after gas |

Do **not** substitute bridged USDC (USDC.e, USDbC, Polygon `0x2791...`, Arbitrum `0xFF97...`) for these contracts. The chain-config `Token.UsdcAddress` on Arbitrum and Polygon identifies bridged USDC, not the native contracts above. When planning a Coinbase destination, convert bridged representations into the listed native USDC first. Coinbase deposit addresses are network-specific. The user must select the network in Coinbase that matches the chain and confirm the displayed asset and address. Coinbase support changes: confirm the network on Coinbase's deposit screen before sending.

This table deliberately excludes chains with native USDC but no confirmed **retail Coinbase deposit support for this use case**: Avalanche C-Chain, Unichain, World Chain, Celo, Linea and Sonic. Circle's contract list establishes native issuance, not Coinbase deposit availability. Do not add one as a target until the user confirms support in their own Coinbase deposit screen. Coinbase's general receive instructions explain why network selection matters; they do not guarantee that every listed chain is available to every account. See also https://help.coinbase.com/en/coinbase/trading-and-funding/cryptocurrency-trading-pairs/steps-to-receive-crypto and https://help.coinbase.com/en-gb/coinbase/trading-and-funding/sending-or-receiving-cryptocurrency/assets-on-multiple-networks.

**Default endpoint is the agent wallet on a listed chain.** Never send to a Coinbase deposit address unless the user supplies that exact address and explicitly asks for the send. A send is separate from consolidation authorization; verify that the address and selected Coinbase network match the target chain before preparing it.
