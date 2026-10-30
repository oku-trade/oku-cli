---
name: ensure-hyperliquid-authentication
description: Check or establish Oku Agent Privy authorization, including account binding, embedded-wallet approval, session expiry and logout. Use before authenticated reads, swaps, bridges or trades, or when Oku reports an auth error.
---

# Ensure Oku Agent authentication

Start with `oku docs commands` and use `--json` for machine-readable output. Never request or handle tokens, passphrases or authorization codes in chat.

## Check status

```sh
oku profiles list --json
oku auth status --profile <name> --json
oku auth status --profile <name> --verify --json
```

The account is the external Hyperliquid master wallet. The agent is a separate Privy embedded Ethereum wallet approved by that master. Plain status reads only local metadata and reports `verified: false`. `status: active` alongside a past `access_expires_at` is normal and proves nothing about live approval. `--verify` refreshes credentials as needed, may renew the Oku session and save updated credentials, and then checks the agent approval. Do not run it when told to stay read-only. If missing or expired, ask the user to complete the authorization page rather than retrying trades.

## User-operated login

In their own terminal, the user runs:

```sh
oku auth login
```

With no profiles configured, this creates a profile named `default` once login succeeds and makes it the default. For a named profile:

```sh
oku profiles add <name> --account 0x...
oku auth login --profile <name>
```

The default API is production, `https://accounts.icarus.tools`. Existing profiles keep the API URL they were created with (check `oku profiles list`); `--api-url` is available for custom deployments. The Privy app is built in; `--privy-app-id` or `OKU_PRIVY_APP_ID` can override it.

On macOS, when Keychain authentication fails, follow `troubleshoot-macos-keychain` before suggesting another login. In particular, an updated binary may only need the user's **Always Allow** authorization.

## Credential storage (Linux)

On Linux the default `file` store is a plain 0600 file with no passphrase, so agents and MCP can use it right after login. A profile can instead use the desktop `keyring` store; check `credential_store` in `oku auth status --json`. With the keyring store, a "credential store unavailable" error means the user must unlock their desktop keyring (or run `oku auth migrate --to file` on a headless machine).

Credentials from older releases were protected by a passphrase. If a command fails with `AUTH_REQUIRED` and mentions the old passphrase-protected format, ask the user to run this once in their own terminal, then retry:

```sh
oku auth unlock
```

It asks for the old passphrase once and converts the credential. Never ask for the passphrase in chat.

The CLI prints a URL and user code. The user visits the Oku authorization page, signs the Hyperliquid agent approval with their external master wallet, and approves the Privy device grant. That browser-signing wallet becomes the account, even if the profile previously held a different address. The CLI reads the approved agent's master from Hyperliquid and stores credentials only when its role is `agent`. Use `--no-open` to prevent automatic browser launch. Login is never available over MCP or the Service API.

## Logout and revocation

`oku auth logout --profile <name>` best-effort logs out of Oku and revokes the Privy device grant, then deletes local credentials. It **does not** revoke the Hyperliquid agent, cancel orders or close positions. The user must revoke the agent separately on Hyperliquid or the Oku page. A revoked Privy grant's access JWT may remain usable for up to 15 minutes. Never perform logout without the user's authorization.

A stolen credential can sign anything its Privy grant permits. Recommend a policy restricting `eth_signTypedData_v4` to the Hyperliquid Agent typed-data domain with chainId 1337.
