# Usage

> Grammar verified against the built binary. `oku --help` is authoritative if they differ.

## Output contract

- `--json`: exactly one JSON document on **stdout**. Diagnostics go to **stderr**.
- Envelope: `schema_version` (`oku.cli/v1`), `request_id`, `command`, `ok`, `data`, `error` (`code`, `message`, `retryable`) or `null`.
- Quantities and prices are decimal strings.
- Only Hyperliquid **mainnet** is supported. Trading requires a profile with an account. In automation, pass `--profile` explicitly.

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

## Global flags

`--profile NAME` (default `$OKU_PROFILE` or config default), `--network NAME` (only `mainnet` is supported and is checked against the profile), `--account 0x..` (for read commands), `--json`, `--timeout DUR` (default 15s, max 2m).

## Commands

Taken from `oku --help` at commit `ca84dac`:

```sh
oku version
oku doctor
oku config path | config show          # no secrets printed

oku auth setup                          # enrollment code from the Oku website
oku auth status [--verify]
oku auth revoke                         # revokes the Oku API token + deletes local credentials only

oku profiles add NAME --account 0x.. [--api-url URL] [--default]
oku profiles list | use NAME | remove NAME

oku markets list
oku markets quote COIN
oku markets funding COIN [--since 24h]
oku account summary
oku positions list
oku orders list
oku fills list

oku orders prepare --market BTC --side buy|sell --type limit|market --size <dec> \
  [--price <dec>] [--tif gtc|ioc|alo] [--slippage-bps N] [--reduce-only] \
  [--expires 5m] [--intent-id ID]
#   local only, sends nothing. Returns intent_id and digest.
#   --price for limit orders. --slippage-bps is required for market orders.
oku orders submit --intent ID --digest SHA
oku orders status --intent ID
oku orders reconcile
oku orders cancel --market COIN --order-id OID [--intent-id ID]
oku orders abandon ...                  # only for never-signed intents: cancels the server action so it can never execute

oku skills list
oku skills install   --target jcode|claude|codex | --dir PATH [--force]
oku skills uninstall --target jcode|claude|codex | --dir PATH [--force]
```

Skill install locations: jcode `~/.jcode/skills`, Claude Code `~/.claude/skills`, Codex `~/.agents/skills`. `uninstall` only removes files that match the bundled version unless `--force` is given.

Example error (no profile configured): `orders prepare` prints `{"ok":false,"error":{"code":"PROFILE_NOT_FOUND",...}}` and exits 2.

`--digest` binds submission to the exact prepared intent. If anything changes, prepare again. There is no per-order web approval: this is **trusted-host delegated mode** (see `security.md`).

## Order states

HTTP success is not a fill. On timeout or connection loss after submit, exit 6 / `OUTCOME_UNKNOWN`: use `oku orders status --intent ID` / `oku orders reconcile`. **Never place a replacement until the original is resolved.**
