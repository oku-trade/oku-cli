---
name: ensure-hyperliquid-authentication
description: Check or establish Oku CLI device authorization for a Hyperliquid account, covering profiles, pairing, scopes, expiry and revocation. Use before any authenticated read or trade, or when oku returns an auth error (exit 3).
---

# Ensure Oku authentication

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets.

## Check the current state (read-only)

```sh
oku profiles list --json
oku auth status --profile <p> --json            # local enrollment: status, account, agent, scopes
oku auth status --profile <p> --verify --json   # also asks Oku whether the device is still valid
```

Scopes:
- `read`: account data.
- `trade`: orders and cancels.
- `risk`: leverage and isolated margin. It is granted separately.

A missing or expired scope is fixed by the user re-enrolling with that permission on the Oku website. Retrying won't help.

## Pairing is manual

Only the user pairs a device. Tell them to run these in their own terminal:

```sh
oku profiles add <name> --account 0x...   # only if they asked to create a profile
oku auth setup --profile <name>           # prompts for the website enrollment code and a local passphrase
oku auth setup --resume --profile <name>  # resume a pending enrollment
```

Never run `auth setup` yourself. Never ask for the enrollment code or passphrase in chat, and never create or edit `OKU_PASSPHRASE_FILE`. Over MCP, authentication problems come back as errors. Pass the hint on to the user.

## Revocation boundaries

`oku auth revoke --profile <p>` revokes only this device's Oku token and deletes the local credentials. It does NOT remove the Hyperliquid agent approval, cancel orders or close positions. Those are separate steps on the Oku website or Hyperliquid. Only revoke when the user asks. See `oku docs show security`.

## macOS Keychain

`oku auth migrate-keychain --profile <p>` moves a legacy encrypted-file credential into the Keychain. It is explicit and interactive, so suggest it and let the user run it. On other platforms it reports that no migration is needed.

## Permissions and consequences

Status reads are harmless. Revocation is irreversible for that device. Pairing grants a local agent key that can trade the account, so it belongs only on hosts the user trusts.
