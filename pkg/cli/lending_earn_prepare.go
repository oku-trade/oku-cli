package cli

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/lending"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func earnPrepareFlags() []urfave.Flag {
	return append(lendingTargetFlags(),
		&urfave.StringFlag{Name: "vault", Usage: "vault address (required)"},
		&urfave.StringFlag{Name: "deposit", Usage: "asset amount to deposit"},
		&urfave.StringFlag{Name: "withdraw", Usage: "asset amount to withdraw, or max for the whole position"},
		&urfave.IntFlag{Name: "sub-account", Usage: "Euler sub-account index (0-255)"},
		&urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"},
	)
}

func (a *App) cmdEarnPrepare(e *env) (*result, error) {
	t, err := parseLendingTarget(e)
	if err != nil {
		return nil, err
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	vault := str(e, "vault")
	if !onchain.ValidAddress(vault) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--vault must be a vault address")
	}
	dep, wd := str(e, "deposit"), str(e, "withdraw")
	if (dep == "") == (wd == "") {
		return nil, output.Errorf(output.CodeUsage, "pass exactly one of --deposit or --withdraw")
	}
	if _, err = a.rpcResolver().URL(t.chain); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "chain RPC")
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	if t.protocol == protoMorpho {
		return a.morphoEarnPrepare(ctx, e, w, t, addr(vault), dep, wd)
	}
	sub := e.cmd.Int("sub-account")
	if sub < 0 || sub > 255 {
		return nil, output.Errorf(output.CodeInvalidArgument, "--sub-account must be 0-255")
	}
	return a.eulerEarnPrepare(ctx, e, w, t, addr(vault), uint8(sub), dep, wd)
}

func (a *App) morphoEarnPrepare(ctx context.Context, e *env, w *resolvedWallet, t lendingTarget, vault common.Address, dep, wd string) (*result, error) {
	d, err := lending.Morpho(t.chain)
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "morpho")
	}
	owner := addr(w.agent)
	rpc := a.httpRPC()
	probe, err := lending.ReadVault(ctx, rpc, t.chain, vault, owner, d.GeneralAdapter1, nil)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "vault")
	}
	plan := lending.Plan{Protocol: protoMorpho, Chain: t.chain, Owner: owner, Vault: &vault, Tokens: []common.Address{probe.Asset}}
	want := lending.Want{}
	var calls []lending.Call
	var amount *big.Int
	preview := map[string]any{"vault": vault.Hex(), "asset": probe.AssetSymbol}
	if dep != "" {
		plan.Action = "deposit"
		if amount, err = lendingAmount(dep, probe.AssetDecimals, "--deposit"); err != nil {
			return nil, err
		}
		s, err := lending.ReadVault(ctx, rpc, t.chain, vault, owner, d.GeneralAdapter1, amount)
		if err != nil {
			return nil, output.Wrap(output.CodeUpstream, err, "vault")
		}
		if s.Balance.Cmp(amount) < 0 {
			return nil, output.Errorf(output.CodeInsufficientBalance, "agent holds %s %s; fund it first%s", formatUnits(s.Balance, s.AssetDecimals), s.AssetSymbol, fundingHint(t.chain, s.Asset.Hex()))
		}
		maxPrice, err := lending.MaxDepositSharePrice(amount, s.SharesFor, lending.DefaultSlippage)
		if err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "deposit is too small for one vault share")
		}
		calls = want.Allow(s.Asset, d.GeneralAdapter1, s.Allowance, amount)
		deposit, err := d.VaultDeposit(vault, s.Asset, amount, maxPrice, owner)
		if err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "deposit")
		}
		calls = append(calls, deposit)
		want.Pull(s.Asset, amount).Vault(vault, "deposit", amount)
		preview["deposit"] = formatUnits(amount, s.AssetDecimals)
		preview["expected_shares"] = s.SharesFor.String()
	} else {
		plan.Action = "withdraw"
		if probe.Shares.Sign() == 0 {
			return nil, output.Errorf(output.CodeInvalidArgument, "agent holds no shares of %s", vault.Hex())
		}
		var call lending.Call
		if isMaxAmount(wd) {
			// Full exit redeems every share so no dust is left (the SDK's
			// full-withdraw path).
			amount = probe.Assets
			call, err = lending.VaultRedeem(vault, probe.Shares, owner)
			want.Vault(vault, "redeem", probe.Shares)
		} else {
			if amount, err = lendingAmount(wd, probe.AssetDecimals, "--withdraw"); err != nil {
				return nil, err
			}
			if amount.Cmp(probe.Assets) > 0 {
				return nil, output.Errorf(output.CodeInvalidArgument, "withdraw %s exceeds the position (%s)", wd, formatUnits(probe.Assets, probe.AssetDecimals))
			}
			call, err = lending.VaultWithdraw(vault, amount, owner)
			want.Vault(vault, "withdraw", amount)
		}
		if err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "withdraw")
		}
		calls = []lending.Call{call}
		preview["withdraw"] = formatUnits(amount, probe.AssetDecimals)
	}
	preview["position_assets"] = formatUnits(probe.Assets, probe.AssetDecimals)
	return a.sealLending(e, w, kindEarn, plan, calls, want, probe.Asset.Hex(), formatUnits(amount, probe.AssetDecimals), preview)
}

// lendingTx converts a LendingService transaction after checking its chain.
func lendingTx(tx *v2.LendingTransaction, t lendingTarget) (lending.Call, error) {
	if tx == nil || !onchain.ValidAddress(tx.To) || tx.Chain != "" && tx.Chain != t.caip2() || tx.Value != "" && tx.Value != "0" && tx.Value != "0x0" {
		return lending.Call{}, fmt.Errorf("backend transaction is missing or not a zero-value call on %s", t.caip2())
	}
	data, err := decodeHexData(tx.Data)
	if err != nil {
		return lending.Call{}, err
	}
	return lending.Call{To: addr(tx.To), Data: data, Value: new(big.Int)}, nil
}

