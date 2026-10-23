# Live skill evaluation pilot: 2026-10-23

## Scope and method

The user authorized real-money evaluation with a $5 onchain loss budget. This pilot evaluated `skills/move-agent-funds/SKILL.md`, not the complete skill collection. No fake chain, fake API, or simulated execution was used.

- Target: installed `oku`, run by fresh Claude Code sessions with the skill injected.
- Runtime: `uv run` for Python; SkillOpt installed in an isolated uv environment.
- Optimization: recorded live trajectories, offline model reflection, Microsoft SkillOpt's `apply_patch_with_report`, manual source review, and subsequent real-money evaluation. This was a small human-reviewed pilot, not the full SkillOpt training loop or a benchmark reproduction.
- Evidence: independent Arbitrum/Base RPC balances and receipts, Oku bridge destination status, and read-only SQLite journal inspection.
- Budget check: before each run, reserve its entire outbound principal plus $0.50 for costs. This is a cooperative runner check, not an unbypassable wallet policy. USD figures use the observed reference price of $2,388.93 per ETH and exclude model costs and subsequent market movements.

## Results

| Run | Live task | Observed result | Net cost at reference price |
|---|---|---|---:|
| Swap baseline | 0.0005 ETH to USDC and back, Arbitrum | Two successful receipts; no USDC remaining | $0.01756 |
| Swap candidate | 0.0004 ETH to USDC and back, Arbitrum | Two successful receipts; accurate zero-balance explanation | $0.02108 |
| Bridge baseline | 0.0004 ETH Arbitrum to Base, then back less reserve | Both destination fills verified; **failed post-gas reserve criterion** | $0.01330 |
| Bridge candidate | Separate Arbitrum/Base round trip | Both fills verified; post-gas reserve criterion passed | $0.01332 |

Total net cost across both chains: approximately **$0.06525**. Eight source transactions were confirmed. Four bridge destination-fill receipts were also checked independently. No outstanding pilot bridges remained.

Final native balances at the end of the pilot:

- Arbitrum: 0.041681536671983214 ETH.
- Base: 0.000062372301377637 ETH, deliberately retained as gas reserves rather than spent returning dust.

## Retained skill changes

### ERC-20 balance interpretation

The baseline agent first queried native-only balances, then incorrectly explained an omitted USDC row as matching the expected CSV amount. The optimizer initially repeated this false inference. Source review rejected it: `pkg/onchain/multicall.go` omits zero balances, while CSV amount is used for mismatch annotation. The corrected skill explains explicit token manifests and zero-balance omission. A subsequent live swap run reported the semantics correctly.

### Gas-inclusive native reserve

The first bridge agent left the requested floor **before** paying return-transaction gas. Its final Base balance was 29,686,365,267,930 wei, below the requested 30,000,000,000,000 wei floor.

The retained instruction caps native input at balance minus the required floor minus estimated gas minus a safety margin, subject to the original authorization amount. Re-read balances after confirmation.

In the subsequent run, the required final Base balance was 59,686,365,267,930 wei including the pre-existing balance. Actual final balance was 62,372,301,377,637 wei. The validation prompt also made the post-gas requirement more explicit, so this is not a controlled demonstration that the skill alone caused the improvement.

## Transaction evidence

Swap baseline:

- `0x42e2e743a4984913862e59c3e1ed93c516a7f8d5489b036c2aa71f897cfff938`
- `0xa57e52ee5ac7a662a984997acfb06a4a11346a6b781ecbc6a44fc93a1ab20ee1`

Swap candidate:

- `0xec7bbcd563c1aa88111dfd9a2afaea0c20658818b57b472ef9e6fa14e889652d`
- `0x70aa9152aa2324caf378baead00fd8d442d096166673fbaf3f5e1d20d24f777e`

Bridge baseline source transactions:

- Arbitrum: `0xa3ec84d40325b8313cdcb346958d9485c9cc6398f7f84f234b0005338a4e31c9`
- Base: `0xe606eb4a6d6c1e458b459f6942854b1e0e80fcb950adcd11b401b217b59eb3ac`

Bridge candidate source transactions:

- Arbitrum: `0x26498418ac8e3e06d041ab512f678fc4d662325277c2b09031fc2f9c4bab889e`
- Base: `0x3029e998130def54b69648dcdf62b1d1b80f0efb78bf7d433ac73bf03c331a69`

## Artifacts and limitations

Local runner scripts, prompts, model trajectories, proposed/rejected patches, and deterministic scores are retained at `~/.local/state/oku-skill-evals/2026-10-23/`. They are intentionally not committed as public conversation data. These scripts are specific to the authorized pilot wallet, date and budget, and are **not** unattended production runners.

The two swap runs passed all four financial outcome checks. The bridge baseline passed three of four, and its candidate run passed all four. These are descriptive pilot results, not statistically reliable estimates or proof of broad safety.

In the original four-run pilot, no ambiguous broadcasts, RPC outages during transaction execution, reorgs, expired solver orders, required bridge claims, or Hyperliquid perps were exercised. Successful ordinary swaps and bridges do not validate recovery from those cases. No new test files were needed and no scaffolding tests were added to the repository.

## Hyperliquid follow-up

The subsequent live evaluation added a Hyperliquid ETH round trip. The user approved setting ETH to 10x isolated and buying 0.005 ETH, followed by a reduce-only close with 50 bps slippage. The leverage action reached server state `completed`. Entry and close reached `filled`:

| Action | Exchange order ID | Size | Fill price |
|---|---|---:|---:|
| Open ETH long | `583160267544` | 0.005 ETH | 2389.9 USDC |
| Reduce-only close | `583160459613` | 0.005 ETH | 2389.6 USDC |

Gross realized P&L was -0.0015 USDC. Fill fees totaled 0.013141 USDC, including builder fees, for **0.014641 USDC net cost**. The isolated position snapshot showed 1.187879 USDC margin used. After the close, ETH was absent from positions and the fresh open-order list was empty. Existing AVAX, BNB and ZRO position sizes remained unchanged. ETH's per-market configuration remains 10x isolated; no unrelated market configuration was changed.

The account also contains an older October 15 staging-origin RKLB journal record in `submitted` state. Production reconciliation returned `NOT_FOUND`. It was retained, not erased or declared settled. Source inspection confirmed that this submitted record is not an account-blocking signing/submitting/unknown action.

This follow-up validates an ordinary risk-setting action, entry and reduce-only close. It does not validate cancellation, partial fills, ambiguous submissions or recovery from exchange outages. The operator, rather than a fresh autonomous evaluator, executed this follow-up.

The user then requested removing redundant per-preview confirmation prompts. Hyperliquid guidance now treats a clear action request or bounded standing instruction as authorization within its scope. Payload validation, digest checks, explicit risk-change authorization, missing-input questions and unknown-outcome reconciliation remain required. This is a user-requested policy change, not an optimizer-discovered performance improvement.
