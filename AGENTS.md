# Agents

To install or use `oku`, read [docs/agents.md](docs/agents.md) first, before you install or authenticate. Bootstrap checklist:

1. Install with a method from [docs/install.md](docs/install.md). For a manual tarball, list the archive with `tar -tzf` before extracting, then verify it against `SHA256SUMS`.
2. Read `oku auth login --help` before logging in. Login needs an interactive terminal (it refuses to run through MCP or a non-TTY stdin) and accepts a `--timeout` of at most 15 minutes.
3. Do not create a profile first. The first `oku auth login` creates it.
4. If the user is remote, run `oku auth login --no-open` and send them the printed verification URL.
5. Install the bundled skills into your skill directory: `oku skills install --target claude|codex|jcode`, or `--dir PATH`.
6. Confirm with `oku auth status --verify`.

To develop this repository, run `make test` before committing.
