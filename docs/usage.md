# Usage

> `oku --help` and the [generated command reference](commands.md) describe the urfave/cli v3 command tree. Run `make docs-check` to detect reference drift.

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

`--profile NAME` (default `$OKU_PROFILE` or config default), `--network NAME` (only `mainnet` is supported and is checked against the profile), `--account 0x..` (for read commands), `--json`, `--timeout DUR` (default 15s, max 2m). Global flags work before or after a command. Use `--` to end flag parsing.

## Shell completion

Completion scripts are available without credentials or network access. Generate one with `oku completion bash`, `oku completion zsh`, or `oku completion fish`. Zsh/fish scripts use urfave/cli generation; bash uses a safe array-based wrapper around urfave/cli's candidate protocol (the stock bash template evaluates user input). The scripts invoke `oku --generate-shell-completion` when completing; these requests only enumerate command names and flags and never run auth, order, or read handlers. No installation command modifies your shell startup files automatically.

For the current shell session (with `oku` on `$PATH`), enable zsh's `compinit` first:

```sh
# bash
source <(oku completion bash)
# zsh
autoload -Uz compinit && compinit
source <(oku completion zsh)
# fish
oku completion fish | source
```

For persistence, save the output and source it from a shell startup file yourself. For fish, place the script at `~/.config/fish/completions/oku.fish` if desired. Completion can be tested without installing: `oku --generate-shell-completion` prints static candidates.

## Commands

See [the generated reference](commands.md) for every command and flag. Common examples:

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
#   no exchange submission; may read current market data. Returns intent_id and digest.
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
