---
name: consolidate-agent-funds
description: Fee sweep. Read the sweep CSV (chain_id,token_address,amount), check live balances, pick one intermediate asset per chain by end-to-end value at the destination, swap every token into it with --amount max, bridge it to the destination (circleStandard for native USDC, else the best route), claim, and verify. Runs chains concurrently (sell per chain, bridge as soon as ready, one claim loop) and submits automatically under the user's instruction and pauses only on guardrails.
---

# Consolidate Oku Agent Funds

Use this for a fee sweep: protocol fees land in the agent wallet on several chains, and the agent turns them into one asset on one destination chain. Read `oku://skills/move-agent-funds` first for command details; this skill overrides only its per-submit approval rule. Confirm flags with `oku docs commands --json`. Use token contract addresses everywhere; `native` means the gas token. Symbols are not used.

## Approval model

The user's instruction to run the consolidation (with the CSV, the destination chain and asset) **is the approval** for every swap, bridge and claim in this procedure. Do not ask per swap or per bridge. Restate the plan once before starting (chains, chosen asset per chain, destination, thresholds), then run it.

**Pause and ask the user only when a guardrail trips:**

| Guardrail | Default |
| --- | --- |
| Swap `valuation.high_impact: true` (prepare refused with `high impact`: priced loss `loss_bps`, excluding the Oku fee, above `--max-loss-bps`, default 300) | always, unless the chain is on the user's pre-approved mispricing list |
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

**High impact (mispricing) guardrail.** Small chains often have thin pools or a stale oracle, so the priced loss can look huge (bob, goat, nibiru, gensyn, sei and similar). By default, `swap prepare` refuses when `loss_bps` exceeds `--max-loss-bps` (default 300 = 3%). `swap quote` reports `loss_bps`, `high_impact` and `no_simulation`. When it trips, pause that chain and show the user the route, `in_amount` and `out_amount`, `in_usd`, `out_usd`, `oku_fee_usd` and `loss_bps`. Proceed only if they approve, by re-running prepare with `--allow-high-impact` (or `--max-loss-bps N` at the level they accept). If the user's instruction includes a **pre-approved mispricing list** of chains (for example "small chains ok: 60808, 2345, 6900, 685689, 1329"), use `--allow-high-impact` on those chains without pausing, but still report each loss. Never use it on major chains (Ethereum, Arbitrum, Base, Optimism, Polygon, BNB, Avalanche) without asking. `no_simulation: true` means the market returned no simulation, so min-out is derived from the quoted output and slippage. The swap calldata still enforces it on chain, and the OkuRouter target and calldata checks are unchanged.

**Check guardrails on the prepare output (`valuation`), not on `swap quote`.** `swap quote` is unauthenticated, so the backend prices it as an anonymous account. Trades under $25 then pay a flat $0.25 Oku fee (taken from the input), which makes small quotes look about $0.25 worse. `prepare` is authenticated and uses the agent's real fee tier (free trades, a volume tier or the same flat fee), so its `valuation.oku_fee_usd` is what actually applies. Raw `price_impact` includes the Oku fee; use `market_impact` for the market-only impact.

Other hard rules: one in-flight intent **per chain** (the journal gate is per agent and chain, so different chains run in parallel while each chain stays serial; an `OUTCOME_UNKNOWN` blocks only its chain until resolved); never prepare a replacement for an unresolved intent; recipient stays the agent wallet (do not pass `--recipient`) unless the user named a destination address; intents expire about 3 minutes after prepare, so submit right after prepare and requote if expired.

## 1. Read the CSV and live balances

```sh
oku wallet address --json
oku wallet balances --chains 8453,10,137 --tokens-file sweep.csv --json
```

The CSV is `chain_id,token_address,amount` (optional header). Its `amount` is informational only (`csv_amount`, `mismatch`); never spend it. Per chain, keep tokens with nonzero live `balance`. Skip as **dust** any token whose swap `gas_usd + oku_fee_usd` is at least its `in_usd` (assume a $0.25 flat fee on sells under $25 unless prepare shows less). Report zero and dust rows as skipped; do not sell them.

## 2. Choose one intermediate asset per chain

Candidates on each source chain:
Get each chain's candidates with `oku wallet balances --chains CHAINS --stables --json`. It adds the chain's **configured stablecoins** from chain-config (rows with `stable: true`, for example USDC.e `0x1f3AA82227281cA364bFb3d253B0f1af1Da6473E` on 0G) with live balance, symbol and decimals.

- **Native USDC** (Circle-issued, CCTP). Ethereum `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48`, Arbitrum `0xaf88d065e77c8cC2239327C5EDb3A432268e5831`, Base `0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913`, Optimism `0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85`, Polygon `0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359`, Avalanche `0xB97EF9Ef8734C71904D8002F8b6Bc66Dd9c48a6E`. Confirm others from Circle's list.
- **Every configured stable** on that chain (`stable: true` from `--stables`), not only native USDC. On chains without native USDC this is usually where the best destination value comes from (0G USDC.e, bridged USDT).
- **`native`**.

