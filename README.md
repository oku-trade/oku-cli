# oku

`oku` is a small Go command-line client for trading Hyperliquid perps through the Oku account service (`gfxcafe.oku.account.v2.PerpsService`). It is built for people and coding agents: human-readable tables by default, a versioned JSON contract (`oku.cli/v1`) with `--json`.

Supports Hyperliquid **mainnet** only.

> **Status: pre-release / private alpha development.** There are no public releases, no hosted install script, and no auto-update. Device enrollment (backend) and the Oku website `/cli` page are implemented but not deployed. Live enrollment and trading are untested (see docs/validation.md). Command grammar may change.

## Install (from source)

Requires Go (version in `go.mod`).

```sh
git clone <this repository> && cd oku-cli
go build -trimpath -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o bin/oku ./cmd/oku
# check: ./bin/oku version
# or: make build | make install   (installs to $HOME/.local/bin/oku)
# or: go install -ldflags "-X main.version=<ver>" github.com/oku-trade/oku-cli/cmd/oku@<commit>   (only works once the repo is reachable by the Go toolchain)
```

Uninstall: `oku skills uninstall --target <agent>` for any installed skills, `oku auth revoke`, then `make uninstall` (or `rm "$(command -v oku)"`). Also revoke the Hyperliquid agent from your main wallet. Removing the binary does **not** revoke Oku sessions or the exchange delegated signer. See [docs/security.md](docs/security.md).

Local release archives: `make dist` builds linux/darwin × amd64/arm64 into `dist/` with `SHA256SUMS`. See [docs/releasing.md](docs/releasing.md).

## Docs

- [docs/usage.md](docs/usage.md): commands, JSON contract, order lifecycle
- [docs/commands.md](docs/commands.md): generated command and flag reference (`make docs`, `make docs-check`)
- [docs/architecture.md](docs/architecture.md): how the CLI talks to Oku
- [docs/security.md](docs/security.md): signing modes, key risks, revocation
- [docs/deployment.md](docs/deployment.md): backend/frontend prerequisites
- [docs/releasing.md](docs/releasing.md): local builds and checksum verification
- [docs/validation.md](docs/validation.md): what has actually been verified
- Agent skill: [pkg/skills/assets/oku-perps/SKILL.md](pkg/skills/assets/oku-perps/SKILL.md)
