---
name: manage-hyperliquid-orders
description: Track, cancel or replace Hyperliquid orders with the Oku Agent, covering intent status, open orders, cancels and abandoning unsigned intents. Use when the user asks about an order's status or wants to cancel or change one. The user's cancel or replace request is the authorization; prepare, validate and submit within its stated scope without asking again.
---

# Manage Hyperliquid orders

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets.

## Status (read-only)

```sh
oku orders list --profile <p> --json                          # open exchange orders with oid
oku orders status --profile <p> --intent <intent_id> --json   # local intent state and server state
```

## Cancel (mutation, needs the trade scope)

Only cancel when the user asks. Identify the order by market and exchange order id (`oid` from `orders list`).

Prefer `{"market":"BTC","order-id":12345}` in `cancel.json` after checking `oku docs schema --command 'orders cancel'`, then `oku orders cancel --profile <p> --input cancel.json --json`. Alternatively use flags:

```sh
oku orders cancel --profile <p> --market BTC --order-id <oid> --json
```

A cancel can race a fill. Afterwards, re-read `orders list` and `positions list` and report what actually happened. The cancelled intent's `orders status` may still show a cached `submitted` state; treat fresh `orders list` and `positions list` as the evidence, and never resubmit or cancel again based on the stale snapshot.

## Amend

This build has no amend command (confirm with `oku docs commands`). To change the price or size:

1. Cancel the order.
2. Confirm the cancel took effect.
3. Prepare and submit the new order within the same authorized scope (same market, side, size and bounds), without asking again. A different market, side, size or broader scope needs new permission.

Tell the user these steps are not atomic, so the original order may fill in between.

## Abandon an unsigned intent

```sh
oku orders abandon --profile <p> --intent <intent_id> --json
```

This only works for intents that were never signed. It cancels the pending server action so it can never execute. Signed intents must be reconciled instead (reconcile-hyperliquid-actions).
