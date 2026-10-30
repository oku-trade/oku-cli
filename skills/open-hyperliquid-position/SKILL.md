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
- **Size:** use the size the user states. A USD amount or fraction of the current position is a concrete size instruction: convert using current market data, round **down** to the market's `szDecimals`, and state the computed base size in the preview. No separate confirmation is needed if it matches the instruction and rounding is not material.
- **Flip** ("flip my AVAX short"): use the opposite side and twice the current absolute position size for a market order, then show the computed size in the preview. This deterministic conversion does not require another confirmation if the preview matches the request.
- **Market:** pass the symbol the user says (`RKLB`, `AVAX`). The CLI may resolve an unambiguous stock symbol to its HIP-3 identifier (`xyz:RKLB`). Confirm if the resolved instrument is not the one requested or if the margin mode differs.

Ask when the size or side is ambiguous (for example "buy some BTC" with no amount). Never change leverage as a side effect. A leverage or margin-mode change is a separate action (manage-hyperliquid-risk) and is not atomic with the order. If margin looks insufficient, say so in the preview rather than refusing.

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

Review the preview against the request: market, side, computed base size, order type, price or worst price, slippage, leverage and margin mode. A clear request such as "buy $500 of RKLB" or "flip my AVAX short" authorizes submit after reviewing a matching preview. Confirm only if the request is ambiguous, the agent had to guess, or the preview differs (including market resolution, margin mode, price beyond tolerance, slippage, leverage side effects or materially different size after rounding). Fix invalid orders by preparing again.

## 5. Submit within the authorized scope

```sh
oku orders submit --profile <p> --intent <intent_id> --digest <digest> --json
```

A clear request identifying the market, side and size, including a USD amount, a fraction of a current position, or a flip, authorizes submit after reviewing a matching preview. State any deterministic base-size calculation and rounding in that preview. A bounded standing instruction authorizes only the concrete actions and limits it names.

Submit the exact digest from the prepare; never alter the payload or digest. Intents expire after 5 minutes by default. If an intent expires or the market moves past the user's price or slippage bound, prepare again and submit only within the original authorized bounds. Anything new or broader (a different market, side, size or order type, a worse tolerated price, more risk) needs new permission.

Confirm ambiguity, guesses, or a preview that differs from the request in size (including material rounding), price or tolerance, leverage, margin mode, slippage, side or market. On an unknown outcome, follow reconcile-hyperliquid-actions without resubmitting.

## 6. After submitting

A submitted order may rest on the book or fill partially. Check with `oku orders status --intent <id> --json` and `oku positions list`. On exit 6 or a timeout, do not resubmit. Follow reconcile-hyperliquid-actions. New orders stay blocked while any intent has an unknown outcome.
