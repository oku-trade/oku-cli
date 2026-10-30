---
name: consolidate-agent-funds
description: Consolidate agent wallet balances from an optional CSV, another holdings list or live wallet discovery into user-chosen chain and token targets. Plan and execute each chain independently, enforce guardrails, bridge and claim, then verify.
---

# Consolidate agent wallet balances

Use this for a bounded consolidation of agent wallet assets across chains into a **user-chosen set of acceptable (chain ID, token contract address) targets**, optionally ordered by preference. Sources may come from a CSV, another list or file, or wallet discovery; never require a CSV. Ask for the target set if absent. If the user asks to end on Coinbase-depositable assets, read `oku://skills/coinbase-deposit-targets` and use its table as the target set.

Read `oku://skills/move-agent-funds` for command and reconciliation details; this procedure supplies standing authorization for its bounded execution. Confirm installed flags with `oku docs commands --json`, and inspect the active API profile with `oku profiles list`. Do not infer a staging or production profile from the manifest. Use explicit token addresses: `native`, the zero address `0x0000000000000000000000000000000000000000`, and `0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE` all denote the native gas token. The manifest may use the zero address for native rows. Symbols alone do not identify a token across chains.

## Authorization and guardrails

The user's instruction to consolidate **the identified assets into the specified target set** authorizes routine prepare, submit and claim within the plan and thresholds. When discovering holdings without a supplied asset list, show the discovered source set and ask about ambiguous inclusions before executing; do not silently expand the user's stated scope. Do not request approval for each transaction. Restate the sources, target set and preference, thresholds, gas reserves and any explicit exceptions once. Each chain worker posts a one-line plan (stay or bridge, intermediate C, destination D, score reason) when starting execution. This is status, not an approval wait. An inferred or changed target, additional source assets, recipient change, route change or action beyond the authorized scope requires a question. The recipient remains the agent wallet (omit `--recipient`) unless the user explicitly authorized another address.

| Guardrail | Default action |
| --- | --- |
| Swap prepare `valuation.high_impact: true`, or priced loss `loss_bps` above `--max-loss-bps` (default 500 bps = 5%, excluding Oku fee) | Stop and ask before any further prepare or submit on that chain |
| Swap prepare `valuation.market_impact` below `-0.02` (market-only impact worse than 2%) | Stop and ask |
| Bridge `out_usd` below `in_usd` by more than 1% for stable assets, 3% for non-stables | Stop and ask |
| `--slippage-bps` above the CLI default of 50 | Stop and ask |
| Bridge route differs from the planned route | Stop and ask |
| `intent_based: true` | Stop and ask; normally quote/prepare with `--no-intent` |
| Any submit or claim error | Stop that chain and investigate |
| `OUTCOME_UNKNOWN` | Stop that chain, run status, **never retry blindly** |
| Bridge `refunded`, `failed` or `expired` | Report and stop; never re-bridge automatically |
| Selling native gas below the reserve | Stop and ask |

A user can override a threshold or **explicitly pre-authorize a high-impact exception scoped by chain and maximum acceptable loss**, for example, "allow high impact on chain 1329 up to 1500 bps loss". No chain is pre-authorized by this skill. Only for that explicitly authorized scope, and only if the preliminary quote fits its loss ceiling, add `--allow-high-impact` to prepare. Check the authenticated prepare `valuation.loss_bps` against that ceiling **before submit** and stop if it exceeds it; report every permitted override and actual loss. If the scoped ceiling can be enforced with `--max-loss-bps N` instead, still check the prepare output against that ceiling. An unbounded phrase such as "small chains ok" is not a loss ceiling: ask for a limit. A separately tripped `market_impact` or other guardrail still requires approval. Never assume an oracle discrepancy is permission to bypass min-out, slippage, route validation or the on-chain checks.

