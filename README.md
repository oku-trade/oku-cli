# Oku Agent (`oku`)

Swap and bridge across 30+ chains and trade perps on Oku, from your terminal or your AI agent.

- **Swap** tokens across 30+ chains with quotes from multiple routes.
- **Bridge** between supported EVM chains.
- **Trade perps** on Hyperliquid with orders, positions and risk controls.
- **Work with agents** using JSON output, offline docs, skills and MCP. Review every prepared action before submitting.

**Status:** Public alpha. Commands may change.

## Install

```sh
brew install oku-trade/tap/oku    # Homebrew
npm install -g @gfxlabs/oku-cli   # npm, or: npx @gfxlabs/oku-cli
yay -S oku-bin                    # Arch Linux AUR
```

For Debian/Ubuntu, Fedora/RHEL, the install script, Go, mise, Nix or source builds, see [all install methods](docs/install.md).

## Quick start

```sh
oku auth login
oku account summary
oku wallet balances --chain 42161
oku orders prepare --market BTC --side buy --type market --size 0.0002
# Review the returned account, market, side, size and digest against your request.
oku orders submit --intent ID --digest SHA
oku orders status --intent ID
```

Use your own trusted RPC via `OKU_RPC_URL_42161` for wallet transactions. See [usage](docs/usage.md) for agent-wallet funds, JSON input and unknown outcomes.

## Use with your AI agent

```sh
oku skills install --target claude # or codex, jcode, or --dir PATH
claude mcp add oku -- oku mcp serve
```

For Codex, add this to `~/.codex/config.toml`:

```toml
[mcp_servers.oku]
command = "oku"
args = ["mcp", "serve"]
```

MCP is read-only by default. To expose mutation tools, start the server with `oku mcp serve --allow-mutations`. See [agent setup](docs/agents.md) for skills, permissions and other clients.

## Docs

- [Install](docs/install.md): methods, upgrades, uninstall and completions.
- [Agents](docs/agents.md): skills, MCP and authorization.
- [Usage](docs/usage.md): workflows, JSON, funds and recovery.
- [Commands](docs/commands.md): command and flag reference.
- [Security](docs/security.md): credentials, signing and revocation.
- [Telemetry](docs/telemetry.md): optional usage telemetry.
- [Architecture](docs/architecture.md): CLI design.

Read these offline with `oku docs list` and `oku docs show <name>`.

## License

AGPL-3.0-only. See [LICENSE](LICENSE).
