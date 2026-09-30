# Security

## Signing mode: trusted-host delegated signer

The CLI signs with a Hyperliquid **agent (API wallet) key** stored on the host, approved by your main wallet via Oku. Your main wallet private key is never entered into the CLI.

Risks you accept:

- Anything that can use the key can sign exchange actions directly, bypassing Oku and any CLI limits. On Linux that is anything that can unlock the encrypted credential file (for example a process as your user that can read `OKU_PASSPHRASE_FILE`, including a coding agent with shell access). On macOS it is anything the Keychain lets read the item. Encryption at rest and the Keychain do not isolate the key from a compromised or over-trusted process running as your user.
- Agent keys can do more than place orders (for example leverage, margin-mode and isolated-margin changes, per Hyperliquid docs). Do not treat the key as "order-only". Withdrawals to external addresses require the main wallet.
- Local confirmations, digests and notional checks are guardrails, not security boundaries. A digest proves the payload was not changed after `prepare`. It does not prove that a human approved it.

Only use this on a host you trust.

## Credential storage by platform

| Platform | Provider | Unlock |
|---|---|---|
| Linux (and other non-Darwin) | Encrypted file: scrypt KDF + XChaCha20-Poly1305 | Interactive masked TTY prompt, or `OKU_PASSPHRASE_FILE` (must be mode 0600). Never a command-line argument. |
| macOS | Native Keychain via Security.framework (`github.com/keybase/go-keychain`) | The Keychain authorizes access. No passphrase is requested. |

Neither platform has a plaintext fallback, and neither falls back to the other provider. The macOS provider needs CGO, so macOS binaries must be built natively on macOS (see [releasing](releasing.md)). A macOS binary built without CGO cannot use the Keychain, and it fails instead of storing credentials another way.

The CLI never prints, logs or returns agent private keys, device tokens, poll secrets or Keychain values.

### Explicit legacy credential migration (macOS)

Earlier builds stored credentials in the encrypted file on macOS too. Normal reads, enrollment and trading **never** migrate automatically. Migration only happens when you run:

```sh
oku auth migrate-keychain --profile NAME
```

It asks for the legacy passphrase and decrypts the file. It then writes the Keychain item and reads it back to confirm it matches. Only after that check does it rename the encrypted file to `<credential>.pre-keychain` (mode 0600, no longer active).

- If anything fails before the rename, the encrypted file stays active.
- If a Keychain item already exists and differs, or a `.pre-keychain` backup already exists, it refuses and you must resolve the conflict by hand.
- On Linux the command exits 2 with a usage error. No migration is needed there.

The `.pre-keychain` backup still holds the encrypted key. Delete it yourself once you have confirmed the Keychain credential works, or keep it as a recoverable copy. Nothing deletes it for you.

## Device scopes

Enrollment grants the scopes chosen on the Oku website:

- `read`: account reads only. No Hyperliquid delegation.
- `trade`: orders and cancels.
- `risk`: leverage, margin-mode and isolated-margin changes (`oku positions leverage` / `positions margin`, then `orders submit`). This is a separate opt-in. A `trade` device cannot submit risk actions.

The CLI checks scopes locally before unlocking, and the backend enforces them again. The backend support for the `risk` scope is **not deployed** (see [deployment](deployment.md)). Leverage changes and orders are separate exchange actions. They are not one atomic "buy with N× leverage".

## Local execution journal

Intents, action IDs, allocated nonces and execution states live in a SQLite database under the state directory: `journal.sqlite`, plus `-wal`/`-shm` sidecars. All of these are mode 0600, and the directory is 0700. The journal holds no secrets. It does contain your account address, market, size and price for every prepared intent.

Safety rules the journal enforces:

- The nonce and the `submitting` state are committed in one transaction before anything is signed or sent.
- An unknown outcome survives restarts and blocks new exposure on the same account, across every profile and API origin, until it is reconciled.
- A signed or possibly sent action can never be reset or abandoned.

Pre-SQLite JSON journals are **not** migrated automatically. See [usage](usage.md#local-journal).

## MCP server

The planned `oku mcp serve` reuses the same profiles, credentials, scopes and journal as the CLI:

- It starts read-only. Mutation tools must be enabled explicitly in its configuration, and they still need the enrolled scopes.
- Tool annotations are hints, not authorization.
- It never prompts for pairing codes or passphrases over stdio. Pair with `oku auth setup` in a terminal first.

See [usage](usage.md#mcp-server) for its current status.

## Revocation is separate

These are independent. None implies another:

1. **Oku session/token revocation**: stops the CLI using Oku APIs. Use the website device list, or `oku auth revoke`, which also deletes local credentials.
2. **Exchange agent-key revocation**: stops the key signing on Hyperliquid. It requires deregistering or replacing the agent from your main wallet. Revoking the Oku token alone does **not** do this.
3. **Canceling open orders.**
4. **Closing positions.**

Uninstalling the binary or npm package does none of these. It also leaves the journal, the credential file or Keychain item, and any `.pre-keychain` backup in place. Never reuse a retired agent address.

## Reporting

Report vulnerabilities privately to the Oku team. Do not open public issues with key material.

## Enrollment flow (implemented, not deployed)

- `VerifyEnrollment` checks the human-entered CLI code **before** the browser offers Hyperliquid agent delegation. No delegation happens for an unverified device.
- Read-only devices never get a delegated Hyperliquid signer.
- The CLI polls enrollment status until the device is verified. Live enrollment against deployed routes has not been tested end to end.
