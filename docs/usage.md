# Usage

> Grammar is provisional and may change before release. `oku --help` is authoritative.

## Output contract

- `--json`: exactly one JSON document on **stdout**. Diagnostics, progress and warnings go to **stderr**.
- Every JSON response carries `schema_version`, `request_id`, and either `data` or a structured `error` (`code`, `message`, `retryable`, known execution state).
- Quantities and prices are decimal strings. Never round through floats.
- Non-interactive runs never wait for prompts. They fail with a structured error such as `APPROVAL_REQUIRED`.
- Account and network must be explicit (`--account`, `--network`). Don't rely on a default profile in automation.

## Commands (provisional)

```sh
oku setup                 # device enrollment (pending backend contract)
oku auth status
oku doctor
oku markets list --json
oku markets quote BTC --json
oku account summary --account <addr> --network mainnet --json
oku positions list --account <addr> --network mainnet --json
oku orders list --account <addr> --network mainnet --json

oku orders prepare --account <addr> --network mainnet --market BTC --side buy \
  --type limit --size <size> --price <price> --json
oku orders submit  --intent <intent-id> --json
oku orders status  --client-id <client-order-id> --json
oku orders cancel  --order-id <order-id> --json
oku orders reconcile --json

oku skills install --target <agent>
```

## Order states

`prepared` → `submitting` → `accepted`/`resting` → `partially_filled` → `filled` | `canceled` | `rejected` | `unknown`.

HTTP success is not a fill. On timeout or connection loss after submit, the outcome is `OUTCOME_UNKNOWN`: run `oku orders status`/`oku orders reconcile` with the same client order id. **Never resubmit a replacement order until the original is resolved.**
