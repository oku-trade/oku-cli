# Install oku

Choose one method. Run `oku version` to check your installation. Package-manager installations must be upgraded with that package manager, not `oku self-update`.

## Homebrew

```sh
brew install oku-trade/tap/oku
# Or tap first: brew tap oku-trade/tap && brew install oku
brew update && brew upgrade oku
brew uninstall oku
brew untap oku-trade/tap # optional
```

Homebrew installs Bash, Zsh and Fish completions. Use the install script to pin an older version.

## npm

```sh
npm install -g @gfxlabs/oku-cli
npx @gfxlabs/oku-cli version # use without a global install
npm update -g @gfxlabs/oku-cli
npm uninstall -g @gfxlabs/oku-cli
```

The right native binary for your OS and CPU is installed automatically, with no install scripts. For `npx`, use the desired version in the package specifier to change versions.

## AUR (Arch Linux)

```sh
yay -S oku-bin
yay -Syu
yay -R oku-bin
```

The last command removes the package. AUR installs shell completions.

## Debian/Ubuntu and Fedora/RHEL

Each release has amd64 and arm64 packages. Use these verified commands to install or upgrade:

```sh
V=$(curl -fsSLo /dev/null -w '%{url_effective}' https://github.com/oku-trade/oku-cli/releases/latest); V=${V##*/v}

# Debian/Ubuntu (amd64; use linux_arm64 on arm64)
curl -fsSLO "https://github.com/oku-trade/oku-cli/releases/download/v$V/oku_${V}_linux_amd64.deb"
sudo apt install "./oku_${V}_linux_amd64.deb"

# Fedora/RHEL (x86_64; use aarch64 on arm64)
sudo dnf install "https://github.com/oku-trade/oku-cli/releases/download/v$V/oku-$V-1.x86_64.rpm"
```

There is no apt or dnf repository. Check the release's `SHA256SUMS` if you download a package manually. These packages include shell completions. Remove with `sudo apt remove oku` or `sudo dnf remove oku`.

## Install script

Installs the latest release in `~/.local/bin`, checking `SHA256SUMS` without sudo:

```sh
curl -fsSLO https://raw.githubusercontent.com/oku-trade/oku-cli/master/scripts/install.sh
sh install.sh # options: --version v0.1.0 --dir ~/bin
```

Run the script again to upgrade. Remove `~/.local/bin/oku` to uninstall, or remove the binary from your chosen `--dir`.

### Manual tarball

Choose the archive for your platform (`darwin_arm64` below; also `darwin_amd64`, `linux_amd64`, `linux_arm64`):

```sh
V=$(curl -fsSL https://api.github.com/repos/oku-trade/oku-cli/releases/latest | awk -F '"' '/"tag_name"/ {print $4; exit}')
curl -fsSLO "https://github.com/oku-trade/oku-cli/releases/download/${V}/oku_${V}_darwin_arm64.tar.gz"
curl -fsSLO "https://github.com/oku-trade/oku-cli/releases/download/${V}/SHA256SUMS"
shasum -a 256 -c --ignore-missing SHA256SUMS
tar -xzf "oku_${V}_darwin_arm64.tar.gz"
mkdir -p ~/.local/bin && mv "oku_${V}_darwin_arm64/oku" ~/.local/bin/oku
oku version
```

On Linux use `sha256sum -c --ignore-missing SHA256SUMS`. Repeat with a new release to upgrade. Remove `~/.local/bin/oku` to uninstall. The checksum manifest comes from the same release and is not an independent signature.

### Self-update and auto-update

For a standalone release binary:

```sh
oku self-update --check
oku self-update
oku self-update --version v0.1.0 # choose a specific release
oku autoupdate enable --interval 24h # optional, off by default
oku autoupdate status
oku autoupdate disable
```

Auto-update checks in the background at the configured interval (minimum 1h). `OKU_NO_AUTO_UPDATE=1` skips checks for a shell or CI job. For install-script installs, auto-update also refreshes unedited skills in the standard agent directories unless enabled with `--no-skills`. See [agent setup](agents.md#refreshing-skills). Self-update refuses package-manager installs and unversioned source builds. Use the original installation method to upgrade them.

## mise

```sh
mise use -g github:oku-trade/oku-cli
mise use -g github:oku-trade/oku-cli@0.0.1 # pin a version, without v
mise up github:oku-trade/oku-cli
mise unuse -g github:oku-trade/oku-cli
```

To pin in one project, omit `-g`. `mise ls-remote github:oku-trade/oku-cli` lists versions. mise may hide very recent releases under its `minimum_release_age`; use an explicit version if needed.

## Nix

Requires Nix with flakes enabled. Clone the repository, then:

```sh
nix run . -- version
nix shell . --command oku version
nix build . # ./result/bin/oku
nix profile add .#oku
nix profile upgrade oku
nix profile remove oku
```

Older Nix versions use `nix profile install .#oku`. Check `nix profile list` for the installed entry. To update a source flake, pull changes before rebuilding or upgrading. You can also run `nix run github:oku-trade/oku-cli/master -- version` without cloning.

## Go

Requires the Go version in `go.mod` (currently 1.27.1 or newer). macOS builds require CGO on a native macOS host.

```sh
go install github.com/oku-trade/oku-cli/cmd/oku@latest
"$(go env GOPATH)/bin/oku" version
```

Rerun `go install` to upgrade. Remove the binary from `GOBIN`, or `$(go env GOPATH)/bin/oku` when `GOBIN` is unset, to uninstall. Leave auto-update off for source builds.

## From source

```sh
git clone https://github.com/oku-trade/oku-cli.git && cd oku-cli
make build # bin/oku
./bin/oku docs list
make install # $HOME/.local/bin/oku
make uninstall
```

Pull changes and rebuild to upgrade. Linux builds with `CGO_ENABLED=0`. macOS needs CGO and a native host for Keychain support.

## Shell completion

Homebrew, AUR, deb and rpm install Bash, Zsh and Fish completions. For other installations, generate scripts with `oku completion bash`, `oku completion zsh` or `oku completion fish`. For the current shell:

```sh
source <(oku completion bash)
# Zsh: autoload -Uz compinit && compinit; source <(oku completion zsh)
# Fish: oku completion fish | source
```

Save the generated script and source it in your shell configuration for persistent completion.

## macOS signing

Release archives may be Developer ID signed and notarized or ad-hoc signed, depending on release configuration. Check the artifact before assuming its signing status. If macOS blocks a download, verify its source and checksum first.

## Before uninstalling

Run `oku skills uninstall --target claude` (or your chosen target) and `oku auth logout` if you want to remove skills and revoke the session. Removing the binary does not revoke the exchange delegated signer or remove the local journal. See [security](security.md).
