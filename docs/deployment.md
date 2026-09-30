# Deployment prerequisites

Nothing here is deployed by this repository. Before the CLI is usable end to end, the following must be in place.

## Backend (oku-trade/account)

- `PerpsService` reachable at the configured endpoint (default `https://accounts.lat.gfx.town/connect`), with `CreateAction`, `ConfirmAction`, `GetAction`, `ReconcileActions`, `GetOrderStatuses` and the read RPCs.
- **Device enrollment endpoints** (`CliDeviceService`): implemented, migration 137, **not deployed**. Needs short-lived single-use codes, pending-device binding to a generated key, expiry, rate limits, and device listing/revocation.
- **`risk` device scope** (leverage, margin mode, isolated margin): in progress on the backend feature branch `feat/cli-device-enrollment` (proto commit `1d5f44133` plus uncommitted scope-interceptor work). It is **not merged or deployed**. Until it is, `oku positions leverage` / `positions margin` intents can be prepared locally but cannot be submitted against a deployed backend. Existing `trade` tokens are intentionally not widened to cover risk actions.
- **Authenticated order origin/device attribution** (migration 138 on the same branch): **not applied or deployed**. The Oku UI cannot yet reliably distinguish CLI or MCP orders by authenticated device.
- Idempotent `ConfirmAction` across retries and concurrent clients, with unresolved actions visible server-side.

## Frontend

- The `/cli` page is implemented but not deployed. It covers the connected account/network, a risk explanation, the enrollment code, agent approval, a device list with revocation, and wording that separates token revocation from agent-key revocation. An explicit `risk` scope opt-in is on the trade feature branch and is not deployed.

## Release

- There is **no published npm package or public release channel**. The repository is private. `.github/workflows/release.yml` is manual only and disabled unless the repository variable `NPM_PUBLISH_ENABLED` is `true`. Even then it defaults to `--dry-run`, requires the `npm-release` environment, and publishes with `--access restricted`.
- Package names (`@oku-trade/cli` and `@oku-trade/cli-{linux,darwin}-{x64,arm64}`), scope ownership, visibility and license (`UNLICENSED`) are pending owner decisions.
- npm does **not** generate provenance for private source repositories. None is claimed.
- macOS binaries carry only an ad-hoc code signature. Developer ID signing and notarization are a separate, undecided release step. See [releasing](releasing.md).

## Migration runbook (human-operated, documentation only)

Agents and scripts in this repository must **not** run migrations or change services or databases. A human operator performs every step below. This repository executes none of them.

1. Read-only pre-checks: confirm the target environment, the current schema version, and backups. Do not write anything.
2. Review the backend migrations in the account repo: 137 (devices) and 138 (device origin/attribution, once finalized). Take exact commands from the account repo. Do not guess them.
3. The operator applies the migrations and deploys the backend, then the frontend `/cli`.
4. Read-only post-checks: the `GetMarkets` read probe (see `validation.md`), then an enrollment smoke test by a human.
5. Prepare the rollback plan before step 3.

## Enrollment RPCs (implemented in backend, not deployed)

- `VerifyEnrollment` carries `agentAddress` and the human-entered `confirmationCode`.
- The `ApproveEnrollment` request is only `{enrollmentId}`.
- The device routes are **not deployed**. Backend DB tests and the migrations have intentionally **not** been run against shared databases, because services and databases are operator-only.

**Status: not production-ready.**

## Operator-run integration acceptance (required before any release)

A human operator runs these in a non-production environment and records the results in `validation.md`:

1. Apply migrations 137/138 (operator only) and run the backend DB/integration tests.
2. Deploy the backend device routes and the frontend `/cli` to staging.
3. `oku auth setup`: enter the code in the CLI, and confirm the browser shows `VerifyEnrollment` succeeding before any HL delegation prompt.
4. Read-only device: confirm that no HL agent delegation is offered.
5. Trading device: `ApproveEnrollment`, then `oku auth status --json` reports verified with the expected scopes.
6. Minimal order on a test account: prepare → submit → visible in the Oku UI → cancel in the UI → the CLI sees the cancel.
7. Risk device (only with an explicit `risk` opt-in): prepare a leverage change, confirm that a `trade`-only device is refused, then submit only with human approval.
8. On macOS, native builds only: enroll into the Keychain. Separately, run `oku auth migrate-keychain` on a disposable legacy credential.
9. Revoke the Oku token and separately revoke the HL agent. Confirm the CLI fails with exit 3 and the agent can no longer sign.
