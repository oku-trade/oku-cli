---
name: manage-hyperliquid-orders
description: Track, atomically modify or cancel Hyperliquid orders with the Oku Agent, including TP/SL triggers, attached children, active TWAPs, batch cancels and unsigned intents. Use when the user asks about order status or wants to cancel or change an order. Review prepared actions and confirm any ambiguity or deviation before submit.
---

# Manage Hyperliquid orders

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets.

## Status (read-only)

```sh
oku orders list --profile <p> --fresh --json                  # orders, children and active_twaps
oku orders status --profile <p> --intent <intent_id> --json   # local intent state and server state
```

Inspect `orderType`, `triggerPx`, `triggerCondition`, derived `tpsl` (`tp` or `sl` when recognized), `isPositionTpsl`, `reduceOnly` and nested `children`. Position TP/SL can have size `0`, meaning the whole position rather than no protection. Match market, closing side and the particular child `oid` before modifying or cancelling. `active_twaps` has its own `twapId`, status and executed size. A TWAP id is not an order id. Unknown TWAP statuses remain visible rather than being treated as completed.

## Cancel (mutation, needs the trade scope)

Only cancel when the user asks. Identify the order by market and exchange order id (`oid` from `orders list`).

Prefer `{"market":"BTC","order-id":12345}` in `cancel.json` after checking `oku docs schema --command 'orders cancel'`, then `oku orders cancel --profile <p> --input cancel.json --json`. Alternatively use flags:

```sh
oku orders cancel --profile <p> --market BTC --order-id <oid> --json
```

A cancel can race a fill. Afterwards, re-read `orders list` and `positions list` and report what actually happened. The cancelled intent's `orders status` may still show a cached `submitted` state; treat fresh `orders list` and `positions list` as the evidence, and never resubmit or cancel again based on the stale snapshot.

Single-order cancel above submits immediately. Use `--prepare` to review it first. Batch cancels use comma-separated ids for one market, and prepare an intent. A TWAP cancel also prepares an intent:

```sh
oku orders cancel --profile <p> --market <market> --order-id <oid> --prepare --json
oku orders cancel --profile <p> --market <market> --order-ids <oid1,oid2> --prepare --json
oku orders cancel-twap --profile <p> --market <market> --twap-id <twap-id> --json
```

Review the exact market and every id, then submit using the intent and digest below. Cancelling a TWAP stops future slices, it does not reverse fills already executed. Do not cancel protective children unless the instruction covers removing that protection.

## Atomic modify

Use `orders modify`, never a cancel followed by a replacement, to move an existing stop or take-profit. Cancellation first creates an unprotected interval. Re-read the order before preparing and carry forward the existing side, size and execution mode unless the user explicitly changed them.

```sh
oku orders modify --profile <p> --market <market> --order-id <oid> --side <buy|sell> --size <existing-size> --price <new-limit> --json
oku orders modify --profile <p> --market <market> --order-id <sl-oid> --side <closing-side> --size <existing-size> --trigger-price <new-stop> --tpsl sl --market-trigger --json
oku orders modify --profile <p> --market <market> --order-id <tp-oid> --side <closing-side> --size <existing-size> --trigger-price <new-tp> --tpsl tp --price <execution-limit> --json
```

`--market-trigger` is for market-trigger legs only. A trigger-limit leg needs `--price` and must not be silently converted to a market-trigger leg. Whole-position TP/SL uses the existing size `0` and position TP/SL semantics, not a newly sized order. If the installed build cannot atomically modify that order, stop and report the limitation. Never fall back to cancel-first.

Modify only prepares. Review the exact `oid`, side, size, trigger kind, trigger price, execution limit, reduce-only and position TP/SL semantics. A clear request to change a named order to a specified price or size authorizes a matching preview. USD-to-base and position-fraction conversions rounded down to `szDecimals` are deterministic, so state the computed size. Confirm ambiguity, guesses or deviations, including material rounding. Submit prepared modify, batch cancel and TWAP cancel intents with:

```sh
oku orders submit --profile <p> --intent <intent_id> --digest <digest> --json
```

Afterwards, reconcile status and re-read `orders list --fresh` and `positions list`. An atomic modify can still race a fill or be rejected. Verify the resulting order and protection, not just the old id's disappearance. On timeout or an unknown outcome, follow reconcile-hyperliquid-actions without retrying the mutation.

## Abandon an unsigned intent

```sh
oku orders abandon --profile <p> --intent <intent_id> --json
```

This only works for intents that were never signed. It cancels the pending server action so it can never execute. Signed intents must be reconciled instead (reconcile-hyperliquid-actions).
