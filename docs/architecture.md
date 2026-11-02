# Architecture

```
human terminal ──> oku auth login ──> Privy device grant ──> Oku /agent/authorize
                                                    │
external master wallet ──approveAgent───────────────┤
                                                    ▼
Oku Agent (Go) ──independent agent session──> Accounts AuthService + PerpsService──> Hyperliquid
       │
       ├─ Privy embedded Ethereum agent wallet RPC (typed-data signing;
       │    eth_sendTransaction and wallet_sendCalls for agent-wallet funds)
       ├─ EVM JSON-RPC (balances, gas, receipts, batch lookup by nonce)
       ├─ encrypted credential / macOS Keychain (tokens only; no wallet private key)
       └─ SQLite journal (intents, nonces, signing/submission states)
```

Module `github.com/oku-trade/oku-cli`, binary `oku`, JSON schema `oku.cli/v1`. `pkg/cli` owns commands, profile/session renewal and signing integration. `pkg/privy` owns OAuth device grant, refresh, HPKE wallet authorization and signed wallet RPC. `pkg/auth` stores profile-bound credentials and takes a cross-process lock over refresh-token rotation. `pkg/api` wraps generated Connect clients for Accounts AuthService and PerpsService. `pkg/signing` computes the Hyperliquid action hash and typed-data digest and recovers signatures. `pkg/journal` owns SQLite persistence, including onchain intents for agent-wallet funds. `pkg/onchain` executes verified onchain intents: preflight checks, single transactions or one atomic EIP-7702 batch, and receipt confirmation. `pkg/mcp` exposes a read-only-by-default agent interface but never interactive login/logout.

## Login and session renewal

Run `oku auth login` without an account address. Login prints the Privy device authorization URL and user code, then polls until approved. The wallet signed in the browser on the Oku authorization page is the Hyperliquid profile account, not the primary Oku site identity. The page pairs a separate embedded Ethereum agent wallet with that wallet and obtains Hyperliquid agent approval. After Privy authentication, the CLI reads the agent's master through Hyperliquid `userRole` and uses it as the profile account, creating a `default` profile when necessary or updating an existing profile. It stores nothing if the role is not an agent. It then exchanges the Privy JWT for an independent Oku Agent session, never a primary site session. On authenticated calls it locks and reloads the credential, refreshes a near-expiry Privy token and persists the new rotating token **before** proceeding, and extends or re-exchanges an expiring Oku session. Signing access is requested lazily only for submit.

### Accounts fee association

The Hyperliquid master address, embedded agent address, primary Oku site user and independent Privy-based Oku Agent user are distinct. Hyperliquid approval alone does not associate Accounts fee histories. In the proposed frontend flow, an explicit user action keeps the authenticated primary site session intact. `PrivySessionExchange` exchanges the agent's Privy token into its own `privy_<id>` session, never a primary-account session. The page uses **only the agent session** for `EvmChallenge` / `EvmSolve` to prove control of the embedded wallet and checks the returned agent identity. It then calls `AssociateAgentWallet` with the verified primary site's authentication plus the agent Privy token and wallet address. Accounts derives the primary user from its verified session, confirms the independent agent wallet association for fees, and returns the agent user ID and address, not a session. The old `linkToCurrentUser: true` exchange is rejected.

The association shares historical volume-based fee tiers **both ways**: quotes for the primary and associated agent include both users' orders, including orders placed before association, without rewriting either history. Group free-trade accounting is shared rather than multiplied per agent. Address-specific exemptions remain address-specific. This is not account login linking or a merge. The agent session cannot access the primary site's sessions, settings, balances or order history. Existing CLI device-grant exchanges and stored sessions remain standalone agent sessions, without logout or re-login to adopt a primary identity. Wallet funds, Hyperliquid master approval and Privy device grants remain separate permissions. There is no merge or unlink flow in this scope. A conflicting wallet owner is rejected. If setup fails after association but before terminal approval, fee sharing may already be active, so retry authorization rather than assuming approval.

This describes the planned integration, not a live rollout. Deploy the [Accounts contract and migration](https://github.com/oku-trade/account/pull/715) first, then enable the [Trade authorization flow](https://github.com/oku-trade/trade/pull/4246) alongside these [CLI docs](https://github.com/oku-trade/oku-cli/pull/32). Accounts migration `155_identity_agent_wallets.sql` has release-order collision gates to resolve before deployment. Editing these repositories does not deploy services or migrate historical data.

## Submission lifecycle

`orders prepare` writes a local intent and digest. `orders submit` requests a server-built action and validates it against the intent. The journal allocates and burns a nonce and holds the account gate in `signing`. Privy signs the Hyperliquid Agent EIP-712 typed data (`Exchange/1/1337/zero address`), and the CLI recovers the returned signature to the stored agent address. Failure or a crash-left `signing` state returns to `created` (after a grace period to protect a concurrent signer); a fresh retry gets a new nonce. After signature verification and a second intent-expiry check, state becomes `submitting` and the CLI calls `ConfirmAction` exactly once. Ambiguous confirm outcomes remain unresolved until `orders reconcile`; neither signing again nor a blind confirm retry is safe.

The journal gate is machine-local. Sharing one embedded agent across machines can cause nonce collisions or simultaneous exposure. Revoking an Oku session or Privy device grant does not remove Hyperliquid agent approval.

## Agent-wallet funds lifecycle

`send`, `deposit` and `swap` share one onchain journal. `prepare` records an intent binding chain, recipient, token, exact amount and (for swaps) the Oku Router route and minimum output, and returns a digest. Swap quotes come from the backend `GetFlowstreamQuotes` long-poll. `submit` re-fetches execution info, decodes and verifies the router calldata and exact approval, seals the final transactions into the intent, and marks it `sending` (one unresolved onchain intent per agent blocks the next). A single step is sent with Privy `eth_sendTransaction`. Approve+swap is sent as one atomic `wallet_sendCalls` batch: the agent nonce and block are journaled before the handoff, and because Privy returns only a batch ID, the mined transaction is located by that nonce and accepted only if it decodes to exactly the journaled Kernel calls. Failures before the handoff close the intent; anything ambiguous after it stays `unknown` until `status` or `reconcile` resolves it from chain evidence.

## Current capabilities

The CLI supports Privy login, Hyperliquid order preparation and submission, agent-wallet sends, swaps and bridges. Onchain submission requires a user-configured RPC for each chain. Check the command reference for the available operations and their authorization requirements.
