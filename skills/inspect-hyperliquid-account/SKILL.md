---
name: inspect-hyperliquid-account
description: Read a Hyperliquid account through the Oku CLI, including balances, margin, positions, open orders, fills, funding and builder-fee status, without changing anything. Use when the user asks what they hold, owe, or have open.
---

# Inspect a Hyperliquid account

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets.

All commands here are reads. Use the account the user named, with `--profile <p>` or `--account 0x...`. Never switch accounts silently.

```sh
oku account summary --profile <p> --json      # equity, margin used, withdrawable, builder-fee approval
oku positions list --profile <p> --json       # size, entry, liquidation price, leverage, margin used
oku orders list --profile <p> --json          # open orders (add --fresh to bypass cache)
oku fills list --profile <p> --market BTC --json
oku markets quote BTC --json                  # mark, mid, oracle
oku markets funding BTC --since 24h --json
oku markets list --json                       # size decimals, max leverage, isolated-only flag
```

## Interpreting results

- Every read includes `meta.fetched_at` and `meta.source`. Report the time along with the numbers.
- Missing, stale or error data is an error. Never report it as a zero balance or "no positions".
- Hyperliquid position sizes are signed. A negative size is short.
- `orders list` shows all exchange orders, including ones placed outside Oku. Local intent history is `oku orders status --intent ID`.

## Permissions and consequences

Needs only public data or the `read` scope. Nothing here changes the account.
