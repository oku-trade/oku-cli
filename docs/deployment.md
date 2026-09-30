# Deployment prerequisites

Nothing here is deployed by this repository. Before the CLI is usable end-to-end:

## Backend (oku-trade/account)

- `PerpsService` reachable at the configured endpoint (default `https://accounts.lat.gfx.town/connect`) with `CreateAction`, `ConfirmAction`, `GetAction`, `ReconcileActions`, `GetOrderStatuses` and read RPCs.
- Device enrollment endpoints: implemented in backend `405b4538a`, **not deployed**. Needs short-lived single-use codes, pending-device binding to a generated key, expiry, rate limits, and device listing/revocation.
- Idempotent `ConfirmAction` across retries and concurrent clients. Unresolved actions visible server-side.
- CLI provenance recorded on actions so the web UI can show origin.

## Frontend

- `/cli` page (implemented at `bbfb56930`, not deployed): connected account/network, risk explanation, enrollment code, agent approval, device list with revocation, and wording that separates token revocation from agent-key revocation.

## Release

- No public release channel exists yet. Signed/notarized macOS binaries and a hosted distribution are future work.

## Migration runbook (human-operated, documentation only)

Agents and scripts in this repository must **not** run migrations or change services or databases. A human operator performs every step below. This repository executes none of them.

1. Read-only pre-checks: confirm the target environment, current schema version, and backups. Do not write anything.
2. Review the backend enrollment migration in the account repo (exact commands will be added once the backend contract is final. Do not guess them).
3. The operator applies the migration and deploys backend, then frontend `/cli`.
4. Read-only post-checks: `GetMarkets` read probe (see `validation.md`), then enrollment smoke test by a human.
5. Rollback plan prepared before step 3.

## Enrollment RPCs (implemented in backend, not deployed)

- `VerifyEnrollment` carries `agentAddress` and the human-entered `confirmationCode`.
- `ApproveEnrollment` request is only `{enrollmentId}`.
- The new device routes are **not deployed**. Backend DB tests and the migration have intentionally **not** been run, because services and databases are operator-only. Only pure unit tests and builds have run.

**Status: not production-ready.**

## Operator-run integration acceptance (required before any release)

A human operator runs these in a non-production environment and records results in `validation.md`:

1. Apply the enrollment migration (operator only) and run backend DB/integration tests.
2. Deploy backend device routes and frontend `/cli` to staging.
3. `oku auth setup`: enter the code in the CLI, confirm the browser shows `VerifyEnrollment` succeeding before any HL delegation prompt.
4. Read-only device: confirm no HL agent delegation is offered.
5. Trading device: `ApproveEnrollment`, then `oku auth status --json` reports verified.
6. Minimal order on a test account: prepare → submit → visible in Oku UI → cancel in UI → CLI sees cancel.
7. Revoke the Oku token and separately revoke the HL agent. Confirm the CLI fails with exit 3 and the agent can no longer sign.
