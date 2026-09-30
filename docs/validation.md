# Validation

Only what was actually executed is recorded here. **Status as of 2026-09-30: not production-ready.** Live enrollment, signing and trading have not been tested.

## Current status

| Area | Latest evidence | Status |
|---|---|---|
| CLI | Full `-race` + vet passed (root `BG722774rqmc`) **before** the latest UNKNOWN/abandon fixes. Live read smoke passed (`BG663998t7rz`). | **Not final.** Rerun of tests and packaging at the final commit is pending. |
| CLI packaging | `scripts/build.sh` reproducible at `ca84dac` (below). | Must be rerun at the final commit. |
| Frontend | HEAD `bbfb56930`: full suite 252 files / 3208 tests pass, `lint:check` exit 0 (`BG683551au6j`). Vite build exit 0. `/cli` renders in Chromium on desktop and mobile. | Passing. Wallet-connected flow untested. |
| Backend | `405b4538a`: tests, vet and build pass (`BG692697b2l7`). `go test -race` passes for `./src/connectapi`, `./src/connectapi/clidevice`, `./src/modules/features/clidevice` and `./src/pkg/hlsigning` (`BG8948497p4q`, no DB exercised). Integration tests were compiled only. Two review fixes verified. | Passing pure checks. Migration 137 is **not applied**. Device routes are **not deployed**. |
| Deployed read API | `GetMarkets` over Connect JSON returns HTTP 200 with BTC data. | Verified. |
| End-to-end enrollment/trading | Not run. Operator-only (see `deployment.md` acceptance checklist). | **Blocked on operator.** |

## Kinds of evidence

| Kind | What it covers | What it does not cover |
|---|---|---|
| **Synthetic** | CLI unit and safety tests (race, vet) against HTTP fixtures and fakes: unknown-outcome handling, the abandon limit, reconcile, digests, skills install. Backend pure unit tests. | Any real server behavior. |
| **Real, read-only** | Live `GetMarkets` and live CLI `markets` reads against the deployed Oku API. Local `orders prepare`, which sends nothing. | Authenticated calls or writes. |
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
- Fixes made after the `-race` run: a `submitted` status without `hlOid` is treated as `OUTCOME_UNKNOWN`. `orders abandon` is limited to intents that were never signed. `reconcile` can no longer let an UNKNOWN intent escape through the 2-minute grace period.

### Packaging (`ca84dac`, to be redone at the final commit)

`VERSION=v0.0.0-local ./scripts/build.sh` was run twice. Both runs produced identical `SHA256SUMS` (Go 1.27.1, linux host) and `sha256sum -c` passed. Nothing was published.

- darwin_amd64 `3609380edc5885a9731a6f63728c87be60c6a735dc4981f4e13a9be2d78c3556`
- darwin_arm64 `ac67a77868cd5779ca38138cd7ae84a7a8b3a0ecc8e6df13105423e7e3c41d57`
- linux_amd64 `65004691af9ffd7fd06ca9621480601901b79f6342844499c20cea97575f901d`
- linux_arm64 `43580905615407f9c31fc3a0412e9e17038405eb883370d1e6c99c54033370c1`

The linux/amd64 binary runs. The darwin binaries were not run (no macOS host) and are unsigned.

### Frontend

- Targeted vitest (26 tests), lint, and `vite build` with PostHog uploads disabled all passed at `e4c7845d1`. Superseded by the full suite at `bbfb56930`.
- Local Playwright Chromium against the Vite app at `/cli`:
  - HTTP 200, title `Oku CLI and Agents | Oku Trade`, heading and "Connect wallet" visible
  - enrollment controls hidden without a wallet, zero page errors
  - mobile 390x844 has no horizontal overflow

  No wallet was connected and there were no mocks.

### Backend

- Enrollment: `VerifyEnrollment` (agentAddress + confirmationCode) runs before HL delegation. `ApproveEnrollment` takes `{enrollmentId}`. Read-only devices never get a delegated signer.
- Trading-envelope signer binding is implemented.
- DB tests and migration 137 were intentionally not run (operator-only).

### Deployed read API

`POST https://accounts.lat.gfx.town/connect/gfxcafe.oku.account.v2.PerpsService/GetMarkets` with `Content-Type: application/json`, `Connect-Protocol-Version: 1`, body `{"coin":"BTC"}`. Returned HTTP 200, `markets[0].coin == "BTC"`, and decimal-string prices.
