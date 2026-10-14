# Deployment prerequisites

**Oku Agent Privy authorization is not confirmed production-ready.** This repository does not deploy the frontend, Accounts service or Privy app configuration. Operators must validate all of these before releasing trading access.

## Backend and Privy

1. Deploy Accounts `AuthService/PrivySessionExchange`, `ExtendSession`, `Logout` and `PerpsService/ValidateAgentKey`, plus the normal Perps action RPCs.
2. The dedicated Privy app **Oku Trade Agents** has ID `cmv72u7fa001m0gk200vq97nd`. Configure device authorization to send users to `https://oku.trade/agent/authorize`. The CLI defaults to that app ID; profile `privy_app_id` and `OKU_PRIVY_APP_ID` override it. `OKU_PRIVY_AUTH_URL` is for local test servers.
3. The backend must accept this app's device-grant JWT. The Accounts branch `feat/privy-multi-app-verifier` is required and its deployment and live device-JWT exchange remain **unverified**.
4. The Privy OAuth wallet RPC must allow `eth_signTypedData_v4` for Hyperliquid Agent typed data, including chainId 1337. This is **unverified live**. Apply a grant policy that limits the wallet to that domain if available.
5. Backend agent-session lifetime and attribution, and recovery-to-approved-agent validation must be reviewed. A generic user session is not an equivalent substitute for a scoped device session.

## Web onboarding

The page `/agent/authorize` must: (1) complete the Privy device authorization for the shown user code, (2) authenticate the user's **external** master wallet, (3) obtain/create a separate embedded Ethereum agent wallet in the dedicated Privy app, (4) have the master sign Hyperliquid named-agent approval for the embedded address and builder-fee approval where required, and (5) expose an understandable route to revoke both the device grant and, separately, the Hyperliquid agent. Do not use the retail embedded wallet as the account master. Named-agent slots `oku1`–`oku3` can conflict with existing web approvals. The CLI does not implement this page.

## Operator acceptance

Use disposable accounts and staging, not funded production wallets. Verify pending-to-approved login, refusal before `ValidateAgentKey` reports approved, refresh-token rotation across two processes, signed typed-data recovery against the approved agent, a failed Privy signature returning the journal record to `created`, and separate grant/session/HL-agent revocation. Confirm a minimal submitted order appears in Oku and Hyperliquid before releasing trading access. Record actual deployment SHA and evidence in `validation.md`; local mock tests alone do not close these live blockers.

## npm release

`@oku-trade/cli` and the platform package names remain unchanged pending owner decisions on scope ownership, visibility and licensing. No package is published by this repository automatically. See [releasing](releasing.md).
