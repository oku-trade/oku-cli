---
name: provide-uniswap-liquidity
description: Open, add to, remove from, collect fees of and close Uniswap v3 and v4 liquidity positions from the Oku Agent EVM wallet. Use for LP positions, pool prices, position ranges and uncollected fees.
---

# Provide Uniswap Liquidity

Use `oku docs commands --json` to confirm the installed commands and flags. Set `OKU_RPC_URL_<CHAINID>` to a trusted RPC on each chain used: every pool and position read goes to that RPC. Positions belong to the enrolled agent wallet. v3 is available on the chains the Oku web app supports for liquidity; v4 is available where Oku has a v4 PositionManager deployment (Ethereum and Unichain at the time of writing). Pass `--version v4` for v4; the default is v3.

## Inspect

```sh
oku lp pool --chain 1 --token0 WETH --token1 USDC --fee 3000 --json
oku lp pool --chain 1 --version v4 --token0 native --token1 USDC --fee 500 --json
oku lp list --chain 1 --json
oku lp list --chain 1 --version v4 --json
oku lp position --chain 1 --token-id 123456 --json
```

Prices are always token1 per token0, where token0 is the pool's lower token address; the output names the unit. `fee` is in hundredths of a bip: 100 (0.01%), 500 (0.05%), 3000 (0.3%), 10000 (1%). v4 pools are also keyed by tick spacing and hooks; the defaults are the standard tick spacing for the fee tier and no hooks.

`lp list` enumerates v3 positions on chain (up to 50 per call). v4 position NFTs cannot be enumerated on chain, so v4 discovery uses the Oku portfolio API and needs a logged-in profile. If it is unavailable, the output says so under `warnings`; pass `--token-id` with known v4 IDs. A field that could not be read is `N/A`, never zero.

## Prepare

```sh
oku lp open prepare --chain 1 --token0 WETH --token1 USDC --fee 3000 --range-percent 5 --amount 0.5 --amount-token WETH --json
oku lp open prepare --chain 1 --token0 WETH --token1 USDC --fee 3000 --price-lower 3000 --price-upper 4000 --amount 1000 --amount-token USDC --json
oku lp increase prepare --chain 1 --token-id 123456 --amount max --amount-token token0 --json
oku lp decrease prepare --chain 1 --token-id 123456 --percent 25 --json
oku lp collect prepare --chain 1 --token-id 123456 --json
oku lp close prepare --chain 1 --token-id 123456 --json
```

Give exactly one range: `--range-percent`, `--price-lower`/`--price-upper`, `--tick-lower`/`--tick-upper`, or `--full-range`. Ranges snap to the pool's tick spacing. `--amount` is in `--amount-token`; the other token's amount is derived from the current price and range. A range entirely above or below the price takes only one token. `max`/`all` uses the full on-chain balance, keeping a gas reserve for native.

On v3, `native` resolves to the wrapped native token and is paid as native value; leftover native is refunded in the same transaction. On v4, native pools use the zero address directly.

Prepare reads the pool, computes slippage limits like the Oku web app (default 50 bps, at most 500), adds only the approvals the chain shows are missing (exact amounts to the v3 manager; for v4, ERC-20 to Permit2 and a 30 day Permit2 allowance to the PositionManager), simulates the action when no approval precedes it, and journals the intent. It never signs. Show the user the preview: pair, fee tier, range and whether it is in range, expected amounts, the minimums or maximums, native value, number of calls and the digest.

`decrease` removes a percent of liquidity and collects those tokens plus all uncollected fees. `close` removes everything, collects fees and burns the position NFT. `collect` claims fees only and refuses a position with none.

## Submit and reconcile

```sh
oku lp submit --intent ID --digest SHA --json
oku lp status --intent ID --json
oku lp reconcile --intent ID --tx-hash TX_HASH --json
```

Submitting signs and broadcasts with Privy. The user's concrete instruction (action, position or pair, amount, range) may authorize a matching prepared intent after preview review; confirm guesses and any difference in range, amounts or slippage. Submit re-verifies every stored call against the plan before signing: only approvals for the plan's own tokens and spenders, then exactly the planned position-manager call. On chains supporting EIP-7702 the calls go out as one atomic batch. Otherwise each call is sent in order and `status` reports a safe resume point; submit the same intent and digest again to continue from it. The plan deadline is 20 minutes after prepare; after that, prepare anew.

Price moves beyond the slippage limits make the action revert on chain rather than lose value. `INSUFFICIENT_GAS`, `INSUFFICIENT_BALANCE`, or `REJECTED` with nothing broadcast closes the intent: fix the cause and prepare a new one. On `OUTCOME_UNKNOWN`, never retry submit or prepare a replacement. Run `oku lp status --intent ID --json`, or `oku lp reconcile` only with an independently recovered hash. An unresolved intent blocks new onchain intents for the agent on that chain only. MCP exposes `lp submit` and `lp reconcile` only with `--allow-mutations`.

## Permissions and consequences

Reads only query the chain RPC (and, for v4 `lp list`, the Oku portfolio API). Prepare writes a local journal intent and never signs. Submit moves agent funds into or out of Uniswap positions and may set token approvals for the position manager or Permit2. Liquidity in a range earns fees only while the price is inside it, and its token mix changes as the price moves.
