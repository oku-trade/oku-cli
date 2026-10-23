# SkillOpt evaluation summary

## Method

These are one-pass, human-reviewed SkillOpt patch-engine evaluations, not a reproduction of SkillOpt's full training loop or statistically controlled benchmarks. Real Oku CLI trajectories and exchange/chain data were collected first. Offline reflection proposed per-skill patches, Microsoft SkillOpt's `apply_patch_with_report` applied them to candidate copies, and review rejected unsupported or duplicate changes before validation. Python ran through `uv`. The local SkillOpt checkout was `1a44050`.

No exchange, chain or account responses were mocked. Read-only coverage is not labeled as transaction coverage. The authorization model remains direct execution within a clear request or bounded standing instruction, without repeated preview confirmation.

## Earlier completed work

- `move-agent-funds`: two Arbitrum swap round trips and two Arbitrum/Base bridge round trips supplied the original optimization evidence. Retained changes clarified ERC-20 zero-balance omission and reserving native gas after transaction fees. The first bridge run missed its reserve target; the candidate run met it. The task wording also became more explicit, so this was not a controlled attribution to skill text alone.
- A later parallel batch completed three Arbitrum and two Base swap round trips, plus 18 read-only cases. All transaction receipts succeeded, but the Base worker violated its assigned loss guardrail by adding an unauthorized override. That batch was recorded as a compliance failure rather than an overall pass, and the skill was tightened.
- A direct Hyperliquid smoke test covered isolated leverage configuration, entry, reduce-only exit and post-only order cancellation. This preceded the Hyperliquid SkillOpt pass and was not itself an optimization run.

Detailed prior evidence is in [the live evaluation record](live-skill-evaluation.md).

## Remaining nine skills: review decisions

| Skill | SkillOpt review result | Evidence and validation scope |
|---|---|---|
| `learn-oku` | Retain guidance on bare commands under restricted tool permissions and reporting unavailable conditions honestly | Fresh live Hyperliquid baseline and candidate workflow |
| `inspect-hyperliquid-account` | Retain distinction between mid/mark/oracle and unavailable best bid/ask | Actual market-price envelopes and live account reads |
| `open-hyperliquid-position` | No standalone edit retained; proposed additions duplicate inherited guidance | Live entry, scope, size and fill checks |
| `close-hyperliquid-position` | No standalone edit retained; duplicate and overly broad reconciliation wording rejected | Live reduce-only close and final position checks |
| `manage-hyperliquid-orders` | No standalone edit retained; existing fresh-order cancellation guidance already covers the issue | Live post-only placement/cancel baseline and conditional candidate task |
| `manage-hyperliquid-risk` | Retain structured `is_cross` semantics and flat-market verification limits | Earlier real approved leverage action, source check and current account inspection; no new leverage change in this pass |
| `reconcile-hyperliquid-actions` | Retain warning that local `submitted` can be stale after fill/cancel | Real local records versus fresh exchange state; no induced ambiguous broadcast |
| `ensure-hyperliquid-authentication` | Retain file-store versus desktop-keyring distinction and exact `credential_store` field | Real active keyring session, status and verification; no fresh browser login, lock, logout or revocation |
| `consolidate-agent-funds` | Retain successful-read zero semantics, requested native versus incidental gas distinction, and empty-sweep stop | Real zero-token manifest and native balances, with read-only candidate evaluations; no funded sweep or claim |

All nine received separate reflection/patch passes. Six have retained changes and three remain unchanged after deduplication. Unchanged is a valid review outcome, not a missing pass.

## Review corrections

- The initial consolidation proposal would have classified every asset absent from `tokens` as zero, including native rows. It was rejected and revised to read native balances separately and preserve errors as errors. A later revision that excluded all native gas was also rejected because a native asset can be explicitly requested in the CSV.
- Repeated bare-command advice was kept in the shared entry-point skill instead of copied into every Hyperliquid skill.
- A leverage proposal treated an omitted mode field as unspecified. Source review showed that `is_cross` is an omitted-false boolean, so absence means isolated, while true means cross.
- No proposal was allowed to restore mandatory per-preview confirmation or weaken unknown-outcome reconciliation.

## Live Hyperliquid comparison

The baseline used the current skill bundle in a fresh Claude CLI session. It completed a 0.005 ETH entry and reduce-only exit, and placed/canceled a post-only order. Independent fill inspection found 0.017645 USDC net cost and unchanged pre-existing position sizes, with no remaining test orders or position.

