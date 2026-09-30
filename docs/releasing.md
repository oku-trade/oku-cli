# Local releases

```sh
make dist                  # or: VERSION=v0.1.0 ./scripts/build.sh
./scripts/checksums.sh dist
```

Produces `dist/oku_<version>_<os>_<arch>.tar.gz` for linux/darwin × amd64/arm64 and `dist/SHA256SUMS`. Builds use `CGO_ENABLED=0 -trimpath -buildvcs=false`, stripped build id, and deterministic tar/gzip metadata keyed to `SOURCE_DATE_EPOCH` (last commit time), so rebuilding the same commit with the same Go version should yield identical checksums.

Verify: `cd dist && sha256sum -c SHA256SUMS`.

Caveats: checksums only prove integrity against the `SHA256SUMS` file you trust. There is no signing, notarization (macOS Gatekeeper will quarantine downloaded binaries), publishing, or auto-update. Do not distribute via `curl | sh`.

## Verified run

At commit `eaad4ba` the script was run twice, with identical checksums. See `validation.md` for the hashes.
