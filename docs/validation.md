# Validation

Only what was actually executed is recorded here. **Status as of 2026-09-30: not production-ready.** Live enrollment, signing and trading have not been tested. **No real order, cancel, leverage or margin action has been submitted by any validation run.**

## Current milestone (SQLite, Keychain, docs, skills, risk, drafts, MCP, npm): implementation verified

Final implementation CI [run 36747435979](https://github.com/oku-trade/oku-cli/actions/runs/36747435979) at commit `9d8dc9e` passed all six jobs on 2026-09-30. Subsequent integration changes preserve upstream Nix README instructions and update documentation only; runtime Go code, dependencies, tests, and build/release scripts are unchanged. This is **not** an npm release or production deployment. Backend PR https://github.com/oku-trade/account/pull/415 and frontend PR https://github.com/oku-trade/trade/pull/3343 remain unmerged and undeployed. Local runs used Linux amd64 with scratch `HOME`/XDG dirs. Native Linux arm64 and macOS evidence comes from GitHub-hosted runners.

| Area | Actually executed | Not yet verified |
|---|---|---|
| Build and tests | Root full suite `722662p3xx`: `go vet ./...` and `go test -race ./...` passed for every package (api, auth, cli, decimal, docs, drafts, journal, signing, skills, trading; CLI 215.560s), against fakes and fixtures. The final journal change was then rerun independently. `make docs-check` passed (`oku docs commands --markdown` equals `docs/commands.md`). The Nix package build (`flake.nix`, updated `vendorHash`) passed, and the resulting binary passed `version` and `scripts/binary-smoke.sh`. Module checksums were unchanged after `go mod tidy`. | |
| Binary smoke | `scripts/binary-smoke.sh` passed: `version --json`, `--json` usage error with exit 2, `docs list`/`show`, `skills list`. `docs commands` lists every command in [usage](usage.md). `docs list` returns the 7 embedded documents (this file is intentionally not embedded). `drafts repair --stdin` turned a fenced, unquoted-key draft into `repaired:true` with `prepare_args`, exit 0, and made no network or journal writes. | |
| MCP server (root, 16:30 UTC, rebuilt binary) | Over stdio, `initialize` listed 13 read tools and 15 resources. Mutation tools were hidden without `--allow-mutations`. `--profile local` kept the server pinned to `local` even though the default profile was different. An in-process SDK session test (`pkg/cli/agentux_test.go`) also covers pinning and refusing a conflicting profile. | Any mutation tool call, including with `--allow-mutations`. Cancellation. Authenticated tool calls. |
| Local reads (root, 16:30 and 16:40 UTC) | Against the local accounts API `127.0.0.1:13000`: BTC quote and market list succeeded, `markets funding` returned 24 records, and `positions list` for a synthetic public address returned an empty list. A locally prepared intent and its `orders status` worked with scratch state. Nothing was submitted. | Authenticated reads. Parsing of non-empty positions. |
| Skills | `skills list` returns 8 skills. `skills install --dir <scratch>` wrote all 8 `SKILL.md` files (root, 16:30 UTC). A pinned `npx skills@1.7.0 add <local repo> --list`, run with a scratch HOME/cache and `DO_NOT_TRACK=1`, found all 8 canonical skills and installed nothing. | Walking each skill command by command against the binary. Anonymous discovery from GitHub (the repo is private). |
| SQLite journal | Unit tests cover Goose schema versioning, nonce/state transactions, and rejection of a symlinked database. Nonce allocation across separate processes and reopening a persisted `submitting` record are tested (`TestMultiprocessNonceAndCrashState`). | Killing a real process between the durable commit and the network send. Disk-full. |
| macOS Keychain | Hosted run `36746272076` (`3e2dfe5`) on both macOS runners (Intel and arm64): an actual round trip through a disposable test Keychain, profile/binding checks, and the locked-keychain failure path, with the default keychain restored afterwards. Also the provider boundary unit test with a fake keychain. | Keychain behavior across signed-identity upgrades. A user's login keychain (deliberately not touched). |
| Risk actions | Synthetic CLI tests against a fake Oku service (`pkg/cli/risk_test.go`) cover:<br>- leverage `completed` with a zero OID counts as success<br>- margin `completed` with exact 6-decimal units<br>- `submitted`/`unresolved`/`error`/`pending` after send stay `OUTCOME_UNKNOWN`: no resend, new risk intents blocked, reconcile keeps them unknown<br>- a divergent backend payload is refused before signing<br>- an error before send counts as rejected<br>- abandoning uses the risk scope | Backend `risk` scope (**not deployed**). **No live submission.** |
| Native CI matrix / npm | Final hosted run `36747435979` (`9d8dc9e`) passed **all six jobs**: Nix, protobuf drift, Linux amd64/arm64 and macOS amd64/arm64. Every native platform passed project tests, the race suite, native build/archive, actual binary smoke, offline npm installation and launcher smoke, and native-to-npm binary checksum equality. Both macOS jobs additionally passed real isolated Keychain tests, Security.framework linkage and ad-hoc codesign verification. Earlier native testing caught and fixed the long-hostname enrollment bug and BSD `wc` padding in the smoke harness. | Nothing published. No provenance (private source repo). No Developer ID signing or notarization. |

Financial acceptance for this milestone is **not run**: enrollment into Keychain or file storage, submit, cancel, risk actions, reconcile, MCP mutations. It remains operator-gated (see [deployment](deployment.md#operator-run-integration-acceptance-required-before-any-release)).

## Historical v1 evidence (not the current release)

Everything below was recorded for earlier commits (up to `eaad4ba`/`29bd173`). It is kept for traceability, and the current milestone above supersedes it where they differ. Migration 137 was later applied to a dedicated **local** database (see "Isolated local runtime" below). It has not been applied to any shared or deployed database.

### v1 status (historical)

| Area | Latest evidence | Status |
|---|---|---|
| CLI | Final `eaad4ba`: `go test -race ./...`, `go vet ./...`, `make build`, `./bin/oku version --json` and live `markets quote BTC` all exit 0 (root `BG991879haiq`). Review source-cleared the submitted-without-oid UNKNOWN handling, never-signed-only abandon, and journal error propagation. | Passing synthetic + real read-only checks. Submit/trading not live-tested. |
| CLI packaging | `scripts/build.sh` at `eaad4ba`: 4 archives, reproducible (below). | Done locally. Not published or signed. |
| Frontend | HEAD `bbfb56930`: full suite 252 files / 3208 tests pass, `lint:check` exit 0 (`BG683551au6j`). Vite build exit 0. `/cli` renders in Chromium on desktop and mobile. | Passing. Wallet-connected flow untested. |
| Backend | `405b4538a`: tests, vet and build pass (`BG692697b2l7`). `go test -race` passes for `./src/connectapi`, `./src/connectapi/clidevice`, `./src/modules/features/clidevice` and `./src/pkg/hlsigning` (`BG8948497p4q`, no DB exercised). Integration tests were compiled only. Two review fixes verified. | Passing pure checks. At the time, migration 137 was not applied anywhere. It was later applied only to a dedicated local database. Device routes are **not deployed**. |
| Deployed read API | `GetMarkets` over Connect JSON returns HTTP 200 with BTC data. | Verified. |
| End-to-end enrollment/trading | Not run. Operator-only (see `deployment.md` acceptance checklist). | **Blocked on operator.** |

### Kinds of evidence (v1)

| Kind | What it covers | What it does not cover |
|---|---|---|
| **Synthetic** | CLI unit and safety tests (race, vet) against HTTP fixtures and fakes: unknown-outcome handling, the abandon limit, reconcile, digests, skills install. Backend pure unit tests. | Any real server behavior. |
| **Real, read-only** | Live `GetMarkets` and live CLI `markets` reads against the deployed Oku API. Local `orders prepare`, which performs read API calls and persists an intent but submits no exchange action. | Authenticated calls or writes. |
| **Real, unauthenticated UI** | The actual Vite app `/cli` in Chromium (a temporary local server, since stopped), with no wallet and no mocks. | Wallet connection. |
| **Not verified** | Wallet connection, enrollment (Verify/Approve), agent delegation, `orders submit`/cancel, revocation, deployed device routes, migration 137. | |

### v1 details

#### urfave/cli v3 migration (local, pending review)

- `go test -race ./...`, `go vet ./...`, `make build`, and `make docs-check` passed locally after migration to urfave/cli v3.13.0 and moving Go packages from `internal/` to `pkg/`. The CLI test suite retains auth, account binding, signing, unknown-outcome, no-retry, and never-signed-abandon checks against fake HTTP services.
- Real public read-only `markets quote BTC --json` and `markets list --json` returned successful JSON envelopes from `https://accounts.lat.gfx.town`. No enrollment, signing, or trading was attempted for this migration.
- Generated bash, zsh, and fish completion scripts were sourced and invoked in the installed shells. A separate real Bash regression checks that redirection, command substitution, backticks, and semicolons in completion words do not execute or change sentinel files. Zsh/fish scripts use urfave generation; bash uses a safe array-based wrapper around its completion candidate protocol.
- Framework parse error tests verify a single JSON envelope on stdout, usage exit code 2, and no network calls. `make docs-check` compares the committed generated reference against the command tree and runs in CI.
- Final CLI commit `29bd173`: root independently reran race, vet, docs-check and build; four local release archives (linux/darwin × amd64/arm64) were built with checksums independently verified. Nothing was published. Actual bash/fish completions returned expected command candidates; zsh `compinit` registered `_oku`. Bash redirection and command-substitution sentinels remained unchanged with the safe wrapper.
- **Isolated local runtime, not deployed enrollment:** accounts API `127.0.0.1:13000` uses a dedicated local database at migration 137 and staging Hyperproxy configured for mainnet. Scratch-profile CLI quote/list against this loopback API succeeded. The local unauthenticated device endpoint returned 401 rather than the deployed endpoint's 404. Chromium loaded local UI `/cli` at `127.0.0.1:15177`, saw heading/HTTP 200 and runtime accounts URL on port 13000; observed Connect origins were local and page errors were empty. A local worker was observed running with schedules disabled, accounts/background queues active and metrics at `127.0.0.1:16061`. Schedule/workflow lists remained empty; `EvmChallenge` for a synthetic address returned HTTP 200, a SIWE challenge and local workflow ID. No challenge was signed or solved and no session created. These checks did **not** perform wallet login, enroll a device, approve delegation, sign, or trade. See [local testing](local-testing.md) for a cautious manual guide.

#### CLI

- Live smoke (`BG663998t7rz`), all exit 0:
  - `version`
  - **live** `markets quote BTC`, `markets list --market BTC`, `markets funding BTC --since 1h`
  - scratch `profiles add` and `profiles list`
  - local `orders prepare`, which returned a valid digest and a warning that the builder fee is not approved
  - `skills install` and `skills uninstall` against a scratch target

  **No `orders submit`.**
- Docs worker at `ca84dac`: `--help` for every command was captured and the docs aligned to it. `version --json` returns an `oku.cli/v1` envelope. `orders prepare` with no profile returns `PROFILE_NOT_FOUND` and exits 2.
- Fixed in `eaad4ba` and covered by the final run: a `submitted` status without `hlOid` is treated as `OUTCOME_UNKNOWN`. `orders abandon` is limited to intents that were never signed. `orders abandon` can no longer clear an UNKNOWN intent after a 2-minute grace period.

#### Packaging (final, `eaad4ba`)

`VERSION=eaad4ba ./scripts/build.sh` was run twice into separate directories. Both runs produced identical `SHA256SUMS` (Go 1.27.1, linux/amd64 host) and `sha256sum -c` passed. Nothing was published.

- darwin_amd64 `eaa7d27d7bc00803a6eb2ee6bbdec3c987785d00a13d9241fad2ffd0e7ebaefd`
- darwin_arm64 `2d8ae532ed4b94532ea31a3a41c9d2d8e2140d19d7b483dc456064c43c2bc57f`
- linux_amd64 `591e303e7269d335474f458c4c02ebc3466f381f0e85e83755326d33c1440acd`
- linux_arm64 `0a8ebe56de80bd90a06dfe39029eabddecd69f75ed8dbda762d327168052e91c`

The extracted linux/amd64 binary prints `oku eaad4ba (unknown, linux/amd64)`. The commit shows "unknown" because only `main.version` is injected. The darwin binaries were not run (no macOS host) and are unsigned. An earlier run at `ca84dac` is superseded.

#### Frontend

- Targeted vitest (26 tests), lint, and `vite build` with PostHog uploads disabled all passed at `e4c7845d1`. Superseded by the full suite at `bbfb56930`.
- Local Playwright Chromium against the Vite app at `/cli`:
  - HTTP 200, title `Oku CLI and Agents | Oku Trade`, heading and "Connect wallet" visible
  - enrollment controls hidden without a wallet, zero page errors
  - mobile 390x844 has no horizontal overflow

  No wallet was connected and there were no mocks.

#### Backend

- Enrollment: `VerifyEnrollment` (agentAddress + confirmationCode) runs before HL delegation. `ApproveEnrollment` takes `{enrollmentId}`. Read-only enrollment generates a local key for device proof but never requests Hyperliquid trading delegation.
- Trading-envelope signer binding is implemented.
- At `405b4538a`, DB tests and migration 137 were intentionally not run (operator-only). Migration 137 was later applied to a dedicated local database only.

#### Deployed read API

`POST https://accounts.lat.gfx.town/connect/gfxcafe.oku.account.v2.PerpsService/GetMarkets` with `Content-Type: application/json`, `Connect-Protocol-Version: 1`, body `{"coin":"BTC"}`. Returned HTTP 200, `markets[0].coin == "BTC"`, and decimal-string prices.

### Requirement → evidence matrix (v1, historical)

Evidence kinds: **S** = synthetic (unit tests with fixtures or fakes, `go test -race ./...` at `eaad4ba`, root `BG991879haiq`). **R** = real read-only run. **UI** = real unauthenticated UI. **NOT RUN** = no evidence. Synthetic tests do not show live integration.

| Requirement | Evidence | Kind |
|---|---|---|
| v1 command set matches the docs | Every `--help` captured at `ca84dac` and docs aligned. Final `eaad4ba` build passes `version --json`. | R (local binary) |
| JSON on stdout, diagnostics on stderr, error codes | `TestJSONContractAndStderr`, `TestErrorClassification` | S |
| Market data reads | Live `markets quote/list/funding BTC` (`BG663998t7rz`), `markets quote BTC` at `eaad4ba` (`BG991879haiq`), `GetMarkets` over Connect | R |
| Public account reads by address | Real binary at `eaad4ba` (2026-09-30 03:35:45-48 UTC) with `--account 0x0000000000000000000000000000000000000001`, run against the real Oku endpoint:<br>- `account summary` exit 0, returned balances, margin and builder-fee status<br>- `positions list`, `orders list`, `fills list` exit 0 with **empty** arrays<br><br>All returned `oku.cli/v1` with `ok:true` and source metadata. No credentials, signing or mutations. Parsing of non-empty positions, orders and fills was **not** exercised live. | R |
| Authenticated device reads (enrolled credentials) | Not exercised. Device routes are not deployed. | NOT RUN |
| Enrollment: read vs trade | `TestEnrollmentFlow`, `TestEnrollmentRefusesDifferentApprovedAccount`, `TestEnrollmentSignatureVector`, `TestCodeNeverAcceptedWithoutStdinOrTTY`. Backend clidevice `-race` (`BG8948497p4q`). The `/cli` page hides enrollment controls without a wallet. | S, UI |
| Live enrollment against deployed device routes | Deployment-boundary probe (2026-09-30 03:34:50 UTC): an unauthenticated `POST https://accounts.lat.gfx.town/connect/gfxcafe.oku.account.v2.CliDeviceService/GetCurrentDevice` with body `{}` and `Connect-Protocol-Version: 1` returned **HTTP 404** `404 page not found`. This confirms `CliDeviceService` is not deployed at the default endpoint. It is not an enrollment attempt. | NOT RUN (blocked: routes not deployed) |
| Network guard (mainnet only) | Real binary at `eaad4ba` (2026-09-30 03:35:20 UTC): `./bin/oku --network testnet markets quote BTC --json` returned `oku.cli/v1`, `ok:false`, `NETWORK_UNSUPPORTED`, `retryable:false`, exit 2 (checked with a shell assertion). | R (local binary) |
| Authority/account/network/signer binding | `TestDigestBindsAccountAndNetwork`, `TestProfileBindingEnforced`, `TestBindingSensitivity`, `TestWrongDigestRejectedBeforeNetwork`, `TestServerPayloadMismatchNeverSigned`, `TestValidateActionRejectsDivergence`, `TestSameIntentIDDifferentPayloadRejected`. Backend hlsigning `-race`. | S |
| Signing correctness and nonces | `TestReferenceVectors`, `TestBackendFrontendVectors`, `TestNonceUniqueUnderConcurrency`, `TestNonceMonotonicUnderClockRollback`, `TestCorruptNonceStateRefuses` | S |
| Secret protection | `TestSealRoundTripAndNoPlaintext`, `TestNoPassphraseFailsClosed`, `TestCorruptCredentialFilesFailClosed`, `TestErrorMessagesDoNotReflectSecrets`, `TestRedirectNotFollowedAndTokenNotLeaked`. Trusted-host risk documented in `security.md`. | S |
| Unknown outcome, no resend | `TestAmbiguousConfirmIsUnknownAndBlocksResend`, `TestTimeoutIsAmbiguous`, `TestSubmittedWithoutOrderIDStaysUnknown`, `TestUndecodableConfirmSuccessIsUnknown`, `TestUnknownPersistsAcrossReopen`, `TestAbandonOnlyNeverSigned`, `TestDefiniteRejectIsRejected` | S |
| Order bounds (slippage, decimals, perp rules) | `TestMarketOrderSlippageBound`, `TestMarketWorstPriceBound`, `TestPerpRules`, `TestEncodeRejectsAmbiguousInput`, `TestCancelValidation`. `orders prepare` run live-locally (digest + fee warning). | S, R (local) |
| Live submit/cancel/reconcile | No orders submitted | NOT RUN |
| Shared history with the Oku UI (CLI order visible and cancellable in the UI) | Architecture only (CreateAction/ConfirmAction/GetAction) | NOT RUN |
| Revocation boundaries (token vs agent vs orders vs positions) | `TestRevokeReportsBoundaries`. Wording in `security.md`. | S |
| Live revocation | | NOT RUN |
| Agent skills install/uninstall | `TestInstallNeverOverwritesWithoutForce`. Scratch install/uninstall run (`BG663998t7rz`, earlier single `oku-perps` skill). Codex path `~/.agents/skills` from source. v1 only. For the current 8 skills see the current milestone table. | S, R (local) |
| Packaging | `build.sh` run twice at `eaad4ba`, identical `SHA256SUMS`, linux/amd64 binary runs. That evidence predates CGO Darwin. `build.sh` now refuses to cross-build Darwin, so those darwin hashes no longer describe the release process. | R (local). darwin not executed. Ad-hoc signing only in the new flow, and not yet run natively. |
| Wallet-agnostic claim | No wallet connected in any test. No wallet compatibility matrix. **Do not claim.** | NOT RUN |
| Backend migration 137 / DB tests | Applied later to a dedicated local database only. Not applied to shared or deployed databases. Backend DB tests not recorded here. | R (local DB only) |
| Legal / eligibility / terms / release gates | No review performed. Deferred to Oku legal/compliance before any public release. | NOT RUN (deferred) |
