# Security

## Signing mode: trusted-host delegated signer

The CLI signs with a Hyperliquid **agent (API wallet) key** stored on the host, approved by your main wallet via Oku. Your main wallet private key is never entered into the CLI.

Risks you accept:

- Anything that can read the key (the encrypted credential file once unlocked, or any process as your user that can read `OKU_PASSPHRASE_FILE`, including a coding agent with shell access) can sign exchange actions directly, bypassing Oku and any CLI limits. Encryption at rest does not isolate it from a same-user agent that can unlock it.
- Agent keys can do more than place orders (for example some account-mode and internal balance actions, per Hyperliquid docs). Do not treat it as "order-only". Withdrawals to external addresses require the main wallet.
- Local confirmations, `--yes` prompts and notional caps are guardrails, not security boundaries.

Only use this on a host you trust.

## Revocation is separate

These are independent. None implies another:

1. **Oku session/token revocation**: stops the CLI using Oku APIs (website device list, or `oku auth revoke`, which also deletes local credentials).
2. **Exchange agent-key revocation**: stops the key signing on Hyperliquid. Requires deregistering/replacing the agent from your main wallet. Revoking the Oku token alone does **not** do this.
3. **Canceling open orders.**
4. **Closing positions.**

Uninstalling the binary does none of these. Never reuse a retired agent address.

## Reporting

Report vulnerabilities privately to the Oku team. Do not open public issues with key material.

## Enrollment flow (in development)

- `VerifyEnrollment` checks the human-entered CLI code **before** the browser offers Hyperliquid agent delegation. No delegation happens for an unverified device.
- Read-only devices never get a delegated Hyperliquid signer.
- The CLI polls for `device_verified`. That status is still pending in the backend. These docs will be updated once the code is aligned.