Only losses count: a negative `loss_bps` (quote priced above input) is not a high-loss trigger. If high impact blocks prepare, show the route, `in_amount`, `out_amount`, `in_usd`, `out_usd`, `oku_fee_usd` and `loss_bps`, then wait for explicit approval before re-preparing with a bounded override. `no_simulation: true` means the market returned no simulation, so min-out derives from quoted output and slippage; swap calldata still enforces min-out and the OkuRouter target and calldata checks remain in force.

**Evaluate guardrails on prepare output `valuation`, not `swap quote`.** Quotes are unauthenticated and priced at an anonymous fee tier; under $25 that can mean a flat $0.25 fee taken from input. Prepare is authenticated and uses the agent's actual tier, possibly free or volume-priced. Its `valuation.oku_fee_usd` is the applicable fee. Raw `price_impact` includes the Oku fee, whereas `market_impact` excludes it. Oracle USD fields (`in_usd`, `out_usd`, `gas_usd`, `fees_usd`, `price_impact`, `oku_fee_usd`, `market_impact`) guide guardrails, not executable amounts. Rank comparable target output by **amount**, not oracle USD. Always compare the prepared chain, token, amount, recipient, route, min-out, fees and slippage against the authorized plan before submit.

There is **one in-flight intent per agent and chain**. Chains may run in parallel, but each stays serial. Never prepare a replacement for an unresolved intent; an `OUTCOME_UNKNOWN` blocks only its chain until resolved. Intents expire roughly three minutes after prepare, so submit the exact digest promptly or requote and reprepare if definitively expired. A confirmed bridge source releases its source-chain journal gate.

## 1. Identify holdings and read live balances

Any source of holdings information is enough. Accept a CSV with `chain_id,token_address,amount` (optional header), another file or pasted list such as JSON, table or chain/token list, or holdings fetched from a user-named portfolio API or explorer. If none is supplied, discover with `oku wallet balances --chains USER_CHAINS --stables --json`, or `oku wallet balances --all-chains --stables --json` when the user named no chains. Discovery must include non-stable ERC-20 holdings if they are in scope: use a user-specified portfolio API or explorer, or ask for a token list when the built-in balance read cannot enumerate them. Do not infer that an undiscovered token has zero balance. Normalize identified holdings to (chain ID, token address or `native`, optional informational amount) and state the source used in the plan. External amounts are **informational only** (`csv_amount` and `mismatch` for CSV); never spend them or use them to size trades.

```sh
oku wallet address --json
oku wallet balances --tokens-file manifest.csv --json       # only when an actual CSV was supplied
oku wallet balances --chains USER_CHAINS --stables --json   # specified chains, without CSV
oku wallet balances --all-chains --stables --json           # no specified source chains
oku wallet balances --chains TARGET_CHAIN_IDS --stables --json
```

`--tokens-file` reads every CSV chain unless `--chains` restricts it. For arbitrary non-CSV ERC-20s not among configured stables, verify live `balanceOf` against the agent address with a trusted chain RPC or read-only portfolio API that reports on-chain balances. `wallet balances` has no `--tokens` flag, so do not pretend that `--chains --stables` verifies other ERC-20s. Re-read spendable balances with `--amount max` on prepare. The target read captures target-chain balances and native gas before workers begin, even when a target chain is absent from the source input. For target tokens not returned by `--stables`, read their live ERC-20 balances the same way before a tie-break or final verification. A chain under `errors` is unknown, not zero; do not use its holding for a tie-break.

Keep identified assets with nonzero **live** balance. On a successful explicit CSV query, a requested ERC-20 omitted from `tokens` has zero balance; read native rows from `native`, not `tokens`. An omitted token from discovery is **not** proof of zero. `errors`, failed reads and unread chains do not prove zero. Report them as unread and configure `OKU_RPC_URL_<CHAINID>` for `NO_RPC`. Ignore incidental native gas unless native is itself an authorized source asset.

Skip **dust** when a token's sell `gas_usd + oku_fee_usd` is at least its `in_usd`; for sells under $25, provisionally assume a $0.25 flat fee unless authenticated prepare shows less. Report zero and dust rows as skipped. Do not sell them. If all identified assets are zero or dust by quote costs, report skips and stop without preparing or executing.

