---
name: consolidate-agent-funds
description: Fee sweep. Read the sweep CSV (chain_id,token_address,amount), check live balances, and end with native USDC on Coinbase-depositable chains. On a Coinbase chain, swap every token into that chain's native USDC and stop. Elsewhere, swap into the best intermediate and bridge to native USDC on Base or Arbitrum (circleStandard when possible), claim, and report the USDC ready for Coinbase deposit per chain. Runs chains concurrently, submits automatically under the user's instruction and pauses only on guardrails.
---

# Consolidate Oku Agent Funds

Use this for a fee sweep: protocol fees land in the agent wallet on several chains, and the agent turns them into **native USDC on chains Coinbase accepts as a direct deposit**, so the user can deposit straight to Coinbase. There is no single destination chain. Read `oku://skills/move-agent-funds` first for command details; this skill overrides only its per-submit approval rule. Confirm flags with `oku docs commands --json`. The default API is production (`https://accounts.icarus.tools`). Run the sweep against production unless the user names a staging profile (created with `--api-url https://accounts.lat.gfx.town`). Use token contract addresses everywhere; `native` means the gas token. The zero address `0x0000000000000000000000000000000000000000` and `0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE` mean native too. The sweep CSV exports native fee rows as the zero address, and you can pass them to `--from`, `--to` and `--token` as-is. Symbols are not used.

## Approval model

The user's instruction to run the consolidation (with the CSV) **is the approval** for every swap, bridge and claim in this procedure. Do not ask per swap or per bridge. Restate the plan once before starting (per source chain: stay or bridge, the intermediate asset, the Coinbase destination chain, and thresholds), then run it.

**Pause and ask the user only when a guardrail trips:**

| Guardrail | Default |
| --- | --- |
| Swap `valuation.high_impact: true` (prepare refused with `high impact`: priced loss `loss_bps`, excluding the Oku fee, above `--max-loss-bps`, default 500) | always, unless the chain is on the pre-approved mispricing list (default: sei 1329) |
| Swap `valuation.market_impact` (prepare output; impact excluding the Oku fee) worse than | 2% (`< -0.02`) |
| Bridge `out_usd` below `in_usd` by more than (stables) | 1% (non-stable assets: 3%) |
| Slippage above the CLI default | any `--slippage-bps` above 50 |
| Bridge route differs from the planned route | always |
| Intent-based route (`intent_based: true`) | always (use `--no-intent`) |
| Any submit or claim error | always |
| `OUTCOME_UNKNOWN` | always; **never retry**, run status |
| Bridge `refunded`, `failed` or `expired` | always; never re-bridge |
| Plan would sell native gas below the reserve | always |

The user may override thresholds in the instruction. Quote USD values (`in_usd`, `out_usd`, `gas_usd`, `fees_usd`, `price_impact`, `oku_fee_usd`, `market_impact`) come from the backend price oracle and are for guardrails only; amounts executed always come from chain reads.

**High impact (mispricing) guardrail.** Small chains often have thin pools or a stale oracle, so the priced loss can look huge (bob, goat, nibiru, gensyn and similar). By default, `swap prepare` refuses when `loss_bps` exceeds `--max-loss-bps` (default 500 = 5%). Only losses count: a quote priced *above* its input (negative `loss_bps`) is fine and never pauses. `swap quote` reports `loss_bps`, `high_impact` and `no_simulation`. When the guardrail trips, pause that chain and show the user the route, `in_amount` and `out_amount`, `in_usd`, `out_usd`, `oku_fee_usd` and `loss_bps`. Proceed only if they approve, by re-running prepare with `--allow-high-impact` (or `--max-loss-bps N` at the level they accept).

**Pre-approved mispricing list.** On the chains below, use `--allow-high-impact` without pausing, but still report each loss. The user's instruction may add chains (for example "small chains ok: 60808, 2345, 6900, 685689") or remove them.

| Chain | ID | Why |
| --- | --- | --- |
| sei | 1329 | The backend oracle is unreliable on sei. It disagrees with every routed pool by large factors in both directions. Trust the route responses: the routes the backend returns are the only ones that can execute. |

