# Architecture

```
agent / human ──> oku (Go binary) ──Connect RPC──> Oku account service (PerpsService) ──> Hyperliquid
                    │                                   │
                    ├─ local delegated signer key       └─ shared action history (web UI + CLI)
                    │   (Linux: encrypted file, macOS: Keychain)
                    └─ local SQLite journal (intents, nonces, states)
```

- Module `github.com/oku-trade/oku-cli`. The entry point is `./cmd/oku`, and the version is injected via `-X main.version=...`.
- Default endpoint: `https://accounts.lat.gfx.town/connect` (Connect protocol). Override it per profile with `oku profiles add NAME --api-url URL`.
- The binary is intentionally thin. Fees, risk rules and action construction live in the backend and are not duplicated locally. The CLI does validate every backend-built action against the locally prepared intent before it signs.

## Code layout

| Path | Role |
|---|---|
| `cmd/oku` | The only executable. |
| `pkg/cli` | Command tree (urfave/cli v3), handlers, and `Service`, the non-interactive in-process entry point intended for the MCP server so it shares validation, auth, signing and journal code instead of shelling out. |
| `pkg/journal` | SQLite store (`modernc.org/sqlite`, pure Go) with Goose migrations embedded from `pkg/journal/migrations`. |
| `pkg/auth` | Credential providers: encrypted file (non-Darwin), Keychain (`darwin && cgo`). |
| `pkg/trading`, `pkg/signing`, `pkg/decimal` | Intents, digests, action validation, Hyperliquid signing, exact decimals. |
| `pkg/api` | Connect client for the account service. |
| `pkg/gen`, `proto/` | Vendored account protos (`proto/SOURCE.yaml` records the upstream commit) and committed generated Connect code. Ordinary builds need neither the private backend repo nor codegen. `scripts/sync-protos.sh` resyncs and regenerates. |
| `pkg/docs`, `pkg/skills`, `assets.go` | `assets.go` in the module root embeds an explicit list of the canonical `docs/*.md` files, which excludes the repository-only `validation.md`, plus the `skills/<name>/` directories. These are the single source. Go cannot embed through `..` or symlinks, so there are no copies. |
| `pkg/drafts` | Local, syntax-only repair of malformed order-draft JSON (`github.com/kaptinlin/jsonrepair`). It is never applied to backend responses, signed actions, MCP frames, credentials or the journal. |

## Order flow

1. `oku orders prepare` is local only. It returns intent_id + digest. The intent is persisted in the journal before anything else happens.
2. On `submit --intent --digest`, `CreateAction` runs: the backend builds the exchange action payload for an explicit account and network. The CLI compares that payload with the prepared intent and refuses to sign on any divergence.
3. In one SQLite transaction, the journal allocates the signer-global nonce, marks the record `submitting`, and checks that no unresolved submission exists for the same account on any profile or origin.
4. The CLI signs the payload locally with the delegated (agent) signer key. The key never leaves the host.
5. `ConfirmAction`: the signed action is submitted through Oku. It is never retried automatically.
6. `GetAction` / `ReconcileActions` / `GetOrderStatuses` give the authoritative status. History is shared with the Oku website, so orders placed in the CLI appear in the UI and vice versa.

Leverage and isolated-margin changes (`oku positions leverage` / `positions margin`) use the same prepare → submit flow with `LeverageActionMetadata` / `MarginActionMetadata`. They need the `risk` device scope. They are separate exchange actions from orders. Only a server `completed` state counts as success. An `error` observed after a signature may have left the process is treated as unknown, not as a rejection.

Other RPCs used for reads include `GetMarkets`, `GetMarketPrice`, `GetAccountState`, `GetPerpPositions`, `GetOpenOrders` and `GetUserFills`. Transfers, withdrawals and deposits exist in the service but are out of scope for the CLI.

## Local state

- Config lives under the XDG config dir. `oku config path` prints every path.
- Credentials are kept separate from config. See [security](security.md#credential-storage-by-platform).
- The journal is `journal.sqlite` under the state dir, with WAL, `synchronous=FULL`, foreign keys, a 5 s busy timeout, and 0600 file / 0700 directory permissions. Transactions never span network calls. A regular file named `journal` at the legacy JSON journal path acts as a downgrade barrier, because older binaries expect a directory there and fail instead of silently reopening stale JSON state. A `journal-migration-incomplete` marker blocks signing until an import has been verified. Rerunning `oku journal migrate` with no legacy journal is a no-op. Legacy JSON journals are not imported automatically (see [usage](usage.md#local-journal)).

## Pending

- The device enrollment protocol (backend) and the `/cli` website page are implemented but not deployed. The `risk` scope and authenticated device attribution are backend work on an undeployed feature branch. See [deployment](deployment.md) and [validation](validation.md).
