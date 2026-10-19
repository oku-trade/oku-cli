# Local testing and operator acceptance

The offline test suite uses `httptest` Oku and Privy endpoints. Staging acceptance on 2026-10-14 verified login, Privy signed submit, list and cancel; this does not prove production acceptance. Do not use a funded wallet for remaining checks.

```sh
CGO_ENABLED=0 go build ./...
go vet ./...
go test ./...
go test -race ./pkg/cli/... ./pkg/journal/...
make docs-check
mise run binary-smoke bin/oku
```

For operator-controlled staging, use the built-in Privy app **Oku Trade Agents** (`cmv72u7fa001m0gk200vq97nd`), merged Accounts verifier (account#530) and authorize page (trade#3569). Staging verification currently uses `https://unitrade.lat.gfx.town/agent/authorize`; switch to `https://oku.trade/agent/authorize` for production. Use a disposable external master wallet and a separate unfunded embedded agent wallet.

```sh
oku profiles add local --account 0xYOUR_EXTERNAL_MASTER --api-url https://YOUR_STAGING_CONNECT --default
oku auth login --profile local --no-open
oku auth status --profile local --verify
```

`auth login` prints the URL and user code to stderr, even with `--json`. The human completes the web authorization. Neither the code nor the passphrase should be shared in chat. On Linux the passphrase encrypts tokens, not a private key. On macOS the native Keychain stores tokens. If the grant expires before completion, start a new login rather than reusing a device code.

Check a minimal order only with explicit human approval: `orders prepare`, review the digest, `orders submit`, then inspect status in Oku and Hyperliquid. Verify that an interrupted remote signature leaves a retryable local `created` intent, while an ambiguous `ConfirmAction` remains unresolved for reconciliation. Finally, `oku auth logout` deletes the local credential, but the operator must separately revoke the HL named agent, cancel orders and close positions as appropriate.

Agent-wallet funds need native gas in the agent wallet and a full RPC for the chain: `export OKU_RPC_URL_42161=https://YOUR_ARBITRUM_RPC`. With explicit human approval and tiny amounts, run `oku wallet balances --chain 42161`, then `oku send prepare`/`submit`/`status` (to the master by default) and `oku swap prepare`/`submit`/`status`. Submit within about 3 minutes of prepare. A swap that needs an approval sends one atomic batch; confirm on an explorer that the agent's allowance to Oku Router returns to zero. Never repeat submit after `OUTCOME_UNKNOWN`.

Record actual deployment versions, timestamps and observations in `validation.md`. Offline mocks and prior release checks must not be presented as live Privy compatibility evidence.