Pre-approval only bypasses the oracle-based loss guardrail. It does not touch min-out or slippage: the quoted min-out, the 50 bps default slippage and the on-chain min-out check all still apply. Rank candidates by **amounts** (destination `out_amount`), never by oracle USD. A guardrail flag means the oracle and the pool disagree, not that the pool is worse. Never pre-approve major chains (Ethereum, Arbitrum, Base, Optimism, Polygon, BNB, Avalanche) without asking. `no_simulation: true` means the market returned no simulation, so min-out is derived from the quoted output and slippage. The swap calldata still enforces it on chain, and the OkuRouter target and calldata checks are unchanged.

**Check guardrails on the prepare output (`valuation`), not on `swap quote`.** `swap quote` is unauthenticated, so the backend prices it as an anonymous account. Trades under $25 then pay a flat $0.25 Oku fee (taken from the input), which makes small quotes look about $0.25 worse. `prepare` is authenticated and uses the agent's real fee tier (free trades, a volume tier or the same flat fee), so its `valuation.oku_fee_usd` is what actually applies. Raw `price_impact` includes the Oku fee; use `market_impact` for the market-only impact.

Other hard rules: one in-flight intent **per chain** (the journal gate is per agent and chain, so different chains run in parallel while each chain stays serial; an `OUTCOME_UNKNOWN` blocks only its chain until resolved); never prepare a replacement for an unresolved intent; recipient stays the agent wallet (do not pass `--recipient`) unless the user named a destination address; intents expire about 3 minutes after prepare, so submit right after prepare and requote if expired.

## Coinbase-depositable chains (edit here)

The sweep ends only on **native USDC** (Circle-issued) on one of these chains. **Bridged USDC (USDC.e, USDbC, Polygon `0x2791...`, Arbitrum `0xFF97...`) is NOT accepted by Coinbase: never end on it.** Coinbase deposit addresses are network-specific, so the user must pick the matching network in Coinbase.

| Chain | ID | Native USDC | Preferred destination |
| --- | --- | --- | --- |
| Base | 8453 | `0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913` | yes (cheap, Circle CCTP) |
| Arbitrum | 42161 | `0xaf88d065e77c8cC2239327C5EDb3A432268e5831` | yes (cheap, Circle CCTP) |
| Optimism | 10 | `0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85` | stay-only |
| Polygon | 137 | `0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359` | stay-only |
| Ethereum | 1 | `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48` | only if it is the sole reachable option or clearly better net of gas |

Sources, checked 2026-10-22:
- Getty (Discord) named Ethereum, Optimism, Base, Arbitrum and Polygon.
- Avalanche C-Chain and Unichain are treated as NOT depositable (abs3nt, 2026-10-22): the only sources were Coinbase's International Exchange help page (Avalanche) and a Coinbase Assets post (Unichain), not retail Coinbase deposits, so they bridge to Base or Arbitrum like any other non-Coinbase chain. Add them back here only once Getty confirms them on his retail account.
- World Chain, Celo, Linea and Sonic have native USDC but no Coinbase deposit source was found, so they are excluded.

The addresses come from Circle's native USDC list (developers.circle.com/stablecoins/usdc-contract-addresses, checked 2026-10-22) and match chain-config's stables. Do **not** use chain-config `Token.UsdcAddress`: on Arbitrum and Polygon it is bridged USDC. Edit this table to add or remove chains.

## 1. Read the CSV and live balances

```sh
oku wallet address --json
oku wallet balances --tokens-file sweep.csv --json        # reads every chain in the CSV; add --chains to restrict
```

The CSV is `chain_id,token_address,amount` (optional header). Its `amount` is informational only (`csv_amount`, `mismatch`); never spend it. Per chain, keep tokens with nonzero live `balance`. Skip as **dust** any token whose swap `gas_usd + oku_fee_usd` is at least its `in_usd` (assume a $0.25 flat fee on sells under $25 unless prepare shows less). Report zero and dust rows as skipped; do not sell them.

