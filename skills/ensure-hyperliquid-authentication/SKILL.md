---
name: ensure-hyperliquid-authentication
description: Check or establish Oku Agent Privy authorization for Hyperliquid perps, including account binding, embedded-wallet approval, session expiry and logout. Use before authenticated reads or trades, or when Oku reports an auth error.
---

# Ensure Oku Agent authentication

Start with `oku docs commands` and use `--json` for machine-readable output. Never request or handle tokens, passphrases or authorization codes in chat.

## Check status

```sh
oku profiles list --json
oku auth status --profile <name> --json
oku auth status --profile <name> --verify --json
```

The account is the external Hyperliquid master wallet. The agent is a separate Privy embedded Ethereum wallet approved by that master. `--verify` refreshes credentials as needed and checks the agent approval. If missing or expired, ask the user to complete the authorization page rather than retrying trades.

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

The default API `https://accounts.lat.gfx.town` is staging; production uses `https://accounts.icarus.tools`. On a fresh configuration, select production with `oku auth login --api-url https://accounts.icarus.tools`; for an existing named profile, select its API URL when adding the profile. The Privy app is built in; `--privy-app-id` or `OKU_PRIVY_APP_ID` can override it.

## Locked credential (Linux)

On Linux the credential is encrypted with a passphrase. Login and any command the user runs in their own terminal keep it unlocked for 8 hours in the kernel keyring (like `sudo`), so agents and MCP can then trade without prompting. If a command fails with `SIGNER_UNAVAILABLE` and "credential is locked", ask the user to run this in their own terminal, then retry:

```sh
oku auth unlock            # optional: --for 2h
```

`oku auth lock` forgets the unlock immediately. Never ask for the passphrase in chat. `OKU_PASSPHRASE_FILE` remains available for unattended servers but is not needed for normal use.

The CLI prints a URL and user code. The user visits the Oku authorization page, signs the Hyperliquid agent approval with their external master wallet, and approves the Privy device grant. That browser-signing wallet becomes the account, even if the profile previously held a different address. The CLI reads the approved agent's master from Hyperliquid and stores credentials only when its role is `agent`. Use `--no-open` to prevent automatic browser launch. Login is never available over MCP or the Service API.

## Logout and revocation

`oku auth logout --profile <name>` best-effort logs out of Oku and revokes the Privy device grant, then deletes local credentials. It **does not** revoke the Hyperliquid agent, cancel orders or close positions. The user must revoke the agent separately on Hyperliquid or the Oku page. A revoked Privy grant's access JWT may remain usable for up to 15 minutes. Never perform logout without the user's authorization.

A stolen credential can sign anything its Privy grant permits. Recommend a policy restricting `eth_signTypedData_v4` to the Hyperliquid Agent typed-data domain with chainId 1337.
