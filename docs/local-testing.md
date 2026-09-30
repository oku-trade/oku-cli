# Local integration testing (operator-driven)

This guide tests the **locally running** Oku accounts service and CLI. It does not start services or apply migrations. Coordinate service startup, database isolation, and teardown with the local runtime operator. These URLs are loopback only, not a public deployment:

- Accounts API: `http://127.0.0.1:13000`
- Oku UI: `http://127.0.0.1:15177/cli` (runtime accounts URL points to port 13000)
- Hyperproxy: staging infrastructure configured against **Hyperliquid mainnet**

The local worker has been observed running with schedules disabled, accounts/background queues active, and metrics at `http://127.0.0.1:16061`. Schedule/workflow lists were empty before and after startup. A synthetic-address `EvmChallenge` returned HTTP 200 with a SIWE challenge and local workflow ID, but **no wallet signature, challenge solution, session, or login was created**. This is startup/read-path evidence, not evidence that enrollment or trading works. Ask the local runtime operator for the secret-free startup, shutdown, and logs runbook; do not infer commands or credentials from this CLI guide.

**Critical:** A local service does **not** imply a sandbox exchange. Any delegated agent approval or submitted order can affect real mainnet funds. Use a wallet you control and a deliberately limited test account. Do not enroll, delegate, submit, cancel, or revoke on behalf of someone else. The read-only steps below require no enrollment or trading authorization.

## 1. Build and check the read path

From this repository:

```sh
make build
./bin/oku version
./bin/oku completion bash  # optional: see docs/usage.md for bash/zsh/fish installation
```

To bind the local API, replace `YOUR_WALLET_ADDRESS` with your own `0x`-prefixed 20-byte address (never a private key). Adding a profile writes local configuration but neither enrolls a device nor places a trade. Without the profile, market reads use the default deployed API, so **pass `--profile local` on every local read**:

```sh
./bin/oku profiles add local --account YOUR_WALLET_ADDRESS \
  --api-url http://127.0.0.1:13000
./bin/oku profiles list
./bin/oku --profile local markets quote BTC --json
./bin/oku markets list --profile local --market BTC --json
./bin/oku account summary --profile local --json
./bin/oku positions list --profile local --json
./bin/oku orders list --profile local --json
./bin/oku fills list --profile local --json
```

The selected profile binds network (`mainnet`), API origin, and account for later authenticated actions. Check `data.meta.api_url` and `data.meta.network` in JSON responses before proceeding. If the local server is stopped, expect a connection error rather than falling back to the deployed API. `--profile local` may appear before or after a command. Do not use `--account` to substitute a different wallet for trading.

An unauthenticated probe of the local device endpoint may return **401**, which confirms the route exists but does not establish that enrollment works. A 404 instead suggests the route is missing or the URL is wrong. Neither response authorizes a write action.

## 2. Optional enrollment, human approval only

Open `http://127.0.0.1:15177/cli` in a browser and connect **the same wallet** as the `local` profile. In browser Developer Tools, verify Network requests go to `http://127.0.0.1:13000/connect` before generating a short-lived enrollment code. Wallet login requires a real, human-approved wallet signature; there is no synthetic login, fabricated session, or auth bypass in this guide. Choose **read-only** scope first if offered. In an interactive terminal, run:

```sh
./bin/oku auth setup --profile local --device-name local-test
```

The CLI prompts for the enrollment code without echo and for a passphrase used to encrypt the local agent key. Do not put the code, passphrase, or private key in command arguments, logs, this guide, or source control. The CLI then prints a **six-digit confirmation code to stderr**. Enter that confirmation code on the Oku website. The UI must verify the enrollment **before** any Hyperliquid delegation prompt. Read-only devices must not request a trading delegation. If polling was interrupted, use `./bin/oku auth setup --profile local --resume` rather than starting another enrollment. Once approved, check:

```sh
./bin/oku auth status --profile local --json
```

For automation, `OKU_PASSPHRASE_FILE` must point to a file readable only by you, but use an interactive masked prompt for this manual test. Do not assume an active local device is proof of a deployed production route.

## 3. Trading verification is a separate, explicit opt-in

**Do not run this section unless an authorized human has deliberately approved a trading-scope agent for the same mainnet account, reviewed the [security risks](security.md), accepted real-money exposure, and confirmed the local backend is ready.** A CLI passphrase unlock grants same-host signing authority. Oku token revocation does not revoke the exchange agent, cancel orders, or close positions.

The grammar below is illustrative only. Replace placeholders with an independently reviewed market, size, price, and returned identifiers. `prepare` may read market data and persists a local intent, but **does not submit an exchange action**. Review its exact JSON output and digest before running `submit`:

```sh
./bin/oku orders prepare --profile local --market BTC --side buy \
  --type limit --size YOUR_REVIEWED_SIZE --price YOUR_REVIEWED_PRICE --json
# Inspect intent_id and digest from prepare. Do not substitute guessed values.
./bin/oku orders submit --profile local \
  --intent RETURNED_INTENT_ID --digest RETURNED_DIGEST --json
# Only if an actual open order exists and cancellation is authorized:
./bin/oku orders cancel --profile local --market BTC \
  --order-id RETURNED_EXCHANGE_ORDER_ID --json
```

If `submit` returns `OUTCOME_UNKNOWN` (exit 6), **do not retry or place a replacement order**. Inspect `./bin/oku orders status --profile local --intent RETURNED_INTENT_ID --json` and `./bin/oku orders reconcile --profile local --json` until the original outcome is authoritative. `orders abandon` is only for an intent that was **never signed**, not a way to clear an ambiguous submission. Confirm exchange order and position state independently in the Oku UI. Revoking a local device and revoking its Hyperliquid agent are separate human decisions.

## Evidence and limits

The CLI's read-only local quote/list and UI runtime wiring have been exercised, including a browser page render and local device-route 401. The local worker and an unauthenticated challenge path were checked, but **wallet login, enrollment, browser verification, delegation, and trading have not been performed**. A repeatable startup/teardown and logs runbook remains the local runtime operator's responsibility. See [validation](validation.md) for the precise checks and [usage](usage.md) for JSON/exit and completion contracts.