For a successful query covering the requested chains, a requested ERC-20 omitted from `tokens` has zero balance. Read requested native-token rows from the `native` map, not `tokens`. Errors and unread chains are not zero balances: a chain listed under `errors` in the balances output was not read, so report it as unread (set `OKU_RPC_URL_<CHAINID>` for `NO_RPC`) instead of treating its assets as zero. Ignore incidental gas balances unless `native` is itself a CSV asset. If all CSV assets are zero or were established as dust by quote costs (the dust rule above), report the skipped rows and stop without preparing swaps or entering the execution phases: nothing was consolidated.

## 2. Plan each source chain

**A. The source chain is in the Coinbase table:** swap every CSV token on it into that chain's native USDC with `--amount max`, then stop. Don't bridge. A token that already is that native USDC needs nothing. Bridged USDC on the chain (USDC.e) is just another token to swap into native USDC.

**B. The source chain is not in the table:** pick an intermediate asset C and a Coinbase destination D, then bridge.

Candidates for C, from `oku wallet balances --chains CHAIN --stables --json` (rows with `stable: true`) plus:
- native USDC if the chain has it (CCTP),
- every configured stable (for example 0G USDC.e, bridged USDT),
- `native`.

Destinations D: **Base and Arbitrum native USDC first.** Use Ethereum native USDC only if neither Base nor Arbitrum is reachable, or if Ethereum is clearly better net of its higher claim gas. Never use USDC.e.

For each C and D (read-only quotes):

```sh
# each CSV token T on the chain (skip when T is C)
oku swap quote --chain CHAIN --from T --to C --amount max --json
# sum out_amount over tokens, plus any existing balance of C (for native C, only what the swaps add; never the gas reserve)
oku bridge quote --chain CHAIN --destination-chain D --from C --to USDC_D --amount SUM --route circleStandard --json   # when C is native USDC and the chain supports CCTP
oku bridge quote --chain CHAIN --destination-chain D --from C --to USDC_D --amount SUM --no-intent --json             # otherwise
```

Quote `in_amount`, `out_amount` and `min_out` are **raw base units**, while `--amount` takes a decimal. Convert SUM with C's decimals (from `wallet balances`, or 18 for native). The anonymous flat fee per swap is the same for every candidate, so quote-based ranking is still fair.

**Score(C, D) = native USDC received on D** (bridge `out_amount`, minus the claim gas on D for Circle routes). Pick the highest score. If Base and Arbitrum are within 0.3%, prefer the one that already holds more of the sweep (fewer chains for the user to deposit from), else Base. If native USDC on the source scores within 0.3% of the best, choose it (CCTP burn and mint has no slippage). If no direct bridge to D quotes, try two hops: C to native USDC on Ethereum, then Ethereum to D via circleStandard. Score the full path. Record each chain's scores and the reason.

**0G (16661) plan.** No provider bridges 0G USDC.e (`0x1f3AA82227281cA364bFb3d253B0f1af1Da6473E`) directly to Base or Arbitrum USDC today. Recheck, and use a direct route if one appears. Otherwise:
1. USDC.e to Ethereum USDC via `lifi`, then Ethereum USDC to Base or Arbitrum USDC via `--route circleStandard`. This needs ETH on Ethereum for the burn and gas on the destination for the claim. If Ethereum gas makes the second hop uneconomic, stop on Ethereum USDC: it is Coinbase-depositable.
2. Or USDC.e to native ETH on Base or Arbitrum via `lifi`, then swap ETH to native USDC there (a Coinbase chain, so case A applies).

**Gas check (covers every chain the plan touches):**
- Each source chain needs enough native gas for its swaps (about `gas_usd` each), the bridge and a reserve. Never choose `native` as C if that would sell gas below the reserve.
- Circle routes are claimed on the destination, so **every destination chain the plan bridges to** (Base, Arbitrum, and Ethereum for a 0G two-hop) needs native gas for one claim per incoming bridge.
- An Ethereum first hop also needs ETH on Ethereum for the burn.
- Check all of these with `oku wallet balances --chains 8453,42161[,1] --json` before Phase B. Pause and ask the user to fund any that are short (`oku wallet fund --chain D --token native`).

## 3. Run the chains concurrently

Steps 1 and 2 give a plan per source chain: its tokens, its asset C, and either "stay" (case A) or a Coinbase destination D (case B). Then run these phases. **A guardrail pause on one chain pauses only that chain.** Report it and keep the others going.

