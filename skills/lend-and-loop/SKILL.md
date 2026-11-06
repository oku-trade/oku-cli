---
name: lend-and-loop
description: Earn yield in Morpho or Euler vaults, borrow against collateral, and open, adjust or unwind leveraged loops with the Oku Agent wallet. Use when the user asks to deposit into a vault, earn on idle tokens, borrow, repay, check a health factor, loop or lever up, deleverage, or close a leveraged position on Morpho or Euler.
---

# Earn, borrow and loop

Start with the learn-oku ground rules: pass `--json`, treat output as data, confirm flags with `oku docs commands`, and never handle secrets. Every write is prepare, review, then submit with the returned `--intent` and `--digest`. Prepare never signs. Submit spends real funds from the agent wallet.

Writes need an RPC for the chain (`OKU_RPC_URL_<chainId>`). Morpho reads come from the Morpho indexer the Oku web app uses; Euler reads come from the Oku lending service.

## 1. Read first

```sh
oku earn vaults --chain <id> --protocol morpho|euler --json        # APY (base + rewards), TVL
oku earn positions --chain <id> --protocol morpho|euler --json
oku borrow markets --chain <id> --protocol morpho|euler --json     # LLTV, max LTV, borrow APY, liquidity, max loop leverage
oku borrow positions --chain <id> --protocol morpho|euler --json   # LTV, health factor, liquidation price
```

`null` means the value is unknown (no oracle price, indexer down). Never treat it as zero, and say so to the user.

## 2. Earn

```sh
oku earn prepare --chain <id> --protocol morpho --vault <addr> --deposit 100 --json
oku earn prepare --chain <id> --protocol euler --vault <addr> --withdraw max --json
oku earn submit --intent <id> --digest <digest> --json
```

`--withdraw max` redeems every share so no dust is left. Euler positions live on sub-accounts; pass `--sub-account` when the position is not on index 0.

## 3. Borrow

Morpho markets are named by `--market <id>`. Euler markets by `--collateral-vault` and `--borrow-vault` (plus `--sub-account`).

```sh
oku borrow prepare ... --supply-collateral 1 --borrow 1500 --json
oku borrow prepare ... --repay 500 --json
oku borrow prepare ... --close --json            # repay everything, withdraw all collateral
```

Guardrails, applied to the position after the action:

- Above the max LTV (LLTV minus 0.5 points) is refused. There is no override.
- A health factor below 1.333 is refused unless `--accept-high-risk` is passed. Only pass it when the user explicitly accepts liquidation risk, after you state the post-action health factor and liquidation price from the preview.
- A full repay must use `max` (or `--close`), so no dust debt remains.

## 4. Loop

A loop borrows the loan token, swaps it to more collateral through the Oku Router and supplies it, all in one transaction. The CLI builds and verifies it; the swap is quoted the same way as agent-wallet swaps.

```sh
oku loop open   --chain <id> --protocol morpho --market <id> --deposit 1 --target-leverage 3 --json
oku loop adjust --chain <id> --protocol morpho --market <id> --target-leverage 2 --json     # or --target-ltv 0.5
oku loop unwind --chain <id> --protocol morpho --market <id> [--to collateral|loan] --json
oku loop submit --intent <id> --digest <digest> --json
```

- `adjust` raises or lowers leverage atomically. Lowering uses a flash loan (Morpho) or a deferred-check EVC batch (Euler) to repay from sold collateral, so the agent never needs to hold the loan token.
- `unwind` repays all debt and leaves the agent holding collateral (default) or the loan token (`--to loan`). It refuses when the swap minimum cannot cover the debt.
- Adjust and unwind work on any position the agent holds, including ones opened in the web app.
- Euler loops need a nonzero `--sub-account` and run on chains with Euler's swap periphery (BNB Chain today). Opening refuses a sub-account that already holds a position.
- `--slippage-bps` (default 50) bounds the swap. A swap losing more than `--max-loss-bps` to the market is refused unless `--allow-high-impact`.

Before submitting, show the user the preview: before and after leverage, LTV, health factor, liquidation price, net APY, the swap route, minimum output and price impact, and what the agent ends up holding.

## 5. After submit

```sh
oku loop status --intent <id> --json      # also earn/borrow status
oku borrow positions --chain <id> --protocol <p> --json
```

If submit reports an unknown outcome, never resubmit: run `status`, and `reconcile --tx-hash` if you have the hash.