## 2. Plan inside each source-chain worker

Immediately after the shared reads, start **one worker per source chain** (separate shells or sub-agents; the journal supports concurrent processes). Each independently plans from its own assets, the shared snapshot and read-only quotes, then starts Phase A and Phase B as soon as **its own** plan is ready. Never wait for all chains to finish planning. A slow or paused chain must not hold up another chain.

**A. Source chain has an acceptable target token:** If the instruction prefers staying, swap each listed non-target token to that chain's chosen target with `--amount max`, then stop; already-target holdings need no trade. If several acceptable targets exist there, compare routes and preference first. A target-chain token that is not the chosen target is another token to swap. If the user explicitly prioritizes one destination or a cross-chain option is materially better after fees, compare staying and bridging within their scope rather than silently forcing a stay.

**B. Otherwise:** Choose an intermediate asset C on the source and an allowed destination pair D = (chain, token), then bridge. C candidates from `oku wallet balances --chains CHAIN --stables --json` (rows with `stable: true`) include every configured stable, any native USDC where available, and `native`. Quote each feasible C and D, respecting the requested target preference. Do not broaden the target set just because another bridge quotes.

```sh
# For each authorized source token T other than C:
oku swap quote --chain CHAIN --from T --to C --amount max --json
# Sum projected out_amount for sold T plus an in-scope existing C balance;
# if C is native, never count or sell the gas reserve.
oku bridge quote --chain CHAIN --destination-chain D_CHAIN --from C --to D_TOKEN --amount SUM --route circleStandard --json  # native USDC, eligible Circle pair only
oku bridge quote --chain CHAIN --destination-chain D_CHAIN --from C --to D_TOKEN --amount SUM --no-intent --json             # otherwise
```

`in_amount`, `out_amount` and `min_out` in quotes are raw base units; `--amount` expects decimal units. Convert SUM using C's decimals from balances (18 for native). The anonymous flat fee per sell is comparable across C candidates, but actual fees and guardrails come from prepare. Score comparable paths by **destination token out_amount**, subtracting destination claim gas only after converting that gas into target token units with an explicit price estimate. Use token units only for the same asset across chains; when targets differ, compare quoted USD net of gas as an estimate and explain uncertainty, never treat unlike token amounts as equivalent. For same-asset outcomes within 0.3%, use the user's preference order, then prefer the destination holding more of that exact target token in the shared snapshot (fewer destination chains), then the cheaper claim gas. If native USDC on the source is within 0.3% of the best same-asset outcome, choose it as C when Circle Standard is available (burn and mint has no slippage). Record scores and rationale. Unknown destination balances cannot win a balance tie-break.

If no direct bridge quotes, consider two hops through an intermediate chain only when the final pair remains in the target set. For example, bridge a stable to native USDC on Ethereum with a transaction route, then Circle Standard to an allowed destination; score the full path, account for Ethereum burn gas and both claims, and verify the intermediate hop rather than treating it as final. Alternatively bridge into native gas on the destination, then swap there to the authorized token, reserving gas on that chain. For 0G (16661), recheck direct routes for USDC.e `0x1f3AA82227281cA364bFb3d253B0f1af1Da6473E` first; if absent, quote a `lifi` hop to Ethereum native USDC and a Circle Standard hop onward, or `lifi` into destination native gas followed by a swap. Stop on the intermediate chain only if that exact pair is itself an acceptable target. Do not assume a historical route failure still holds.

**Gas checks:** Each source needs native gas for all sells, bridge and a reserve. For native input, size no higher than balance minus reserve, estimated transaction gas and an extra margin for L2 data fees. `--gas-reserve` defaults to 0.001 native units, or 0.01 on Ethereum; increase if the plan needs more. A first hop through Ethereum needs ETH for its burn. Every Circle destination needs native claim gas for one claim per incoming bridge, including already pending claims. Recheck the destination with `oku wallet balances --chains D_CHAIN --json` and `oku bridge status --all --json` immediately before Phase B, then again before claims. Fund short chains only at the user's direction (`oku wallet fund --chain D_CHAIN --token native` displays funding instructions). A short destination gates bridges and claims to it, not another chain's sells.

