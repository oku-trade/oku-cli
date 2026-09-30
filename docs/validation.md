# Validation log

Record only what was actually executed. Nothing here verifies the new CLI binary unless stated.

## 2026-09-30: deployed read contract (reported by root agent)

- Request: unauthenticated `POST https://accounts.lat.gfx.town/connect/gfxcafe.oku.account.v2.PerpsService/GetMarkets`
  - Headers: `Content-Type: application/json`, `Connect-Protocol-Version: 1`
  - Body: `{"coin":"BTC"}`
- Result: HTTP 200, `markets[0].coin == "BTC"`, price and funding fields returned as precise decimal strings.
- Scope: verifies the deployed **read** contract only. It does not verify the new CLI, authentication, enrollment, or any write path.

## Not yet verified

- Wallet-connected browser flow (enrollment, signing, trades): not tested.
- Backend device route deployment + DB, and authorized wallet end-to-end: not run.
- Writes (`CreateAction`/`ConfirmAction`): no live writes authorized or performed.
- Device enrollment and `/cli` page: in development.
- Real `make dist` packaging of the CLI: pending until `./cmd/oku` builds. Script mechanics were verified on a stand-in module (4 archives, checksums verified and identical across two runs).

## Backend enrollment (reported by root agent)

- Only pure tests and builds have run. DB tests and the migration were **not** run (operator-only). The device routes are not deployed.
- The operator-run acceptance steps are in `deployment.md`. Not production-ready.

## 2026-09-30: frontend `/cli` page, local UI check (reported by root agent)

- Setup: fresh local Chromium via Playwright against the real Vite dev app at `http://127.0.0.1:5177/cli`. This was a temporary local process, with no DB or managed-service changes and no mocks in the browser path.
- Results: HTTP 200. Title `Oku CLI and Agents | Oku Trade`. Heading visible. "Connect wallet" visible. Enrollment controls absent without a wallet. Zero `pageerror` events. At a mobile 390x844 viewport the heading is visible with no horizontal overflow.
- Screenshots: `scratch/oku-cli-validation/{desktop,mobile}.png` (local scratch, not committed).
- Scope: unauthenticated page render only. No wallet connected, and no enrollment, signing or trades tested. This replaces the earlier "Firefox bridge unavailable" blocker.
