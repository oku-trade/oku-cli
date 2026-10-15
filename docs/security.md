# Security

## Privy delegated agent model

The account is the user's **external** Hyperliquid master wallet. It holds funds and signs `approveAgent` and builder-fee approval on the Oku authorization page, not in this binary. The trading agent is a distinct Privy **embedded** Ethereum wallet, approved by the master as a Hyperliquid named agent. Oku Agent stores no local agent private key. `oku orders submit` requests `eth_signTypedData_v4` from Privy for the Hyperliquid Agent domain (`Exchange`, version `1`, chainId `1337`, zero verifying contract), and locally recovers the signature against the approved embedded-wallet address before sending it to Oku.

**A readable credential permits signing anything the Privy grant allows on that embedded wallet until revoked.** Revocation may leave an existing access JWT valid for up to 15 minutes. Recommend a Privy policy limiting the wallet to `eth_signTypedData_v4` on that Agent domain. Host compromise, a leaked Linux passphrase file, or a compromised macOS Keychain item can bypass CLI safeguards. The CLI's digest, risk checks and confirmations are guardrails, not a security boundary. The embedded wallet should hold only funds deliberately allocated to agent EVM operations, with a strict Privy policy and minimal gas. Never mistake those balances for Hyperliquid master collateral.

## Credential storage

| Platform | Storage | Unlock |
|---|---|---|
| Linux | scrypt + XChaCha20-Poly1305 encrypted file | Masked interactive prompt, then cached in the kernel user keyring (same uid, never on disk, 8h TTL, `oku auth lock` clears it). Optional mode-0600 `OKU_PASSPHRASE_FILE` for unattended use. Opt-in unprotected mode (empty passphrase plus two typed confirmations) relies only on file permissions. |
| macOS | Keychain | Native Keychain authorization, no CLI passphrase. |

The credential contains Privy access and rotating refresh tokens and an Oku session token. Plaintext metadata binds profile, account, API origin, Privy app ID, agent address and wallet ID. A cross-process profile lock covers refresh and write-back: the rotated refresh token is saved before reuse. The short-lived Privy authorization key exists in memory only, never on disk. The Keychain service identifier and JSON schema version `oku.cli/v1` remain stable.

## Journal and MCP

`journal.sqlite` holds intents, action IDs and burned nonces, not credentials. `signing` holds the account gate while Privy signs; a signing failure or crash-left signing state can return to `created` without sending anything to Hyperliquid. Once `ConfirmAction` may have been sent, the action stays `submitting` or `unknown` until reconciliation. The gate is local to one machine: simultaneous use of the same Privy agent on multiple machines can collide on nonces or exposure.

MCP starts read-only; mutation tools require explicit enablement. Neither MCP nor the Service API runs `auth login` or `auth logout` or prompts for credentials. The user logs in from their own terminal. On Linux headless MCP access works while the credential is unlocked (`oku auth unlock` in the user's terminal), or with a user-supplied `OKU_PASSPHRASE_FILE`. While unlocked, any process running as the same user can use the credential, which is the same trust model as ssh-agent.

## Swap and bridge authorization limits

The EVM sender independently holds tokens and gas on each source chain. The master does not sign these transactions. Prepared intents bind a route, quote, recipient, source/destination chains, exact input and minimum output; submission rejects changed quote parameters, non-EVM deposit addresses, PSBT and unrecognized typed-data routes. Approval is limited to exactly the input amount. Only routes exposing an explicit recipient and minimum output in calldata are considered; this byte-level check alone is **not a semantic ABI proof** for arbitrary router contracts. Operators must restrict Privy policies to audited contract targets/selectors and validate the actual route ABI before funding the wallet or attempting a live swap/bridge. Source receipts prove only source execution, never destination settlement. A timeout or unknown broadcast is not permission to retry. If a route has not been independently verified, do not submit it.

## Independent revocations

The Privy embedded agent wallet now has **EVM spending power independent of Hyperliquid agent trading limits**. Hyperliquid order limits and agent revocation do not protect ERC-20s or native gas held in this wallet. The Privy wallet policy must explicitly allow `eth_sendTransaction` for the intended chains and contracts; restrict grants tightly and do not store more value in the agent wallet than needed. The OAuth transaction parameter shape has not yet been validated against a funded wallet. A receipt confirms an EVM transfer, not Hyperliquid credit to the master.

`oku auth logout` tries Oku session logout and Privy grant revocation on a best-effort basis, reports those outcomes and deletes the local credential. **It does not revoke the Hyperliquid agent approval, cancel orders or close positions.** Revoke the HL agent separately with the master wallet on Hyperliquid or the Oku page. Revoke any other device grants separately. Uninstalling the binary does none of these operations and does not delete journal history or backups.

## Production deployment checks

- Staging on 2026-10-14 verified the merged Accounts multi-app verifier (account#530), Privy login, chainId 1337 signed submit, list and cancel. The authorize page is merged (trade#3569). Check production deployment independently and switch staging's `https://unitrade.lat.gfx.town/agent/authorize` verification URI to `https://oku.trade/agent/authorize`.
- Session lifetime, grant revocation, agent-session attribution and backend recovery-to-approved-agent enforcement need operator review.
- Hyperliquid named-agent slots (`oku1` through `oku3`) are shared with the web app. The dedicated Privy app must not silently reuse a retail embedded wallet already acting as the master.

Report vulnerabilities privately to Oku, never with tokens or signed payloads in public issues.
