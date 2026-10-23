---
name: open-hyperliquid-position
description: Open or increase a Hyperliquid perps position with the Oku Agent, using current market data, risk inputs supplied by the user, and the prepare, validate, submit flow. Use when the user asks to buy, sell, long or short a market via Oku.
---

# Open a Hyperliquid position

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets.

Requires the `trade` scope. This spends real funds.

## 1. Work out the inputs, ask only for what is genuinely missing

You need the market, the side, the size in base units and the order type. Do not interrogate the user for values the request already implies:

- **Type:** default to a market order unless the user gives a price or asks for a limit order.
- **Slippage:** omit `--slippage-bps` to use the CLI default (50 bps = 0.5%), unless the user names a tolerance. The preview shows the worst price; validate it against that tolerance before submitting.
- **Size:** use the size the user states. When the size is derived (a USD amount like "$500 of RKLB", a fraction like "half" or "a third"), compute it and round **down** to the market's `szDecimals` yourself. Never ask the user to choose between two rounded sizes; just state the size used in the preview. For "flip" requests, see below.
- **Flip** ("flip my short to long"): one market order on the opposite side for **twice** the current absolute position size, not reduce-only. This closes the position and opens the same size the other way in one fill. Only ask if the user wants a different size for the new side.
- **Market:** pass the symbol the user says (`RKLB`, `AVAX`). The CLI resolves stock and other HIP-3 markets (`xyz:RKLB`) itself; do not search for the prefix. HIP-3 markets are often isolated-only, so if the user names leverage, use isolated mode without asking.

Ask only when the size or side is truly ambiguous (for example "buy some BTC" with no amount). Never change leverage as a side effect. A leverage or margin-mode change is a separate action (manage-hyperliquid-risk) and is not atomic with the order. If margin looks insufficient, say so in the preview rather than refusing.

## 2. Check the current state

```sh
oku markets list --market BTC --json   # szDecimals and maxLeverage
oku markets quote BTC --json
oku account summary --profile <p> --json
oku positions list --profile <p> --json
```

## 3. Prepare (nothing is sent to the exchange)

Prefer a reviewed `order.json` object, with keys discovered using `oku docs schema --command 'orders prepare'`:

```json
{"market":"BTC","side":"buy","type":"limit","size":"0.0002","price":"60000","tif":"gtc"}
```

```sh
oku orders prepare --profile <p> --input order.json --json
```

Alternatively, pass command options as flags:

```sh
oku orders prepare --profile <p> --market BTC --side buy --type limit --size <size> --price <price> [--tif gtc|ioc|alo] --json
oku orders prepare --profile <p> --market BTC --side buy --type market --size <size> [--slippage-bps <bps>] --json
```

If the order arrives as a messy JSON draft, `oku drafts repair --stdin` returns a candidate and the matching prepare arguments. It never fills in missing values. The candidate still has to be prepared and reviewed.

## 4. Validate the prepared preview

Go straight from the request to a prepared preview in one turn. Show the account, network, market, side, size, price or worst price, reduce-only, builder fee, expiry and any notes (for example, builder fee not approved). The digest binds these exact fields.

Reviewing the preview is your validation step, not a second user gate. Check every field against what the user authorized: right market, side and size, the requested order type, price or worst price within their tolerance, sufficient margin, no leverage or margin-mode side effects. Fix problems by preparing again, not by asking the user to catch them.

## 5. Submit within the authorized scope

```sh
oku orders submit --profile <p> --intent <intent_id> --digest <digest> --json
```

The user's action request is the authorization: a clear "buy $500 of RKLB" or "flip my AVAX short" covers preparing, validating and submitting that action without asking again. A bounded standing instruction ("open these three positions at the listed sizes") authorizes each action in the batch within its stated bounds.

Submit the exact digest from the prepare; never alter the payload or digest. Intents expire after 5 minutes by default. If an intent expires or the market moves past the user's price or slippage bound, prepare again and submit only within the original authorized bounds. Anything new or broader (a different market, side, size or order type, a worse tolerated price, more risk) needs new permission.

Ask only when the size or side is genuinely ambiguous or the prepared action deviates materially from what was authorized. On an unknown outcome, do not ask first: follow reconcile-hyperliquid-actions and only involve the user if recovery needs human input.

## 6. After submitting

A submitted order may rest on the book or fill partially. Check with `oku orders status --intent <id> --json` and `oku positions list`. On exit 6 or a timeout, do not resubmit. Follow reconcile-hyperliquid-actions. New orders stay blocked while any intent has an unknown outcome.
