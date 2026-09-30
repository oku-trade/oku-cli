# Validation

Only what was actually executed is recorded here. **Status as of 2026-09-30: not production-ready.** Live enrollment, signing and trading have not been tested.

## Current status

| Area | Latest evidence | Status |
|---|---|---|
| CLI | Final `eaad4ba`: `go test -race ./...`, `go vet ./...`, `make build`, `./bin/oku version --json` and live `markets quote BTC` all exit 0 (root `BG991879haiq`). Review source-cleared the submitted-without-oid UNKNOWN handling, never-signed-only abandon, and journal error propagation. | Passing synthetic + real read-only checks. Submit/trading not live-tested. |
| CLI packaging | `scripts/build.sh` at `eaad4ba`: 4 archives, reproducible (below). | Done locally. Not published or signed. |
| Frontend | HEAD `bbfb56930`: full suite 252 files / 3208 tests pass, `lint:check` exit 0 (`BG683551au6j`). Vite build exit 0. `/cli` renders in Chromium on desktop and mobile. | Passing. Wallet-connected flow untested. |
| Backend | `405b4538a`: tests, vet and build pass (`BG692697b2l7`). `go test -race` passes for `./src/connectapi`, `./src/connectapi/clidevice`, `./src/modules/features/clidevice` and `./src/pkg/hlsigning` (`BG8948497p4q`, no DB exercised). Integration tests were compiled only. Two review fixes verified. | Passing pure checks. Migration 137 is **not applied**. Device routes are **not deployed**. |
| Deployed read API | `GetMarkets` over Connect JSON returns HTTP 200 with BTC data. | Verified. |
| End-to-end enrollment/trading | Not run. Operator-only (see `deployment.md` acceptance checklist). | **Blocked on operator.** |

## Kinds of evidence

| Kind | What it covers | What it does not cover |
|---|---|---|
| **Synthetic** | CLI unit and safety tests (race, vet) against HTTP fixtures and fakes: unknown-outcome handling, the abandon limit, reconcile, digests, skills install. Backend pure unit tests. | Any real server behavior. |
| **Real, read-only** | Live `GetMarkets` and live CLI `markets` reads against the deployed Oku API. Local `orders prepare`, which performs read API calls and persists an intent but submits no exchange action. | Authenticated calls or writes. |
| **Real, unauthenticated UI** | The actual Vite app `/cli` in Chromium (a temporary local server, since stopped), with no wallet and no mocks. | Wallet connection. |
| **Not verified** | Wallet connection, enrollment (Verify/Approve), agent delegation, `orders submit`/cancel, revocation, deployed device routes, migration 137. | |

## Details

### CLI

- Live smoke (`BG663998t7rz`), all exit 0:
  - `version`
  - **live** `markets quote BTC`, `markets list --market BTC`, `markets funding BTC --since 1h`
  - scratch `profiles add` and `profiles list`
  - local `orders prepare`, which returned a valid digest and a warning that the builder fee is not approved
  - `skills install` and `skills uninstall` against a scratch target

  **No `orders submit`.**
- Docs worker at `ca84dac`: `--help` for every command was captured and the docs aligned to it. `version --json` returns an `oku.cli/v1` envelope. `orders prepare` with no profile returns `PROFILE_NOT_FOUND` and exits 2.
- Fixed in `eaad4ba` and covered by the final run: a `submitted` status without `hlOid` is treated as `OUTCOME_UNKNOWN`. `orders abandon` is limited to intents that were never signed. `orders abandon` can no longer clear an UNKNOWN intent after a 2-minute grace period.

### Packaging (final, `eaad4ba`)

`VERSION=eaad4ba ./scripts/build.sh` was run twice into separate directories. Both runs produced identical `SHA256SUMS` (Go 1.27.1, linux/amd64 host) and `sha256sum -c` passed. Nothing was published.

- darwin_amd64 `eaa7d27d7bc00803a6eb2ee6bbdec3c987785d00a13d9241fad2ffd0e7ebaefd`
- darwin_arm64 `2d8ae532ed4b94532ea31a3a41c9d2d8e2140d19d7b483dc456064c43c2bc57f`
- linux_amd64 `591e303e7269d335474f458c4c02ebc3466f381f0e85e83755326d33c1440acd`
- linux_arm64 `0a8ebe56de80bd90a06dfe39029eabddecd69f75ed8dbda762d327168052e91c`

The extracted linux/amd64 binary prints `oku eaad4ba (unknown, linux/amd64)`. The commit shows "unknown" because only `main.version` is injected. The darwin binaries were not run (no macOS host) and are unsigned. An earlier run at `ca84dac` is superseded.

### Frontend

- Targeted vitest (26 tests), lint, and `vite build` with PostHog uploads disabled all passed at `e4c7845d1`. Superseded by the full suite at `bbfb56930`.
- Local Playwright Chromium against the Vite app at `/cli`:
  - HTTP 200, title `Oku CLI and Agents | Oku Trade`, heading and "Connect wallet" visible
  - enrollment controls hidden without a wallet, zero page errors
  - mobile 390x844 has no horizontal overflow

  No wallet was connected and there were no mocks.

### Backend

- Enrollment: `VerifyEnrollment` (agentAddress + confirmationCode) runs before HL delegation. `ApproveEnrollment` takes `{enrollmentId}`. Read-only enrollment generates a local key for device proof but never requests Hyperliquid trading delegation.
- Trading-envelope signer binding is implemented.
- DB tests and migration 137 were intentionally not run (operator-only).

### Deployed read API

`POST https://accounts.lat.gfx.town/connect/gfxcafe.oku.account.v2.PerpsService/GetMarkets` with `Content-Type: application/json`, `Connect-Protocol-Version: 1`, body `{"coin":"BTC"}`. Returned HTTP 200, `markets[0].coin == "BTC"`, and decimal-string prices.
