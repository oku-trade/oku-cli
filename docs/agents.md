# Use oku with your AI agent

Install `oku` first using the [install guide](install.md). Log in from your own terminal with `oku auth login`. Agents can then use bundled skills and an MCP server without a source checkout.

## Skills

Skills are task instructions for your agent. They explain commands, preview review, authorization and recovery. Install them for your client:

```sh
oku skills install --target claude
oku skills install --target codex
oku skills install --target jcode
oku skills install --dir PATH
oku skills uninstall --target claude # or codex, jcode, --dir PATH
```

Install locations are `~/.claude/skills` for Claude Code, `~/.agents/skills` for Codex and `~/.jcode/skills` for Jcode. `--dir PATH` uses your chosen skills directory. The 12 bundled skills are:

- `learn-oku`: find commands, schemas and offline docs.
- `ensure-hyperliquid-authentication`: log in and check agent authorization.
- `inspect-hyperliquid-account`: read balances, positions and account state.
- `open-hyperliquid-position`: prepare and submit a new position.
- `manage-hyperliquid-orders`: place, inspect and cancel orders.
- `close-hyperliquid-position`: prepare a position close.
- `manage-hyperliquid-risk`: adjust leverage and margin within authorized bounds.
- `move-agent-funds`: send, swap, bridge and deposit agent-wallet funds.
- `consolidate-agent-funds`: move agent balances to chosen chain and token targets.
- `coinbase-deposit-targets`: choose native USDC targets for Coinbase deposits.
- `reconcile-hyperliquid-actions`: resolve unknown outcomes without duplicate submissions.
- `troubleshoot-macos-keychain`: resolve credential access on macOS.

`install` does not overwrite edited skill files unless you pass `--force`. `uninstall` removes only files matching the bundled version unless you pass `--force`. Back up your edits before forcing either action.

### Refreshing skills

After upgrading `oku`, rerun `oku skills install --target claude --force` (or your target) if you have not edited the installed skills. If you have edited them, compare and merge your changes instead of forcing an overwrite. Auto-update refreshes unedited skills in the standard Claude, Codex and Jcode directories for install-script installs, unless you enabled it with `--no-skills`. Custom `--dir` installations need manual refresh.

## MCP server

MCP exposes read tools and bundled docs and skills as resources. For Claude Code:

```sh
claude mcp add oku -- oku mcp serve
```

For Codex, add the following to `~/.codex/config.toml`:

```toml
[mcp_servers.oku]
command = "oku"
args = ["mcp", "serve"]
```

The server runs over stdio. Use `oku mcp serve` for read-only tools, or `oku mcp serve --allow-mutations` to also expose prepare, submit, cancel, reconcile, abandon, leverage and margin tools. Change the final command or `args` in your client's MCP configuration to opt in. Login, logout and profile edits are never exposed. `--json` is not supported for the server.

Resources are available as `oku://docs/...` and `oku://skills/...`, including `oku://docs/usage`. The server also provides a local `drafts_repair` tool. Logs go to stderr, not MCP stdout.

Run `oku auth login` in a terminal first. The server never prompts. For a specific profile, launch `oku --profile NAME mcp serve`; a tool call specifying another profile is refused. Without a launch profile, calls use their specified profile, then `$OKU_PROFILE`, then the default. Start a separate server for another pinned profile. Older passphrase-protected credentials require `oku auth unlock` in your terminal.

### Authorization and recovery

A digest protects the prepared payload, but it is not user permission. A concrete request to trade, close, cancel, change risk or move funds authorizes a matching submit **after the agent reviews its preview** against the requested account, market or chain, side, size, recipient, price or slippage, and risk bounds. A defined standing instruction may authorize a bounded batch. Do not submit if the preview differs. Ask the user to confirm ambiguous, inferred, missing or mismatched values, or anything outside the authorized scope. A fresh preview for an expired or definitively rejected action still needs the same review and original bounds.

For an unknown broadcast outcome, check status or reconcile. Never resend or prepare a replacement until the outcome is resolved. See [usage](usage.md#order-states) and [security](security.md).
