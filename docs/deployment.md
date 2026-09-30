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
