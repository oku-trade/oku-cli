---
name: reconcile-hyperliquid-actions
description: Resolve Hyperliquid Oku Agent intents whose outcome is unknown or still in flight (exit 6, OUTCOME_UNKNOWN, timeouts, crashes) without ever blindly resending. Use after a failed or interrupted Hyperliquid submit, or when oku blocks new orders because an intent is unresolved. For EVM swaps, sends or deposits, use move-agent-funds instead.
---

# Reconcile unknown outcomes

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets. This skill covers Hyperliquid intents, not onchain funds intents; for the latter follow move-agent-funds and the corresponding `swap`, `send` or `deposit` status command.

## The rule

An unknown outcome is not a failure, because the action may already be on the exchange. Until the state is known, never resubmit, never place a replacement, and never retry in a loop.

Use the same profile and API environment throughout recovery. `oku docs commands --json` lists current flags; `oku docs schema --command 'orders status'` describes JSON input for a specific intent. Never change a digest or re-prepare to resolve an unknown action.

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

- **Submitted or completed:** done. Report the effect, such as a fill or a resting oid. A `submitted` local record can remain stale after a later fill or cancel, so confirm with fresh `orders list`, `positions list` and `fills list` before treating it as proof the order is still resting.
- **Rejected:** nothing executed. The intent is closed. A retry is a new prepare, submitted without asking again only within the original authorized scope; anything new or broader needs new permission. An unknown outcome is never retried: reconcile first.
- **Still unknown:** stop, report it, and run reconcile again later. New exposure stays blocked while an intent is unknown. That is intentional.
- **Never signed (prepared or created):** `oku orders abandon --intent <id>` makes sure it can never execute.

Risk actions (leverage and margin) never have an order id. Only `completed` proves they were applied. Check `positions list` for the resulting leverage or margin.
