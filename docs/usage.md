# Usage

> Grammar as finalized by the CLI worker. `oku --help` is authoritative if they differ.

## Output contract

- `--json`: exactly one JSON document on **stdout**. Diagnostics go to **stderr**.
- `schema_version` is `oku.cli/v1`. Each response has `request_id` and either `data` or `error` (`code`, `message`, `retryable`).
- Quantities and prices are decimal strings.
- Account and network must be explicit (via profile or flags). Don't rely on an implicit default in automation.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | ok |
| 1 | internal error |
| 2 | usage error |
| 3 | auth (missing/locked/revoked credentials) |
| 4 | not found |
| 5 | upstream (Oku/exchange) error |
| 6 | `OUTCOME_UNKNOWN`, reconcile, do not resubmit |
| 7 | conflict / rejected |

## Credentials

Credentials are stored in an encrypted local file (no OS keyring). Unlock comes from an interactive TTY passphrase prompt or, for automation, `OKU_PASSPHRASE_FILE=<path>` pointing at a file readable only by you. Never pass the passphrase as an argument.

## Commands

```sh
oku auth setup            # device enrollment (backend contract pending)
oku auth status --json

oku profiles list
oku profiles add ...
oku profiles use <name>
oku profiles remove <name>

oku markets list --json
oku account summary --json
oku positions list --json
oku orders list --json

oku orders prepare --market BTC --side buy --type limit --size <size> --price <price> --json
#   local only: returns intent_id and digest, nothing sent
oku orders submit --intent <intent_id> --digest <sha256> --json
oku orders status ... --json
oku orders cancel ... --json
oku orders reconcile --json

oku skills list
oku skills install   --target jcode|claude|codex [--dir <path>] [--force]
oku skills uninstall --target jcode|claude|codex [--dir <path>]
```

`--digest` binds submission to the exact prepared intent. If anything changes, prepare again. There is no per-order web approval: this is **trusted-host delegated mode** (see `security.md`).

## Order states

HTTP success is not a fill. On timeout or connection loss after submit, exit 6 / `OUTCOME_UNKNOWN`: use `oku orders status` / `oku orders reconcile`. **Never place a replacement until the original is resolved.**
