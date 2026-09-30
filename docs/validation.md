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

## Backend enrollment (reported by root agent)

- Only pure tests and builds have run. DB tests and the migration were **not** run (operator-only). The device routes are not deployed.
- The operator-run acceptance steps are in `deployment.md`. Not production-ready.

## 2026-09-30: frontend `/cli` page, local UI check (reported by root agent)

- Setup: fresh local Chromium via Playwright against the real Vite dev app at `http://127.0.0.1:5177/cli`. This was a temporary local process, with no DB or managed-service changes and no mocks in the browser path.
- Results: HTTP 200. Title `Oku CLI and Agents | Oku Trade`. Heading visible. "Connect wallet" visible. Enrollment controls absent without a wallet. Zero `pageerror` events. At a mobile 390x844 viewport the heading is visible with no horizontal overflow.
- Screenshots: `scratch/oku-cli-validation/{desktop,mobile}.png` (local scratch, not committed).
- Scope: unauthenticated page render only. No wallet connected, and no enrollment, signing or trades tested. This replaces the earlier "Firefox bridge unavailable" blocker.

## 2026-09-30: frontend commit `e4c7845d1`, targeted checks (root agent)

- `corepack yarn vitest run src/app/features/cli src/app/features/perps/state/approvals.test.ts src/app/lib/posthogNetworkMask.test.ts`: 4 files, 26 tests pass.
- `corepack yarn lint:check`: exit 0 (one unrelated hooks warning).
- `POSTHOG_PERSONAL_API_KEY= POSTHOG_PROJECT_ID= corepack yarn vite build --outDir <scratch>/ui-dist`: exit 0 in about 10s, external uploads disabled.
- **Not claimed:** the full frontend test suite is not green. 16 other failures were reported elsewhere in the suite.

## CLI (status at this time)

- All 7 package tests passed with `-race`, as reported by the CLI worker.
- Review fixes are still pending. The final real `make dist` packaging run and the CLI smoke test have **not** been done yet.

## Backend: commits `6b4bcbd5f` and L1 recovery `5eaed3ac4` (reported)

- Pure tests, build and vet pass. Migration 137 exists as an artifact but has **not** been applied (operator-only).
- The scoped trading-envelope signer binding is implemented (it is no longer deferred or fail-closed). Independent review is in progress.
- **Not claimed:** live enrollment and live trading are untested.

## Frontend full suite

- 16 test-only fixture failures are being repaired in a separate commit. Final totals are pending, and none are recorded here yet.

## CLI packaging and help check (docs worker, commit `ca84dac`)

- `go build ./cmd/oku` OK. `oku --help` and per-command `--help` were captured, and `docs/usage.md` and the skill were aligned to them. Codex skill path is `~/.agents/skills` (from `internal/skills/skills.go`).
- `oku version --json` returns an `oku.cli/v1` envelope. `orders prepare` with no profile returns `PROFILE_NOT_FOUND` and exits 2.
- `scripts/build.sh` ran twice: 4 archives (linux/darwin, amd64/arm64), `sha256sum -c` OK, identical checksums across runs. Local only, nothing published.
  - darwin_amd64 `3609380edc5885a9731a6f63728c87be60c6a735dc4981f4e13a9be2d78c3556`
  - darwin_arm64 `ac67a77868cd5779ca38138cd7ae84a7a8b3a0ecc8e6df13105423e7e3c41d57`
  - linux_amd64 `65004691af9ffd7fd06ca9621480601901b79f6342844499c20cea97575f901d`
  - linux_arm64 `43580905615407f9c31fc3a0412e9e17038405eb883370d1e6c99c54033370c1`
- The darwin binaries were not executed (no macOS host), and they are unsigned and not notarized.
- Not done here: authenticated or live CLI commands, e2e tests (in progress by the CLI worker).
