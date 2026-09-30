# Validation log

Record only what was actually executed. Nothing here verifies the new CLI binary unless stated.

## 2026-09-30: deployed read contract (reported by root agent)

- Request: unauthenticated `POST https://accounts.lat.gfx.town/connect/gfxcafe.oku.account.v2.PerpsService/GetMarkets`
  - Headers: `Content-Type: application/json`, `Connect-Protocol-Version: 1`
  - Body: `{"coin":"BTC"}`
- Result: HTTP 200, `markets[0].coin == "BTC"`, price and funding fields returned as precise decimal strings.
- Scope: verifies the deployed **read** contract only. It does not verify the new CLI, authentication, enrollment, or any write path.

## Not yet verified

- Browser/website flow: native Firefox bridge was unavailable, and the extension repair prompt timed out. A local headless browser may be tried later.
- Writes (`CreateAction`/`ConfirmAction`): no live writes authorized or performed.
- Device enrollment and `/cli` page: in development.
- Real `make dist` packaging of the CLI: pending until `./cmd/oku` builds. Script mechanics were verified on a stand-in module (4 archives, checksums verified and identical across two runs).

## Backend enrollment (reported by root agent)

- Only pure tests and builds have run. DB tests and the migration were **not** run (operator-only). The device routes are not deployed.
- The operator-run acceptance steps are in `deployment.md`. Not production-ready.
