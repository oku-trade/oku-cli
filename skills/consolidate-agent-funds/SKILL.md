---
name: consolidate-agent-funds
description: Consolidate many tokens held by the Oku Agent EVM wallet across chains into one asset on one chain (fee sweep). Lists holdings per chain, swaps each token to a single asset per chain while keeping gas, bridges to a target chain preferring fast routes, handles Circle claims and verifies the final balance.
---

# Consolidate Oku Agent Funds

Use this for a fee sweep: protocol fees are sent to the agent wallet on several chains, then the agent turns them into one asset (usually USDC) on one target chain. Read `oku://skills/move-agent-funds` first; every rule there applies here. Confirm flags with `oku docs commands --json`.

## Safety rules (never skip)

- **Explicit user approval before every submit and every claim.** Show chain, input, output, minimum output, route, recipient and expiry for each one. Batch approval ("do all of them") is acceptable only if the user gave it for a listed plan of exact quotes.
- **Sequential only.** One unresolved onchain intent blocks new ones for the agent across **all chains**. Finish (confirmed, failed with nothing broadcast, or reconciled) each intent before preparing the next.
- **Never retry `OUTCOME_UNKNOWN`.** Run `oku swap status` / `oku bridge status` for that intent until it resolves. Never prepare a replacement.
- Recipient defaults to the agent wallet. Do not pass `--recipient` unless the user asks for an external destination and confirms the address.
- Intents expire about 3 minutes after prepare. Prepare only when the user is ready to approve; requote after expiry.
- Never sweep the last native gas on a chain. Swaps, sends and Circle claims all need gas on the chain they run on.
- Do not retry failed quotes in a loop; respect HTTP 429 backoff.

## 1. Sweep in

```sh
oku wallet address --json
oku wallet fund --chain CHAIN --token native --amount 0.002 --json   # optional gas top up; never signs
```

Send fees to the agent address from the fee contracts or treasury (outside the CLI). The address is the same on every EVM chain. Make sure each chain holds a little native gas. The first token approval batch on each chain upgrades the agent to an EIP-7702 Kernel account (same address); chains without EIP-7702 support can only do single-transaction routes (native input) there.

## 2. List holdings

```sh
oku wallet balances --chain CHAIN --json
oku wallet balances --chains 8453,10,137,42161 --json
```

Record every ERC20 with a nonzero balance and its contract address. Use contract addresses, not symbols, from here on (symbols such as USDT on BNB can be ambiguous). Ignore dust worth less than gas plus fees; tell the user what you skipped.

## 3. Swap each token to one asset per chain

Pick a target asset that bridges well, normally native USDC on that chain.

```sh
oku swap quote --chain CHAIN --from TOKEN_ADDRESS --to USDC --amount max --json
oku swap prepare --chain CHAIN --from TOKEN_ADDRESS --to USDC --amount max --json
oku swap submit --intent ID --digest SHA --json        # after explicit approval
oku swap status --intent ID --json
```

- `--amount max` uses the full balance. For native input it keeps a gas reserve (`--gas-reserve` overrides it). Never swap native to zero.
- `REJECTED: no eligible route`: the token is illiquid or unsupported. Report it and leave it. Do not raise slippage beyond the CLI maximum (300 bps) to force a fill.
- Fee-on-transfer or rebasing tokens may fail minimum output checks or leave dust. Requote with a slightly smaller explicit amount once, then report.
- Check the quote's output value against input value. A large loss means poor liquidity; ask before submitting.

## 4. Bridge to the target chain

```sh
oku bridge quote --chain CHAIN --destination-chain TARGET --from USDC --to USDC --amount max --json
oku bridge quote --chain CHAIN --destination-chain TARGET --from USDC --to USDC --amount max --route circleFast --json
oku bridge prepare --chain CHAIN --destination-chain TARGET --from USDC --to USDC --amount max --route ROUTE --json
oku bridge submit --intent ID --digest SHA --json      # after explicit approval
oku bridge status --intent ID --json
```

- The default route is the best net value, not the fastest. For USDC it often picks `circleStandard` (15-20 minutes and usually a destination claim). Prefer passing `--route` explicitly: `circleFast`, `across` or `relay` when they quote, and compare net output against the default. Show the user both if they differ materially.
- Native to native works with `--from native --to native` (not the `ETH` symbol).
- Do not bridge the same chain's last gas.

## 5. Claims

Poll `oku bridge status --intent ID --json` until `destination_fill.status` is `filled` or `claim_required` is true. If a claim is required, the agent needs native gas on the **target** chain. If it has none, fund it with `oku wallet fund --chain TARGET --token native` and wait for the user. Then, with approval, run `oku bridge claim --intent ID --json`. It is idempotent and never sends twice. Statuses `refunded`, `failed`, `expired`: report and stop, never re-bridge automatically.

## 6. Verify

```sh
oku wallet balances --chain TARGET --json
```

Report: per chain, what was swapped, skipped (no route or dust) and bridged; intent IDs and transaction hashes; final target balance; any open claims or unknown outcomes. Leftover gas on source chains is expected.