For each candidate C (read-only quotes):

```sh
# each CSV token T on the chain (skip when T is C)
oku swap quote --chain CHAIN --from T --to C --amount max --json
# sum out_amount over tokens, plus any existing balance of C (for native C, only what the swaps add; never the gas reserve)
oku bridge quote --chain CHAIN --destination-chain DEST --from C --to DEST_ASSET --amount SUM --json
oku bridge quote --chain CHAIN --destination-chain DEST --from C --to DEST_ASSET --amount SUM --route circleStandard --json   # when C is native USDC
```

Quote `in_amount`, `out_amount` and `min_out` are **raw base units**; `--amount` takes a decimal. Convert SUM with C's decimals (from `wallet balances`, or 18 for native) before the bridge quote. Quote-based scores include the anonymous flat fee per swap, which is the same for every candidate, so the ranking is still fair. Score(C) = bridge `out_amount` in the destination asset (for native USDC, use the circleStandard quote; it has no bridge fee). Pick the highest score. If the best score is within 0.3% of native USDC's score, **choose native USDC** (CCTP burn and mint has no slippage). Record the scores and the reason for each chain.

Gas check: the native balance left after the plan must cover the remaining swaps (about `gas_usd` per swap), the bridge, and a reserve. Never choose `native` as C if it requires selling gas below that. Before any Circle bridge, the agent needs native gas on the **destination** for the claim; if it has none, pause and ask the user to fund it (`oku wallet fund --chain DEST --token native`).

## 3. Run the chains concurrently

Steps 1 and 2 give a plan per source chain: its tokens and its asset C. Then run these phases. **A guardrail pause on one chain pauses only that chain.** Report it and keep the others going.

### Phase A: sell, one worker per chain (parallel across chains)

Start one worker per source chain at the same time (separate shells or sub-agents; the journal is safe for concurrent processes). Inside a chain, sell **one token per intent, sequentially**. Never combine several tokens into one batch.

```sh
oku swap prepare --chain CHAIN --from T --to C --amount max --json
oku swap submit --intent ID --digest SHA --json
oku swap status --intent ID --json        # only if submit was ambiguous
```

Prepare shows the resolved `amount` with `amount_source: max` (read by RPC and bound into the digest) and the `valuation` block. Check the guardrails on it, then submit immediately. Skip and report any token that gets `REJECTED: no eligible route`, or a fee-on-transfer/rebasing token that fails min-out once. On chains without the Privy 7702 batch, submit runs approve then swap as two transactions (`execution_mode: sequential`). If it stops between them, `status` shows `next_step` and resubmitting the same intent resumes the swap.

### Phase B: bridge as soon as the chain is ready (parallel)

The moment a chain's last sell settles, that chain's worker bridges C to the destination. Don't wait for other chains.

- **C is native USDC and Circle supports the pair:** `--route circleStandard` only. **Never `circleFast`**.
- **Otherwise, or if circleStandard does not quote:** the best non-intent route (`--no-intent`). If only an intent route quotes, pause that chain and ask.

```sh
oku bridge prepare --chain CHAIN --destination-chain DEST --from C --to DEST_ASSET --amount max --route circleStandard --json
oku bridge submit --intent ID --digest SHA --json
```

Once the source transaction confirms, the bridge no longer holds any gate, so the chain's worker is done.

### Phase C: one loop for all bridges in flight

Before the first claim, make sure the agent holds native gas on DEST. If it has none, pause and ask the user to fund it (`oku wallet fund --chain DEST --token native`). Then, about every 60 seconds:

```sh
oku bridge status --all --json     # every unsettled bridge: chains, amount, age, destination status, claim_required
oku bridge claim --ready --json    # claims every bridge reporting claim_required; idempotent, safe to rerun
```

`claim --ready` claims one at a time per destination chain and never double-claims. It reports a result per intent and keeps going past failures. Report `refunded`, `failed` or `expired` bridges and never re-bridge them. A claim that returns `OUTCOME_UNKNOWN` gates only the destination chain: run `oku bridge claim --intent ID` later to confirm it (it never resends). Circle Standard normally takes 15-20 minutes. Stop the loop when `status --all` is empty or only has bridges paused for the user.

### Phase D: verify

Run step 4 once every bridge is filled.

## 4. Verify and report

```sh
oku wallet balances --chains DEST --tokens-file dest.csv --json   # dest.csv lists DEST,DEST_ASSET
```

Compare the destination balance increase with the sum of bridge `min_out`. Report a table per chain: tokens sold (amount, out), skipped tokens and why, chosen asset and score reasoning, bridge route, intent IDs and tx hashes, received amount. List any open claims, guardrail pauses or unknown outcomes. Leftover gas on source chains is expected.