## 3. Execute each chain's pipeline

Post the one-line chain plan, then sell **one token per intent, sequentially**. Do not batch tokens.

### Phase A: sells

```sh
oku swap prepare --chain CHAIN --from T --to C --amount max --json
oku swap submit --intent ID --digest SHA --json
oku swap status --intent ID --json  # on ambiguity
```

Prepare binds the RPC-resolved `amount` (`amount_source: max`) to the digest. Check `valuation` and every guardrail, then submit promptly. For `REJECTED: no eligible route` with `retryable: true` (CLI already requoted once), wait 30-60 seconds, re-quote and prepare once more before skipping. If `retryable: false`, skip. Report per-route reasons from `error.details.routes`. Skip and report fee-on-transfer or rebasing tokens after one min-out failure. On chains without Privy 7702 batching, approval and swap may be sequential (`execution_mode: sequential`); if approval succeeds but swap does not, `status` gives `next_step`, and resubmitting the **same** intent resumes only the remaining step. An unknown outcome still requires status or reconcile first, never a blind resend.

### Phase B: bridges, as each chain becomes ready

A stay-chain finishes after Phase A. Otherwise bridge immediately after that chain's last sell settles. Circle-compatible native USDC pairs use `--route circleStandard`, **never `circleFast`**. If that does not quote, use the best planned transaction route with `--no-intent`; if only an intent-based route quotes, ask. Replace C and D_TOKEN below with explicit addresses or `native` as appropriate.

```sh
oku bridge prepare --chain CHAIN --destination-chain D_CHAIN --from C --to D_TOKEN --amount max --route circleStandard --json
oku bridge submit --intent ID --digest SHA --json
oku bridge status --intent ID --json  # on ambiguity
```

Use `--no-intent` **instead of** `--route circleStandard` for a planned non-Circle route, and pin its `--route ROUTE` if known. Compare prepared route and values with the plan. For non-Circle routes, the bridge USD loss guardrail applies. On a two-hop path, confirm the first destination delivery and gas, then run the next hop under that chain's one-in-flight-intent rule. Once the source transaction confirms, its worker is free; never mistake source confirmation for final delivery.

### Phase C: monitor and claim all bridges

Before claims, check native gas on every destination in use and pause only destinations short of gas. About every 60 seconds:

```sh
oku bridge status --all --json
oku bridge claim --ready --json
```

`claim --ready` attempts every `claim_required` transfer, one at a time per destination chain, does not double-claim, and reports per-intent results while continuing past failures. Report `refunded`, `failed` or `expired` without automatically re-bridging. `OUTCOME_UNKNOWN` on a claim gates only its destination chain. Later use `oku bridge claim --intent ID --json` to confirm status; it does not resend. Circle Standard can take roughly 15-20 minutes. Stop polling when `status --all` is empty or only user-paused bridges remain.

## 4. Verify and report

```sh
oku wallet balances --chains TARGET_CHAIN_IDS --stables --json
# Include explicit target token reads when the chosen token is not a configured stable.
```

Report **per target pair**: chain name and ID, exact token contract, agent address and live token balance. Compare each destination increase against bridge `min_out` totals for that pair; distinguish previous holdings from newly consolidated funds. Report any non-target representation left over as not consolidated. Include a per-source-chain table of tokens sold (amount, output), zero/dust/unread/skipped tokens and reasons, stay or bridge, chosen C and D with scoring rationale, bridge route, intent IDs, transaction hashes and actual amount received. State whether sources came from CSV, another supplied list, or live discovery. List open claims, guardrail pauses, overrides and unknown outcomes. Leftover source-chain gas is expected. Sending funds onward to any external address is outside this consolidation authorization and needs the user's exact destination address and explicit send instruction.
