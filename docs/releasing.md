# Releasing oku

This is a maintainer guide. Users can install published binaries from the public GitHub Releases page or use `scripts/install.sh`, which verifies `SHA256SUMS`.

1. Run `make test` and check that `make docs-check` passes. Review the release workflow and its required permissions before tagging.
2. Tag a version following the existing release workflow conventions. The GitHub release workflow builds Linux and macOS amd64/arm64 archives named `oku_<tag>_<os>_<arch>.tar.gz`, and publishes `SHA256SUMS`. Verify the tag, assets and checksums before announcing the release.
3. Test installation on supported platforms with `scripts/install.sh --version <tag>` and `oku version`. The installer and `oku self-update` use public release downloads without a token by default.
4. npm publishing is a separate, manual decision. Check ownership, package metadata, access and licensing before publishing. Do not assume a GitHub release publishes npm packages.

The macOS build uses Developer ID signing when the workflow is configured with a signing identity, otherwise it is ad-hoc signed. Notarization runs only when the notary configuration is also available. Check the workflow run and resulting artifact before making a signing or notarization claim for a particular release. Ad-hoc signing is not notarization.
