# Architecture

```
human terminal ──> oku auth login ──> Privy device grant ──> Oku /agent/authorize
                                                    │
external master wallet ──approveAgent───────────────┤
                                                    ▼
Oku Agent (Go) ──Oku session──> Accounts AuthService + PerpsService──> Hyperliquid
       │
       ├─ Privy embedded Ethereum agent wallet RPC (typed-data signing)
       ├─ encrypted credential / macOS Keychain (tokens only; no wallet private key)
       └─ SQLite journal (intents, nonces, signing/submission states)
```

Module `github.com/oku-trade/oku-cli`, binary `oku`, JSON schema `oku.cli/v1`. `pkg/cli` owns commands, profile/session renewal and signing integration. `pkg/privy` owns OAuth device grant, refresh, HPKE wallet authorization and signed wallet RPC. `pkg/auth` stores profile-bound credentials and takes a cross-process lock over refresh-token rotation. `pkg/api` wraps generated Connect clients for Accounts AuthService and PerpsService. `pkg/signing` computes the Hyperliquid action hash and typed-data digest and recovers signatures. `pkg/journal` owns SQLite persistence. `pkg/mcp` exposes a read-only-by-default agent interface but never interactive login/logout.

## Login and session renewal

The user supplies the account (an external HL master) in the profile or via `--account`. Login prints the Privy device authorization URL and user code, then polls until approved. The authorization page pairs a separate embedded Ethereum wallet with the external master and the master signs HL agent approval. The CLI authenticates the embedded wallet, exchanges the Privy JWT for an Oku session, then calls `ValidateAgentKey`; it stores nothing unless the response says `approved`. On authenticated calls it locks and reloads the credential, refreshes a near-expiry Privy token and persists the new rotating token **before** proceeding, and extends or re-exchanges an expiring Oku session. Signing access is requested lazily only for submit.

## Submission lifecycle

`orders prepare` writes a local intent and digest. `orders submit` requests a server-built action and validates it against the intent. The journal allocates and burns a nonce and holds the account gate in `signing`. Privy signs the Hyperliquid Agent EIP-712 typed data (`Exchange/1/1337/zero address`), and the CLI recovers the returned signature to the stored agent address. Failure or a crash-left `signing` state returns to `created` (after a grace period to protect a concurrent signer); a fresh retry gets a new nonce. After signature verification and a second intent-expiry check, state becomes `submitting` and the CLI calls `ConfirmAction` exactly once. Ambiguous confirm outcomes remain unresolved until `orders reconcile`; neither signing again nor a blind confirm retry is safe.

The journal gate is machine-local. Sharing one embedded agent across machines can cause nonce collisions or simultaneous exposure. Revoking an Oku session or Privy device grant does not remove Hyperliquid agent approval.

## Outstanding integration

Staging acceptance on 2026-10-14 verified Privy login, chainId 1337 typed-data signed submission, order list and cancel. Accounts multi-app verifier account#530 and authorize page trade#3569 are merged. Production deployment and remaining operational checks are separate (see [deployment](deployment.md)).
