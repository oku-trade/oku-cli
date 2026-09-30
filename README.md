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

## Install (Nix)

The repo is a flake, so Nix builds the pinned Go toolchain and dependencies for you. Requires Nix with flakes enabled (`--extra-experimental-features 'nix-command flakes'` if they are not on by default).

```sh
git clone <this repository> && cd oku-cli

# run without installing
nix run . -- version

# ephemeral shell with oku on $PATH
nix shell . --command oku version

# build a ./result symlink
nix build .            # ./result/bin/oku version

# install into your user profile
nix profile add .#oku  # then: oku version   (older Nix: nix profile install .#oku)
```

Development shell with Go, `gopls`, and `make`:

```sh
nix develop            # then: make build && go vet ./... && make docs-check
```

Uninstall the profile install with `nix profile remove oku`. Installing the named `.#oku` attribute (rather than plain `.`) is what makes the profile entry match the binary name; `nix profile list` shows the current entries. Once this repository is reachable by URL you can skip the clone and use `nix run github:oku-trade/oku-cli -- version` or add it as a flake input:

```nix
inputs.oku-cli.url = "github:oku-trade/oku-cli";
# then: environment.systemPackages = [ inputs.oku-cli.packages.${system}.default ];
```

Uninstall: `oku skills uninstall --target <agent>` for any installed skills, `oku auth revoke`, then `make uninstall` (or `rm "$(command -v oku)"`). Also revoke the Hyperliquid agent from your main wallet. Removing the binary does **not** revoke Oku sessions or the exchange delegated signer. See [docs/security.md](docs/security.md).

Local release archives: `make dist` builds linux/darwin × amd64/arm64 into `dist/` with `SHA256SUMS`. See [docs/releasing.md](docs/releasing.md).

## Docs

- [docs/usage.md](docs/usage.md): commands, JSON contract, order lifecycle
- [docs/local-testing.md](docs/local-testing.md): local API/UI read-only checks and operator-only enrollment/trade guidance
- [docs/commands.md](docs/commands.md): generated command and flag reference (`make docs`, `make docs-check`)
- [docs/architecture.md](docs/architecture.md): how the CLI talks to Oku
- [docs/security.md](docs/security.md): signing modes, key risks, revocation
- [docs/deployment.md](docs/deployment.md): backend/frontend prerequisites
- [docs/releasing.md](docs/releasing.md): local builds and checksum verification
- [docs/validation.md](docs/validation.md): what has actually been verified
- Agent skill: [pkg/skills/assets/oku-perps/SKILL.md](pkg/skills/assets/oku-perps/SKILL.md)