The baseline also attempted shell compositions blocked by the tool permissions and substituted mid-price for a required best-bid check. The latter condition was marked unverified, not passed. Candidate validation used the same task without changing that condition.

The reviewed candidate also completed its 0.005 ETH entry and reduce-only exit, preserved all pre-existing position sizes, and left no test orders or position. Its net cost was 0.019119 USDC. It correctly declined the conditional limit order because actual best-bid data was unavailable, rather than substituting mid-price. This is less execution coverage for the candidate, but better adherence to the task condition.

| Observed measure | Baseline | Reviewed candidate |
|---|---:|---:|
| Runner elapsed time | 123.8 s | 70.9 s |
| Executor Bash calls | 18 | 11 |
| Permission-denial results detected by audit | 2 | 0 |
| New fills | 2 | 2 |
| Test position remaining | None | None |
| Conditional limit order | Executed using an unverified proxy | Skipped because the condition could not be verified |

The lower runtime and call count cannot be attributed solely to the skill edits: the candidate skipped the conditional order and both runs were single live samples. The defensible improvement is observed removal of permission-denied compositions and correct handling of unavailable data, not a statistical performance claim.

Authentication candidates were checked against the actual active desktop-keyring session. Consolidation required two reviewer corrections before the final read-only evaluation followed the requested-token distinction and empty-sweep stop correctly. No funded consolidation or fresh login was performed.

## Round two (parallel)

All ten skills received a second reflection pass in four parallel lanes: Hyperliquid trading (five skills), EVM funds, authentication and consolidation, and discovery and account reads. Only the Hyperliquid lane placed live orders: one 0.005 ETH market entry, a reduce-only exit and an explicit-price post-only order that was placed and canceled. Independent fill audit found 0.013650 USDC net cost, including builder fees, which the exchange counts inside `fee`. ETH ended flat with no open orders. AVAX, BNB and ZRO sizes were unchanged. The funds evaluator stopped at quoted losses of 108 and 116 bps against a 100 bps limit and sent no transaction.

Three baseline traces were interleaved with other sessions. They were preserved, excluded from primary evidence and replaced with exclusively created read-only reruns. Every trace used for a final decision contains exactly one session and one result.

| Skill | Round-two result |
|---|---|
| `move-agent-funds` | Retained: `--max-loss-bps` omitted or `0` uses the 500 bps default. Source-verified in `maxLossFraction`, not exercised by a prepare. |
| `inspect-hyperliquid-account` | Retained: `fetched_at` is CLI-local, reads and `account summary` sub-calls are not atomic, and fill `time` is execution time. A withdrawable-field edit was dropped because baseline already handled it. |
| `reconcile-hyperliquid-actions` | Retained: doctor's count includes nonterminal `submitted` records, while only `signing`, `submitting` and `unknown` block. The first candidate evaluator misclassified a filled record. The final wording lists terminal server states and passed a source-grounded interpretation check, not a second live run. |
| `ensure-hyperliquid-authentication` | Retained: plain status is cached metadata with `verified: false`, and `--verify` may refresh, renew and save credentials. Confirmed by a clean reflection. `--verify` was not run. |
| `consolidate-agent-funds` | Unchanged: the clean reflection rejected the proposed CSV-identity edit because the clean baseline already listed skipped rows correctly. |
| `learn-oku`, `open-hyperliquid-position`, `close-hyperliquid-position`, `manage-hyperliquid-orders`, `manage-hyperliquid-risk` | Unchanged: proposals duplicated existing guidance or made claims that source review rejected. |

Round two did not exercise funded swaps, bridges, consolidation, leverage changes, auth verification or induced unknown outcomes.

## Limits and artifacts

This does not demonstrate recovery from reorgs, ambiguous broadcasts, authentication revocation, liquidation, required bridge claims or funded consolidation. Real authentication verification can refresh a near-expiry credential, so it is not guaranteed to be a side-effect-free status read.

Original trajectories, proposals, reviewed patches, candidate skills, before/after snapshots, independent grades and timing data are kept locally under `~/.local/state/oku-skill-evals/2026-10-23/`. No private conversation transcripts or account artifacts are committed to the repository. No repository tests were added for dependency behavior or mocked exchange responses.
