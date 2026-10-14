---
name: close-hyperliquid-position
description: Close or reduce a Hyperliquid perps position with the Oku Agent using reduce-only orders, then check for leftover size. Use when the user asks to close, flatten, exit, or take profit on a position via Oku.
---

# Close or reduce a position

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets.

Requires the `trade` scope. This spends real funds.

## 1. Read the exact position

```sh
oku positions list --profile <p> --json
oku orders list --profile <p> --json    # resting orders that could change exposure
```

A positive size is long, so close it with `sell`. A negative size is short, so close it with `buy`. Use the absolute size for a full close, or the size the user specified for a partial one.

## 2. Prepare a reduce-only order

Prefer an `order.json` matching `oku docs schema --command 'orders prepare'`, for example `{"market":"BTC","side":"sell","type":"limit","size":"0.0002","price":"60000","reduce-only":true}`. Then run `oku orders prepare --profile <p> --input order.json --json`. Confirm the actual position size and user-provided price first. Flags are an alternative:

```sh
oku orders prepare --profile <p> --market BTC --side sell --type market --size <abs size> --slippage-bps <user bps> --reduce-only --json
oku orders prepare --profile <p> --market BTC --side sell --type limit --size <abs size> --price <user price> --reduce-only --json
```

`--reduce-only` means the order can only shrink the position. It can never flip or grow it. The slippage or price comes from the user.

## 3. Review, approve, submit

Show the preview. Submit only after explicit approval: `oku orders submit --profile <p> --intent <id> --digest <digest> --json`.

## 4. Check what is left

Re-run `oku positions list`. Market orders are IOC and may fill only partially. Limit orders may rest on the book. Report the remaining size and any resting reduce-only orders. Do not automatically send another order for the remainder. Ask the user. If the submit outcome is unknown, follow reconcile-hyperliquid-actions first.