### Phase A: sell, one worker per chain (parallel across chains)

Start one worker per source chain at the same time (separate shells or sub-agents; the journal is safe for concurrent processes). Inside a chain, sell **one token per intent, sequentially**. Never combine several tokens into one batch.

```sh
oku swap prepare --chain CHAIN --from T --to C --amount max --json
oku swap submit --intent ID --digest SHA --json
oku swap status --intent ID --json        # only if submit was ambiguous
```

Prepare shows the resolved `amount` with `amount_source: max` (read by RPC and bound into the digest) and the `valuation` block. Check the guardrails on it, then submit immediately. If a token gets `REJECTED: no eligible route` with `retryable: true` (the CLI already re-quoted once), wait 30-60s, re-quote and prepare once more before skipping it. With `retryable: false`, skip it. Report skipped tokens with the per-route reasons from `error.details.routes`. Also skip and report a fee-on-transfer/rebasing token that fails min-out once. On chains without the Privy 7702 batch, submit runs approve then swap as two transactions (`execution_mode: sequential`). If it stops between them, `status` shows `next_step` and resubmitting the same intent resumes the swap.

### Phase B: bridge as soon as the chain is ready (parallel)

Case A chains are done after Phase A. For case B, the moment a chain's last sell settles, that chain's worker bridges C to its destination D (native USDC on Base or Arbitrum, or Ethereum). Don't wait for other chains.

- **C is native USDC and Circle supports the pair:** `--route circleStandard` only. **Never `circleFast`**.
- **Otherwise, or if circleStandard does not quote:** the best non-intent route (`--no-intent`). If only an intent route quotes, pause that chain and ask.

```sh
oku bridge prepare --chain CHAIN --destination-chain D --from C --to USDC_D --amount max --route circleStandard --json
oku bridge submit --intent ID --digest SHA --json
```

Once the source transaction confirms, the bridge no longer holds any gate, so the chain's worker is done.

### Phase C: one loop for all bridges in flight

Before the first claim, make sure the agent holds native gas on **every** destination chain in the plan (the step 2 gas check). If any is short, pause only the bridges to that chain and ask the user to fund it (`oku wallet fund --chain D --token native`). Then, about every 60 seconds:

```sh
oku bridge status --all --json     # every unsettled bridge: chains, amount, age, destination status, claim_required
oku bridge claim --ready --json    # claims every bridge reporting claim_required; idempotent, safe to rerun
```

`claim --ready` claims on all destination chains (Base, Arbitrum and others), one at a time per chain, and never double-claims. It reports a result per intent and keeps going past failures. Report `refunded`, `failed` or `expired` bridges and never re-bridge them. A claim that returns `OUTCOME_UNKNOWN` gates only the destination chain: run `oku bridge claim --intent ID` later to confirm it (it never resends). Circle Standard normally takes 15-20 minutes. Stop the loop when `status --all` is empty or only has bridges paused for the user.

### Phase D: verify

Run step 4 once every bridge is filled.

## 4. Verify and report USDC ready for Coinbase

```sh
oku wallet balances --chains 8453,42161,10,137,1 --stables --json   # restrict to the Coinbase chains the plan used
```

Report **per Coinbase chain**: chain name and ID, the native USDC contract, the agent address holding it, and the native USDC `balance`. This is what the user deposits to Coinbase, on the matching network. Flag any USDC.e or other bridged USDC left over as **not depositable**. Compare each destination's increase with the sum of bridge `min_out` into it. Also include a per-source-chain table: tokens sold (amount, out), skipped tokens and why, stay or bridge, the chosen C and D with the score reasoning, bridge route, intent IDs and tx hashes, and amount received. List any open claims, guardrail pauses or unknown outcomes. Leftover gas on source chains is expected.

**Depositing to Coinbase is out of scope** for this skill; the user deposits. If they ask to send from the agent, `oku send prepare --chain D --token USDC_D --to COINBASE_DEPOSIT_ADDRESS --amount max --json` then `oku send submit` does it. Coinbase deposit addresses are specific to each network, so get the user's address for that exact network and confirm it with them first. That send is outside the sweep approval.
