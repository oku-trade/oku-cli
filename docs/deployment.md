# Deployment prerequisites

Nothing here is deployed by this repository. Before the CLI is usable end-to-end:

## Backend (oku-trade/account)

- `PerpsService` reachable at the configured endpoint (default `https://accounts.lat.gfx.town/connect`) with `CreateAction`, `ConfirmAction`, `GetAction`, `ReconcileActions`, `GetOrderStatuses` and read RPCs.
- Device enrollment endpoints: **in development, contract pending**. Needs short-lived single-use codes, pending-device binding to a generated key, expiry, rate limits, and device listing/revocation.
- Idempotent `ConfirmAction` across retries and concurrent clients. Unresolved actions visible server-side.
- CLI provenance recorded on actions so the web UI can show origin.

## Frontend

- `/cli` page (in development): connected account/network, risk explanation, enrollment code, agent approval, device list with revocation, and wording that separates token revocation from agent-key revocation.

## Release

- No public release channel exists yet. Signed/notarized macOS binaries and a hosted distribution are future work.

## Migration runbook (human-operated, documentation only)

Agents and scripts in this repository must **not** run migrations or change services or databases. A human operator performs every step below. This repository executes none of them.

1. Read-only pre-checks: confirm the target environment, current schema version, and backups. Do not write anything.
2. Review the backend enrollment migration in the account repo (exact commands will be added once the backend contract is final. Do not guess them).
3. The operator applies the migration and deploys backend, then frontend `/cli`.
4. Read-only post-checks: `GetMarkets` read probe (see `validation.md`), then enrollment smoke test by a human.
5. Rollback plan prepared before step 3.