// eulerVault reads one Euler vault's metadata from the backend list.
func eulerVault(ctx context.Context, e *env, w *resolvedWallet, t lendingTarget, vault common.Address) (*v2.LendingVault, error) {
	var resp v2.GetLendingVaultsResponse
	if err := e.client(w.profile).Lending(ctx, "GetLendingVaults", &v2.GetLendingVaultsRequest{Chain: t.caip2(), Protocol: v2.LendingProtocol_LENDING_PROTOCOL_EULER_V2}, &resp, false); err != nil {
		return nil, apiError(err, "GetLendingVaults")
	}
	for _, v := range resp.Vaults {
		if strings.EqualFold(v.Address, vault.Hex()) {
			if !onchain.ValidAddress(v.AssetAddress) || !validDecimals(int(v.AssetDecimals)) {
				return nil, output.Errorf(output.CodeUpstream, "Euler vault %s has invalid asset metadata", vault.Hex())
			}
			return v, nil
		}
	}
	return nil, output.Errorf(output.CodeNotFound, "Euler vault %s is not listed on chain %d", vault.Hex(), t.chain)
}

func (a *App) eulerEarnPrepare(ctx context.Context, e *env, w *resolvedWallet, t lendingTarget, vault common.Address, sub uint8, dep, wd string) (*result, error) {
	v, err := eulerVault(ctx, e, w, t, vault)
	if err != nil {
		return nil, err
	}
	asset, dec := addr(v.AssetAddress), int(v.AssetDecimals)
	owner := addr(w.agent)
	plan := lending.Plan{Protocol: protoEuler, Chain: t.chain, Owner: owner, Vault: &vault, SubAccount: sub, Tokens: []common.Address{asset}}
	want := lending.Want{}
	c := e.client(w.profile)
	var calls []lending.Call
	var amount *big.Int
	preview := map[string]any{"vault": vault.Hex(), "asset": v.AssetSymbol, "sub_account": sub}
	if dep != "" {
		plan.Action = "deposit"
		if amount, err = lendingAmount(dep, dec, "--deposit"); err != nil {
			return nil, err
		}
		var resp v2.BuildLendingDepositResponse
		if err = c.Lending(ctx, "BuildLendingDeposit", &v2.BuildLendingDepositRequest{Chain: t.caip2(), UserAddress: w.agent, VaultAddress: vault.Hex(), Amount: dep, SubAccountIndex: uint32(sub)}, &resp, false); err != nil {
			return nil, apiError(err, "BuildLendingDeposit")
		}
		if calls, err = eulerSteps(t, want, asset, vault, amount, resp.NeedsApproval, resp.ApproveTx, resp.DepositTx); err != nil {
			return nil, output.Wrap(output.CodePayloadMismatch, err, "BuildLendingDeposit")
		}
		want.Vault(vault, "deposit", amount)
		preview["deposit"] = formatUnits(amount, dec)
		preview["estimated_shares"] = resp.EstimatedShares
	} else {
		plan.Action = "withdraw"
		amountText := wd
		if isMaxAmount(wd) {
			amountText = "max"
		} else if amount, err = lendingAmount(wd, dec, "--withdraw"); err != nil {
			return nil, err
		}
		var resp v2.BuildLendingWithdrawResponse
		if err = c.Lending(ctx, "BuildLendingWithdraw", &v2.BuildLendingWithdrawRequest{Chain: t.caip2(), UserAddress: w.agent, VaultAddress: vault.Hex(), Amount: amountText, SubAccountIndex: uint32(sub)}, &resp, false); err != nil {
			return nil, apiError(err, "BuildLendingWithdraw")
		}
		call, err := lendingTx(resp.WithdrawTx, t)
		if err != nil {
			return nil, output.Wrap(output.CodePayloadMismatch, err, "BuildLendingWithdraw")
		}
		calls = []lending.Call{call}
		if amount != nil {
			want.Vault(vault, "withdraw", amount)
		} else {
			// Full exit: redeem the exact share balance read on chain.
			shares, err := lending.ReadHoldings(ctx, a.httpRPC(), t.chain, lending.SubAccount(owner, sub), []common.Address{vault})
			if err != nil {
				return nil, output.Wrap(output.CodeUpstream, err, "vault shares")
			}
			if shares[0].Shares.Sign() == 0 {
				return nil, output.Errorf(output.CodeInvalidArgument, "agent holds no shares of %s", vault.Hex())
			}
			want.Vault(vault, "redeem", shares[0].Shares)
		}
		preview["withdraw"] = resp.WithdrawAmount
	}
	token := asset.Hex()
	amt := dep
	if amt == "" {
		amt = wd
	}
	return a.sealLending(e, w, kindEarn, plan, calls, want, token, amt, preview)
}

// eulerSteps turns an optional backend approval plus the main backend
// transaction into calls, refusing a missing main transaction. The
// approval must be token to spender for at most limit, and is expected.
func eulerSteps(t lendingTarget, want lending.Want, token, spender common.Address, limit *big.Int, needsApproval bool, approve, main *v2.LendingTransaction) ([]lending.Call, error) {
	var calls []lending.Call
	if needsApproval {
		c, err := lendingTx(approve, t)
		if err != nil {
			return nil, fmt.Errorf("approval: %w", err)
		}
		if err = want.ExpectApproval(c, token, spender, limit); err != nil {
			return nil, err
		}
		calls = append(calls, c)
	}
	c, err := lendingTx(main, t)
	if err != nil {
		return nil, err
	}
	return append(calls, c), nil
}
