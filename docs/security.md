# Security

## Privy delegated agent model

The account is the user's **external** Hyperliquid master wallet. It holds funds and signs `approveAgent` and builder-fee approval on the Oku authorization page, not in this binary. The trading agent is a distinct Privy **embedded** Ethereum wallet, approved by the master as a Hyperliquid named agent. Oku Agent stores no local agent private key. `oku orders submit` requests `eth_signTypedData_v4` from Privy for the Hyperliquid Agent domain (`Exchange`, version `1`, chainId `1337`, zero verifying contract), and locally recovers the signature against the approved embedded-wallet address before sending it to Oku.

**A readable credential permits signing anything the Privy grant allows on that embedded wallet until revoked.** Revocation may leave an existing access JWT valid for up to 15 minutes. Recommend a Privy policy limiting the wallet to `eth_signTypedData_v4` on that Agent domain. Host compromise, a leaked Linux passphrase file, or a compromised macOS Keychain item can bypass CLI safeguards. The CLI's digest, risk checks and confirmations are guardrails, not a security boundary. The embedded wallet must never be used as a funded trading account.

## Credential storage

| Platform | Storage | Unlock |
|---|---|---|
| Linux | scrypt + XChaCha20-Poly1305 encrypted file | Masked interactive prompt, or user-supplied mode-0600 `OKU_PASSPHRASE_FILE`. |
| macOS | Keychain | Native Keychain authorization, no CLI passphrase. |

The credential contains Privy access and rotating refresh tokens and an Oku session token. Plaintext metadata binds profile, account, API origin, Privy app ID, agent address and wallet ID. A cross-process profile lock covers refresh and write-back: the rotated refresh token is saved before reuse. The short-lived Privy authorization key exists in memory only, never on disk. The Keychain service identifier and JSON schema version `oku.cli/v1` remain stable.

## Journal and MCP

`journal.sqlite` holds intents, action IDs and burned nonces, not credentials. `signing` holds the account gate while Privy signs; a signing failure or crash-left signing state can return to `created` without sending anything to Hyperliquid. Once `ConfirmAction` may have been sent, the action stays `submitting` or `unknown` until reconciliation. The gate is local to one machine: simultaneous use of the same Privy agent on multiple machines can collide on nonces or exposure.

MCP starts read-only; mutation tools require explicit enablement. Neither MCP nor the Service API runs `auth login` or `auth logout` or prompts for credentials. The user logs in from their own terminal. On Linux headless MCP access requires the user to supply `OKU_PASSPHRASE_FILE`.

## Independent revocations

`oku auth logout` tries Oku session logout and Privy grant revocation on a best-effort basis, reports those outcomes and deletes the local credential. **It does not revoke the Hyperliquid agent approval, cancel orders or close positions.** Revoke the HL agent separately with the master wallet on Hyperliquid or the Oku page. Revoke any other device grants separately. Uninstalling the binary does none of these operations and does not delete journal history or backups.

## Deployment blockers (unverified)

- Device-grant JWT acceptance by the backend `PrivySessionExchange` verifier requires the Accounts branch `feat/privy-multi-app-verifier` deployed for the dedicated Oku Trade Agents app. This has **not been confirmed live**.
- Privy OAuth wallet RPC accepting `eth_signTypedData_v4` with chainId 1337 has **not been confirmed live**.
- Session lifetime, grant revocation, agent-session attribution and backend recovery-to-approved-agent enforcement need operator review.
- Hyperliquid named-agent slots (`oku1` through `oku3`) are shared with the web app. The dedicated Privy app must not silently reuse a retail embedded wallet already acting as the master.

Report vulnerabilities privately to Oku, never with tokens or signed payloads in public issues.
