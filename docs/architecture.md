# Architecture

```
agent / human ──> oku (Go binary) ──Connect RPC──> Oku account service (PerpsService) ──> Hyperliquid
                    │                                   │
                    └─ local delegated signer key       └─ shared action history (web UI + CLI)
```

- Module `github.com/oku-trade/oku-cli`, entry point `./cmd/oku`, version injected via `-X main.version=...`.
- Default endpoint: `https://accounts.staging.invalid/connect` (Connect protocol). Override per the CLI's endpoint flag/config.
- The binary is intentionally thin. Fees, risk rules and action construction live in the backend, not duplicated locally.

## Order flow

1. `oku orders prepare` is local only (intent_id + digest). On `submit --intent --digest`, `CreateAction`: backend builds the exchange action payload for an explicit account and network (prepare).
2. The CLI signs the payload locally with the delegated (agent) signer key. The key never leaves the host.
3. `ConfirmAction`: signed action submitted through Oku.
4. `GetAction` / `ReconcileActions` / `GetOrderStatuses`: authoritative status. History is shared with the Oku website, so orders placed in the CLI appear in the UI and vice versa.

Other RPCs used for reads include `GetMarkets`, `GetMarketPrice`, `GetAccountState`, `GetPerpPositions`, `GetOpenOrders`, `GetUserFills`. Transfers, withdrawals and deposits exist in the service but are out of scope for the CLI v1.

## Local state

Config under XDG config dir, credentials separate from config (encrypted file, unlocked via TTY or `OKU_PASSPHRASE_FILE`, no keyring), and a local journal with non-secret execution metadata (intent id, client order id, account, network) persisted **before** submission so unknown outcomes can be reconciled.

## Pending

- Device enrollment protocol (backend) and `/cli` website page: implemented (backend `405b4538a`, frontend `bbfb56930`), not deployed. See validation.md.
