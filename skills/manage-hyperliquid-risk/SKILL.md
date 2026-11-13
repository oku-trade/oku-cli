---
name: manage-hyperliquid-risk
description: Manage Hyperliquid protection, leverage, margin mode and isolated margin with the Oku Agent through prepare and submit. Use when the user explicitly asks to add or move a stop loss or take profit, change leverage or margin mode, or add or remove isolated margin. Moving protection uses atomic modify, never cancel-first.
---

# Manage protection, leverage and margin

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets. Oku runs on mainnet only: never suggest a testnet or sandbox.

Protection orders require `trade`. Leverage and margin changes require the separately granted `risk` scope (check `oku auth status`). Confirm commands and flags in this build with `oku docs commands` and `oku docs schema`. Each leverage or margin change is its own exchange action and is never atomic with an order. Only change leverage or margin when the user asked for that specific change.

## Inspect first

```sh
oku markets list --market BTC --json      # maxLeverage, onlyIsolated
oku positions list --profile <p> --json   # current leverage type and value, margin used
oku orders list --profile <p> --fresh --json   # protective triggers, children and active TWAPs
```

## Stop loss and take profit

Use the live position to determine the closing side (`sell` for long, `buy` for short). Inspect nested `children` as well as top-level orders, including `tpsl`, `triggerPx`, `isPositionTpsl` and `reduceOnly`. Whole-position TP/SL orders can have size `0`, so do not mistake them for missing protection.

To move an existing fixed-size stop or take-profit, follow manage-hyperliquid-orders and use atomic `orders modify`. Never automatically cancel a stop first, even when the goal is to move it to breakeven. Carry forward size, closing side and market-trigger or trigger-limit execution mode unless the user authorized changing them:

```sh
oku orders modify --profile <p> --market <market> --order-id <sl-oid> --side <closing-side> --size <existing-size> --trigger-price <new-stop> --tpsl sl --market-trigger --json
```

For a fixed-size trigger-limit stop, replace `--market-trigger` with `--price <execution-limit>`. For a fixed-size take-profit use `--tpsl tp`.

Whole-position stops and take-profits (`isPositionTpsl`, often listed with size `0`) cannot be modified atomically with this backend. Supplying the live position size would turn them into fixed-size protection. The CLI refuses the operation at preparation and submission. Leave protection in place, explain the limitation and never automatically cancel first. Cancel and re-place with `--close-position` only with explicit authorization that acknowledges the unprotected interval, then review and submit the separate actions. Moving a stop to entry does not by itself authorize removing it.

To add whole-position protection when none exists for that leg:

```sh
oku orders prepare --profile <p> --market <market> --side <closing-side> --type stop_market --trigger-price <stop> --close-position --json
oku orders prepare --profile <p> --market <market> --side <closing-side> --type take_market --trigger-price <take-profit> --close-position --json
```

`--close-position` uses position TP/SL grouping and tracks the position size. For a fixed-size partial exit, use `--size <base-size> --reduce-only` instead. `stop_limit` and `take_limit` also need `--price <execution-limit>`. Stops and take-profits are independent actions here, not an atomic pair. For protection attached to a new entry use open-hyperliquid-position instead. Do not silently duplicate a leg or create a new order to replace an existing stop.

These protection commands only prepare. Review market, side, trigger, execution mode, size or whole-position semantics and reduce-only behavior, then submit the exact intent and digest using `orders submit`. On an ambiguous outcome, reconcile without resubmitting. Verify protection with `orders list --fresh` after submission. Trigger-market execution has price risk, and trigger-limit execution may not fill. Never invent risk levels or claim protection guarantees a fill.

## Leverage and margin mode

Prefer `{"market":"BTC","leverage":2,"mode":"cross"}` in `leverage.json` after reading `oku docs schema --command 'positions leverage'`, then `oku positions leverage --profile <p> --input leverage.json --json`. Alternatively use flags:

```sh
oku positions leverage --profile <p> --market BTC --leverage <user N> --mode cross|isolated --json
```

Leverage is a whole number from 1 up to the market maximum. Cross mode is refused on isolated-only markets. Higher leverage moves the liquidation price closer.

In a prepared leverage intent, `is_cross` is omitted when false: an absent field means isolated mode, and `"is_cross": true` means cross. The preview `summary` line repeats the market, leverage and mode, and must match the requested change exactly. After `completed`, confirm with a fresh `oku positions list`; a market with no open position has no row to check yet, so rely on the summary then and re-verify once a position exists.

## Isolated margin

Prefer `{"market":"BTC","amount":"10"}` in `margin.json` after reading `oku docs schema --command 'positions margin'`, then `oku positions margin --profile <p> --input margin.json --json`. Alternatively use flags:

```sh
oku positions margin --profile <p> --market BTC --amount <USDC> --json    # a negative amount removes margin
```

This only works on an existing isolated position. Amounts allow at most 6 decimals and are never rounded. Removing margin raises liquidation risk.

## Submit

Both leverage and margin commands only prepare. Show the summary, validate it against exactly the change the user asked for (market, leverage value, cross or isolated, margin amount sign), then run:

```sh
oku orders submit --profile <p> --intent <intent_id> --digest <digest> --json
```

Only a server state of `completed` means success. This skill never changes leverage or margin beyond what the user explicitly asked for; a generic trade request does not authorize a risk change, and a risk change is never bundled with an order. Once submitted, anything else, including an error status, is an unknown outcome. Re-read positions and follow reconcile-hyperliquid-actions. Never resend.
