---
name: reconcile-hyperliquid-actions
description: Resolve Oku CLI intents whose outcome is unknown or still in flight (exit 6, OUTCOME_UNKNOWN, timeouts, crashes) without ever blindly resending. Use after any failed or interrupted submit, or when oku blocks new orders because an intent is unresolved.
---

# Reconcile unknown outcomes

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets.

## The rule

An unknown outcome is not a failure, because the action may already be on the exchange. Until the state is known, never resubmit, never place a replacement, and never retry in a loop.

## Find unresolved intents

```sh
oku doctor --profile <p> --json                    # counts unresolved intents
oku orders status --profile <p> --intent <id> --json
```

## Resolve with Oku

```sh
oku orders reconcile --profile <p> --json
```

This asks Oku for the authoritative state of each unresolved intent and records it. It needs the same scope as the original action. Afterwards, check `orders list`, `positions list` and `fills list` to confirm what actually happened on the exchange.

## Outcomes

- **Submitted or completed:** done. Report the effect, such as a fill or a resting oid.
- **Rejected:** nothing executed. Trying again needs a new prepare and new user approval.
- **Still unknown:** stop, report it, and run reconcile again later. New exposure stays blocked while an intent is unknown. That is intentional.
- **Never signed (prepared or created):** `oku orders abandon --intent <id>` makes sure it can never execute.

Risk actions (leverage and margin) never have an order id. Only `completed` proves they were applied. Check `positions list` for the resulting leverage or margin.
