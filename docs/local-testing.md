# Local testing and operator acceptance

The offline test suite uses `httptest` Oku and Privy endpoints. It does not prove a production Privy device-grant JWT is accepted by Accounts or that live Privy wallet RPC signs chainId 1337 typed data. Do not use a funded wallet to close those blockers.

```sh
CGO_ENABLED=0 go build ./...
go vet ./...
go test ./...
go test -race ./pkg/cli/... ./pkg/journal/...
make docs-check
scripts/binary-smoke.sh
```

For an operator-controlled staging integration, configure a dedicated Privy app **Oku Trade Agents** (`cmv72u7fa001m0gk200vq97nd`), an Accounts backend with the multi-app verifier, and the web page `https://oku.trade/agent/authorize`. Use a disposable external master wallet and a separate unfunded embedded agent wallet. Verify that the master signs approval and builder fee on the page, and that the CLI refuses to store credentials before `ValidateAgentKey` reports `approved`.

```sh
oku profiles add local --account 0xYOUR_EXTERNAL_MASTER --api-url https://YOUR_STAGING_CONNECT --default
oku auth login --profile local --no-open
oku auth status --profile local --verify
```

`auth login` prints the URL and user code to stderr, even with `--json`. The human completes the web authorization. Neither the code nor the passphrase should be shared in chat. On Linux the passphrase encrypts tokens, not a private key. On macOS the native Keychain stores tokens. If the grant expires before completion, start a new login rather than reusing a device code.

Check a minimal order only with explicit human approval: `orders prepare`, review the digest, `orders submit`, then inspect status in Oku and Hyperliquid. Verify that an interrupted remote signature leaves a retryable local `created` intent, while an ambiguous `ConfirmAction` remains unresolved for reconciliation. Finally, `oku auth logout` deletes the local credential, but the operator must separately revoke the HL named agent, cancel orders and close positions as appropriate.

Record actual deployment versions, timestamps and observations in `validation.md`. Offline mocks and prior release checks must not be presented as live Privy compatibility evidence.
