---
name: troubleshoot-macos-keychain
description: Diagnose locked macOS Keychain credentials, binary-update access prompts and headless Oku Agent authentication failures without handling passwords.
---

# Troubleshoot macOS Keychain authentication

Start with `oku docs commands`. Check `oku auth status --json` and `oku doctor --json` without prompting. Do not confuse a missing credential (`AUTH_REQUIRED`) with a locked Keychain (`KEYCHAIN_LOCKED`), an access denial (`KEYCHAIN_ACCESS_DENIED`), or a canceled dialog (`KEYCHAIN_CANCELED`). These Keychain codes exit with status 3. `SIGNER_UNAVAILABLE` can mean the Keychain itself is absent or inaccessible, particularly in a headless session. JSON, MCP, and Service calls never display Keychain UI.

## Ask the user to act in their own terminal

- `KEYCHAIN_LOCKED`: ask the user to run `security unlock-keychain ~/Library/Keychains/login.keychain-db`, enter the password only into the macOS terminal prompt, then retry `oku auth status --verify`. An interactive Oku command can invoke this terminal unlock prompt itself. On managed machines the default Keychain might not be the login Keychain, so check the user's Keychain Access settings rather than assuming the path.
- `KEYCHAIN_ACCESS_DENIED`: after a CLI update the new binary may not be trusted by the existing item's access list. Ask the user to run `oku auth status --verify` in their own graphical terminal and choose **Always Allow** in the macOS Keychain prompt. **Allow** is only temporary. Alternatively open Keychain Access, find the Oku Agent generic password under the login Keychain, inspect **Access Control**, and authorize the current oku executable. Never weaken its access to all applications.
- `KEYCHAIN_CANCELED`: the user canceled the prompt. Respect that choice and ask whether they want to retry interactively.
- SSH without a graphical session: terminal unlock can unlock a locked Keychain, but the per-binary Always Allow authorization requires a graphical session. Ask the user to approve it locally before retrying SSH or an agent. Headless jobs cannot answer dialogs.
- If the item genuinely disappeared or access cannot be restored, ask the user to sign in locally again using `oku auth login`. `oku auth logout` deletes the local credential and attempts remote revocation, so use it only with explicit user agreement and only as a last resort before a fresh login.

Never ask for, receive, log, or paste the login/Keychain password. Never use `security find-generic-password -w` or other secret-dumping commands. Never delete a Keychain item or run logout without the user's agreement. macOS does not offer the Linux `oku auth migrate --to file` workaround.
