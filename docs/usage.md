# Usage

> `oku --help`, `oku docs commands` and the [generated command reference](commands.md) describe the urfave/cli v3 command tree of the binary you run. `make docs-check` detects drift between the committed reference and the tree.

## Output contract

- `--json` writes exactly one JSON document to **stdout**. Diagnostics go to **stderr**.
- Envelope fields: `schema_version` (`oku.cli/v1`), `request_id`, `command`, `ok`, `data`, and `error` (`code`, `message`, `retryable`, optional `details`) or `null`.
- Quantities and prices are decimal strings.
- Only Hyperliquid **mainnet** is supported. Trading requires a profile with an account. In automation, always pass `--profile` explicitly.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | ok |
| 1 | internal error |
| 2 | usage error |
| 3 | auth (missing/locked/revoked credentials, or missing scope) |
| 4 | not found |
| 5 | upstream (Oku/exchange) error |
| 6 | `OUTCOME_UNKNOWN`: reconcile, do not resubmit |
| 7 | conflict / rejected |

## Credentials

- **Linux:** an encrypted local file. You unlock it with an interactive masked TTY prompt or, for automation, `OKU_PASSPHRASE_FILE=<path>` pointing at a file readable only by you (mode 0600). Never pass the passphrase as an argument.
- **macOS:** the native Keychain. No passphrase is requested. Official macOS builds need CGO and are built natively.
- Neither platform has a plaintext fallback. See [security](security.md#credential-storage-by-platform).

## Global flags

- `--profile NAME`: defaults to `$OKU_PROFILE` or the config default.
- `--network NAME`: only `mainnet` is supported, and it is checked against the profile.
- `--account 0x..`: for read commands.
- `--json`.
- `--timeout DUR`: default 15s, max 2m.

Global flags work before or after a command. Use `--` to end flag parsing.

## Built-in documentation (offline)

These documents are embedded in the binary. Commands and docs need no network access, credentials or config:

```sh
oku docs list                       # names: architecture, commands, deployment, local-testing, releasing, security, usage
oku docs show security
oku docs search leverage [--limit N]  # default 50, 0 = unlimited
oku docs commands                   # one line per command of this binary
oku docs commands --markdown        # full reference; identical to docs/commands.md
```

## Shell completion

Completion scripts are available without credentials or network access. Generate one with `oku completion bash`, `oku completion zsh`, or `oku completion fish`.

- Zsh and fish scripts use urfave/cli generation.
- Bash uses a safe array-based wrapper around urfave/cli's candidate protocol, because the stock bash template evaluates user input.
- The scripts call `oku --generate-shell-completion` when completing. This only lists command names and flags, and never runs auth, order or read handlers.
- Nothing edits your shell startup files automatically.

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

To make completion persistent, save the output and source it from a shell startup file yourself.

## Commands

See [the generated reference](commands.md) for every command and flag. Common examples:

```sh
oku version
oku doctor
oku config path | config show          # no secrets printed

oku auth login                          # device grant, completed on https://oku.trade/agent/authorize
oku auth status [--verify]
oku auth logout                         # best-effort session/grant revoke, deletes local credential

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
oku orders abandon --intent ID          # only for never-signed intents

oku skills list
oku skills install   --target jcode|claude|codex | --dir PATH [--force]
oku skills uninstall --target jcode|claude|codex | --dir PATH [--force]
```

Example error with no profile configured: `orders prepare` prints `{"ok":false,"error":{"code":"PROFILE_NOT_FOUND",...}}` and exits 2.

`--digest` binds submission to the exact prepared intent. If anything changes, prepare again. There is no per-order web approval. This is **trusted-host delegated mode** (see `security.md`).

## Leverage and isolated margin

```sh
oku positions leverage --market BTC --leverage N --mode cross|isolated [--intent-id ID] [--expires 5m]
oku positions margin   --market BTC --amount <USDC, +add / -remove, ≤6 decimals> [--intent-id ID] [--expires 5m]
oku orders submit --intent ID --digest SHA
```

Both commands only **prepare** an intent. Submission goes through `orders submit` like any other intent.

- Submission needs an active Oku session and the approved embedded Hyperliquid agent; risk changes can affect real funds.
- Leverage must be an integer from 1 to the market maximum. Isolated-only markets refuse cross mode.
- A leverage change and an order are two separate exchange actions, not one atomic trade. Only a server `completed` state counts as success.

## Draft repair (local only)

```sh
oku drafts repair --stdin < draft.json
```

This turns malformed order-draft JSON (code fences, single quotes, unquoted keys) into a strictly validated candidate. The output includes `repaired`, `candidate` and `prepare_args`.

- It does no networking, signing or journal writes, and it never prepares or submits.
- Limits: 16 KiB input, nesting depth 4, one flat object.
- It rejects duplicate keys, unknown fields, invalid decimals and missing required financial fields. It never fills in missing market, side, size, price or leverage.
- Review the candidate, then run `orders prepare` yourself. The resulting digest is what gets approved.

## Agent skills

Eight task skills are bundled:

- `learn-oku`
- `ensure-hyperliquid-authentication`
- `inspect-hyperliquid-account`
- `open-hyperliquid-position`
- `manage-hyperliquid-orders`
- `close-hyperliquid-position`
- `manage-hyperliquid-risk`
- `reconcile-hyperliquid-actions`

Their source is the repository's `skills/` directory.

- **Install locations:** jcode `~/.jcode/skills`, Claude Code `~/.claude/skills`, Codex `~/.agents/skills`, or any `--dir`.
- `install` never overwrites files that differ unless you pass `--force`. `uninstall` only removes files that match the bundled version unless you pass `--force`.
- `npx skills add https://github.com/oku-trade/oku-cli/tree/master` needs Git access to the private repository and selects the default `master` branch. `oku skills install` works from the binary alone.

## MCP server

```sh
oku mcp serve                     # read tools only
oku mcp serve --allow-mutations   # also prepare/submit/cancel/reconcile/abandon and positions leverage/margin
```

The server runs over stdio. Stdout carries only MCP protocol frames, and logs go to stderr. `--json` is refused.

- **Tools:** read-only by default. Mutation tools are exposed only with `--allow-mutations`. Login, logout and profile edits are never exposed.
- **Resources:** bundled docs and skills are available as `oku://docs/...` and `oku://skills/...`. There is also a local `drafts_repair` tool.
- **Profile:** `oku --profile NAME mcp serve` pins the server to that profile. Tools use it, and a tool call naming a different profile is refused. Start a separate server for another profile. Without a launch profile, tools use a per-call `profile`, then `$OKU_PROFILE`, then the config default.
- **Login:** the server never prompts. Run `oku auth login` in a terminal first. On Linux, unlocking needs `OKU_PASSPHRASE_FILE`. Otherwise tools return an auth error.
- **Approval:** a digest proves payload integrity, not human approval. Clients must get explicit user approval before calling `orders_submit`. Unknown outcomes follow the same reconcile-never-resend rules as the CLI.

## Local journal

Every intent's state is kept in `journal.sqlite` under the state directory (`oku config path`). Unknown outcomes survive restarts and block new exposure for the same account across all profiles and API origins until reconciled.

## Order states

HTTP success is not a fill. On a timeout or connection loss after submit, the CLI exits 6 with `OUTCOME_UNKNOWN`. Use `oku orders status --intent ID` or `oku orders reconcile`. **Never place a replacement until the original is resolved.**
