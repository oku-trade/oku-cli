package cli

import (
	"context"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	gmath "github.com/ethereum/go-ethereum/common/math"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/lending"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func borrowPrepareFlags() []urfave.Flag {
	return append(lendingTargetFlags(),
		&urfave.StringFlag{Name: "market", Usage: "Morpho market id"},
		&urfave.StringFlag{Name: "collateral-vault", Usage: "Euler collateral vault"},
		&urfave.StringFlag{Name: "borrow-vault", Usage: "Euler borrow vault"},
		&urfave.IntFlag{Name: "sub-account", Usage: "Euler sub-account index (0-255) of an existing position"},
		&urfave.StringFlag{Name: "supply-collateral", Usage: "collateral amount to add"},
		&urfave.StringFlag{Name: "borrow", Usage: "loan amount to borrow"},
		&urfave.StringFlag{Name: "repay", Usage: "loan amount to repay, or max for the whole debt"},
		&urfave.StringFlag{Name: "withdraw-collateral", Usage: "collateral amount to withdraw, or max"},
		&urfave.BoolFlag{Name: "close", Usage: "repay all debt and withdraw all collateral"},
		&urfave.BoolFlag{Name: "accept-high-risk", Usage: "allow a post-action health factor below 1.333 (never above the max LTV)"},
		&urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"},
	)
}

// borrowRequest is a parsed borrow action. The combinations mirror the web
// app: supply and/or borrow, or repay and/or withdraw, never both sides.
type borrowRequest struct {
	supply, borrow, repay, withdraw string
}

func (r borrowRequest) increases() bool { return r.supply != "" || r.borrow != "" }
func (r borrowRequest) decreases() bool { return r.repay != "" || r.withdraw != "" }

func (r borrowRequest) action() string {
	var parts []string
	for _, p := range []struct{ name, v string }{{"supply-collateral", r.supply}, {"borrow", r.borrow}, {"repay", r.repay}, {"withdraw-collateral", r.withdraw}} {
		if p.v != "" {
			parts = append(parts, p.name)
		}
	}
	return strings.Join(parts, "+")
}

func parseBorrowRequest(e *env) (borrowRequest, error) {
	r := borrowRequest{str(e, "supply-collateral"), str(e, "borrow"), str(e, "repay"), str(e, "withdraw-collateral")}
	if e.cmd.Bool("close") {
		if r.increases() || r.decreases() {
			return r, output.Errorf(output.CodeUsage, "--close cannot be combined with amounts")
		}
		r.repay, r.withdraw = "max", "max"
	}
	if !r.increases() && !r.decreases() {
		return r, output.Errorf(output.CodeUsage, "pass --supply-collateral and/or --borrow, or --repay and/or --withdraw-collateral, or --close")
	}
	if r.increases() && r.decreases() {
		return r, output.Errorf(output.CodeUsage, "increase (supply/borrow) and decrease (repay/withdraw) are separate actions")
	}
	return r, nil
}

func (a *App) cmdBorrowPrepare(e *env) (*result, error) {
	t, err := parseLendingTarget(e)
	if err != nil {
		return nil, err
	}
	req, err := parseBorrowRequest(e)
	if err != nil {
		return nil, err
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	if _, err = onchain.RPCURL(a.Getenv, t.chain); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "chain RPC")
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	if t.protocol == protoMorpho {
		id := str(e, "market")
		if len(id) != 66 || !strings.HasPrefix(id, "0x") {
			return nil, output.Errorf(output.CodeInvalidArgument, "--market must be a Morpho market id")
		}
		return a.morphoBorrowPrepare(ctx, e, w, t, common.HexToHash(id), req)
	}
	cv, bv := str(e, "collateral-vault"), str(e, "borrow-vault")
	if !onchain.ValidAddress(cv) || !onchain.ValidAddress(bv) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--collateral-vault and --borrow-vault are required for Euler")
	}
	sub := e.cmd.Int("sub-account")
	if sub < 0 || sub > 255 {
		return nil, output.Errorf(output.CodeInvalidArgument, "--sub-account must be 0-255")
	}
	return a.eulerBorrowPrepare(ctx, e, w, t, addr(cv), addr(bv), uint8(sub), req)
}

// guard applies the shared LTV and health-factor guardrails to the
// post-action position. Debt-free results always pass.
func guard(e *env, r lending.Risk) error {
	if err := lending.Check(r, e.cmd.Bool("accept-high-risk")); err != nil {
		return output.Wrap(output.CodeRejected, err, "guardrail")
	}
	return nil
}

func riskPreview(before, after lending.Risk) map[string]any {
	view := func(r lending.Risk) map[string]any {
		out := map[string]any{"lltv": r.LLTV, "max_ltv": r.MaxLTV, "ltv": r.LTV, "health_factor": optPtr(r.HealthFactor), "liquidation_price": optPtr(r.LiquidationPrice)}
		if r.HealthFactor == nil {
			out["ltv"] = 0.0
		}
		return out
	}
	return map[string]any{"before": view(before), "after": view(after)}
}

func (a *App) morphoBorrowPrepare(ctx context.Context, e *env, w *resolvedWallet, t lendingTarget, id common.Hash, req borrowRequest) (*result, error) {
	d, err := lending.Morpho(t.chain)
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "morpho")
	}
	owner := addr(w.agent)
	s, err := lending.ReadMarket(ctx, onchain.HTTPRPC{Getenv: a.Getenv}, d, id, owner)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "morpho market")
	}
	if s.OraclePrice == nil {
		return nil, output.Errorf(output.CodeUpstream, "market oracle price unavailable; refusing a borrow action without a price")
	}
	mp := s.Params
	now := a.Now().Unix()
	accrued := s.Market.AccrueBorrow(s.BorrowRate, lending.AccrualHorizon(now))
	debt := s.Debt(now)
	price := lending.MorphoPrice(s.OraclePrice, s.CollateralDecimals, s.LoanDecimals)
	lltv := lending.Float(mp.Lltv, 18)
	position := func(coll, debt *big.Int) lending.Position {
		return lending.Position{Collateral: lending.Float(coll, s.CollateralDecimals), Debt: lending.Float(debt, s.LoanDecimals), Price: price, LLTV: lltv}
	}
	before, err := lending.Assess(position(s.Position.Collateral, debt))
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "current position")
	}
	plan := lending.Plan{Protocol: protoMorpho, Action: req.action(), Chain: t.chain, Owner: owner, MarketID: &id, Tokens: []common.Address{mp.LoanToken, mp.CollateralToken}}
	want := lending.Want{}
	coll, newDebt := new(big.Int).Set(s.Position.Collateral), new(big.Int).Set(debt)
	var calls []lending.Call
	preview := map[string]any{"market": id.Hex(), "collateral": s.CollateralSymbol, "loan": s.LoanSymbol}
	if req.increases() {
		var supply, borrow *big.Int
		if req.supply != "" {
			if supply, err = lendingAmount(req.supply, s.CollateralDecimals, "--supply-collateral"); err != nil {
				return nil, err
			}
			if s.CollateralBalance.Cmp(supply) < 0 {
				return nil, output.Errorf(output.CodeInsufficientBalance, "agent holds %s %s%s", formatUnits(s.CollateralBalance, s.CollateralDecimals), s.CollateralSymbol, fundingHint(t.chain, mp.CollateralToken.Hex()))
			}
			coll.Add(coll, supply)
			calls = append(calls, want.Allow(mp.CollateralToken, d.GeneralAdapter1, s.CollateralAllow, supply)...)
			want.Pull(mp.CollateralToken, supply).Supply(id, supply)
			preview["supply_collateral"] = formatUnits(supply, s.CollateralDecimals)
		}
		if req.borrow != "" {
			if borrow, err = lendingAmount(req.borrow, s.LoanDecimals, "--borrow"); err != nil {
				return nil, err
			}
			liquidity := new(big.Int).Sub(s.Market.TotalSupplyAssets, s.Market.TotalBorrowAssets)
			if borrow.Cmp(liquidity) > 0 {
				return nil, output.Errorf(output.CodeRejected, "borrow exceeds market liquidity (%s %s)", formatUnits(liquidity, s.LoanDecimals), s.LoanSymbol)
			}
			newDebt.Add(newDebt, borrow)
			if !s.Authorized {
				calls = append(calls, d.AuthorizeAdapter())
				want.Flag("authorize")
			}
			want.Borrow(id, borrow)
			preview["borrow"] = formatUnits(borrow, s.LoanDecimals)
		}
		var call lending.Call
		switch {
		case supply != nil && borrow != nil:
			min, merr := lending.MinBorrowSharePrice(borrow, accrued, lending.DefaultSlippage)
			if merr != nil {
				return nil, output.Wrap(output.CodeInvalidArgument, merr, "borrow")
			}
			call, err = d.SupplyCollateralBorrow(mp, supply, borrow, min, owner)
		case supply != nil:
			call, err = d.SupplyCollateral(mp, supply, owner)
		default:
			min, merr := lending.MinBorrowSharePrice(borrow, accrued, lending.DefaultSlippage)
			if merr != nil {
				return nil, output.Wrap(output.CodeInvalidArgument, merr, "borrow")
			}
			call, err = d.Borrow(mp, borrow, min, owner)
		}
		if err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "borrow action")
		}
		calls = append(calls, call)
	} else {
		var repay lending.Repay
		var withdraw *big.Int
		if req.repay != "" {
			if debt.Sign() == 0 {
				return nil, output.Errorf(output.CodeInvalidArgument, "position has no debt to repay")
			}
			if isMaxAmount(req.repay) {
				// Full repay by shares, funded with the debt accrued over the
				// SDK's horizon; the unused remainder is skimmed back.
				transfer := accrued.ToBorrowAssetsUp(s.Position.BorrowShares)
				maxPrice, merr := lending.MaxRepaySharePrice(nil, s.Position.BorrowShares, accrued, lending.DefaultSlippage)
				if merr != nil {
					return nil, output.Wrap(output.CodeInvalidArgument, merr, "repay")
				}
				repay = lending.Repay{Shares: s.Position.BorrowShares, Transfer: transfer, MaxSharePrice: maxPrice}
				newDebt.SetInt64(0)
				preview["repay"] = formatUnits(transfer, s.LoanDecimals) + " (max, by shares; unused remainder returned)"
			} else {
				amt, aerr := lendingAmount(req.repay, s.LoanDecimals, "--repay")
				if aerr != nil {
					return nil, aerr
				}
				if amt.Cmp(debt) >= 0 {
					return nil, output.Errorf(output.CodeInvalidArgument, "repay %s covers the whole debt; use --repay max so no dust debt remains", req.repay)
				}
				maxPrice, merr := lending.MaxRepaySharePrice(amt, nil, accrued, lending.DefaultSlippage)
				if merr != nil {
					return nil, output.Wrap(output.CodeInvalidArgument, merr, "repay")
				}
				repay = lending.Repay{Transfer: amt, MaxSharePrice: maxPrice}
				newDebt.Sub(newDebt, amt)
				preview["repay"] = req.repay
			}
			if s.LoanBalance.Cmp(repay.Transfer) < 0 {
				return nil, output.Errorf(output.CodeInsufficientBalance, "agent holds %s %s, repay needs %s%s", formatUnits(s.LoanBalance, s.LoanDecimals), s.LoanSymbol, formatUnits(repay.Transfer, s.LoanDecimals), fundingHint(t.chain, mp.LoanToken.Hex()))
			}
			calls = append(calls, want.Allow(mp.LoanToken, d.GeneralAdapter1, s.LoanAllowance, repay.Transfer)...)
			want.Pull(mp.LoanToken, repay.Transfer).Repaid(id)
		}
		if req.withdraw != "" {
			if isMaxAmount(req.withdraw) {
				withdraw = new(big.Int).Set(s.Position.Collateral)
			} else if withdraw, err = lendingAmount(req.withdraw, s.CollateralDecimals, "--withdraw-collateral"); err != nil {
				return nil, err
			}
			if withdraw.Sign() == 0 || withdraw.Cmp(s.Position.Collateral) > 0 {
				return nil, output.Errorf(output.CodeInvalidArgument, "withdraw exceeds the supplied collateral (%s)", formatUnits(s.Position.Collateral, s.CollateralDecimals))
			}
			coll.Sub(coll, withdraw)
			want.Withdraw(id, withdraw)
			preview["withdraw_collateral"] = formatUnits(withdraw, s.CollateralDecimals)
		}
		var call lending.Call
		switch {
		case repay.Transfer != nil && withdraw != nil:
			if !s.Authorized {
				calls = append(calls, d.AuthorizeAdapter())
				want.Flag("authorize")
			}
			call, err = d.RepayWithdrawCollateral(mp, repay, withdraw, owner)
		case repay.Transfer != nil:
			call, err = d.RepayOnly(mp, repay, owner)
		default:
			call, err = d.WithdrawCollateral(mp, withdraw, owner)
		}
		if err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "repay action")
		}
		calls = append(calls, call)
	}
	after, err := lending.Assess(position(coll, newDebt))
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "projected position")
	}
	if err = guard(e, after); err != nil {
		return nil, err
	}
	plan.Risk = &after
	plan.Guard = lending.NewGuard(s.Position.Collateral, debt, coll, newDebt, e.cmd.Bool("accept-high-risk"))
	preview["risk"] = riskPreview(before, after)
	return a.sealLending(e, w, kindBorrow, plan, calls, want, mp.LoanToken.Hex(), req.action(), preview)
}

func eulerMarket(ctx context.Context, e *env, t lendingTarget, cv, bv common.Address) (*v2.LendingMarket, error) {
	markets, err := eulerMarketList(ctx, e, t)
	if err != nil {
		return nil, err
	}
	for _, m := range markets {
		if strings.EqualFold(m.CollateralVaultAddress, cv.Hex()) && strings.EqualFold(m.BorrowVaultAddress, bv.Hex()) {
			if !onchain.ValidAddress(m.CollateralAssetAddress) || !onchain.ValidAddress(m.BorrowAssetAddress) || !validDecimals(int(m.CollateralAssetDecimals)) || !validDecimals(int(m.BorrowAssetDecimals)) {
				return nil, output.Errorf(output.CodeUpstream, "Euler market has invalid asset metadata")
			}
			return m, nil
		}
	}
	return nil, output.Errorf(output.CodeNotFound, "no Euler market lends %s against %s on chain %d", bv.Hex(), cv.Hex(), t.chain)
}

func (a *App) eulerBorrowPrepare(ctx context.Context, e *env, w *resolvedWallet, t lendingTarget, cv, bv common.Address, sub uint8, req borrowRequest) (*result, error) {
	m, err := eulerMarket(ctx, e, t, cv, bv)
	if err != nil {
		return nil, err
	}
	cd, ld := int(m.CollateralAssetDecimals), int(m.BorrowAssetDecimals)
	owner := addr(w.agent)
	c := e.client(w.profile)
	var pos v2.GetLendingPositionResponse
	if err = c.Lending(ctx, "GetLendingPosition", &v2.GetLendingPositionRequest{Chain: t.caip2(), UserAddress: w.agent, BorrowVaultAddress: bv.Hex(), CollateralVaultAddress: cv.Hex(), SubAccountIndex: uint32(sub)}, &pos, false); err != nil {
		return nil, apiError(err, "GetLendingPosition")
	}
	price := parseOpt(m.OraclePrice)
	lltv := parseOpt(pos.LiquidationLtv)
	if !lltv.ok {
		lltv = parseOpt(m.LiquidationLtv)
	}
	if !price.ok || price.v <= 0 || !lltv.ok {
		return nil, output.Errorf(output.CodeUpstream, "Euler oracle price or liquidation LTV unavailable; refusing a borrow action without a price")
	}
	coll, okc := new(big.Int).SetString(pos.CollateralAssets, 10)
	debt, okd := new(big.Int).SetString(pos.DebtAssets, 10)
	if !okc || !okd {
		return nil, output.Errorf(output.CodeUpstream, "Euler position amounts unavailable")
	}
	position := func(coll, debt *big.Int) lending.Position {
		return lending.Position{Collateral: lending.Float(coll, cd), Debt: lending.Float(debt, ld), Price: price.v, LLTV: lltv.v}
	}
	before, err := lending.Assess(position(coll, debt))
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "current position")
	}
	plan := lending.Plan{Protocol: protoEuler, Action: req.action(), Chain: t.chain, Owner: owner, Vault: &bv, CollateralVault: &cv, SubAccount: sub, Tokens: []common.Address{addr(m.CollateralAssetAddress), addr(m.BorrowAssetAddress)}}
	want := lending.Want{}
	newColl, newDebt := new(big.Int).Set(coll), new(big.Int).Set(debt)
	var calls []lending.Call
	preview := map[string]any{"collateral_vault": cv.Hex(), "borrow_vault": bv.Hex(), "collateral": m.CollateralAssetSymbol, "loan": m.BorrowAssetSymbol, "sub_account": sub}
	if req.increases() {
		supply, borrow := new(big.Int), new(big.Int)
		supplyText, borrowText := "0", "0"
		if req.supply != "" {
			if supply, err = lendingAmount(req.supply, cd, "--supply-collateral"); err != nil {
				return nil, err
			}
			supplyText = req.supply
			newColl.Add(newColl, supply)
			want.Vault(cv, "deposit", supply)
			preview["supply_collateral"] = req.supply
		}
		if req.borrow != "" {
			if borrow, err = lendingAmount(req.borrow, ld, "--borrow"); err != nil {
				return nil, err
			}
			borrowText = req.borrow
			newDebt.Add(newDebt, borrow)
			want.Vault(bv, "borrow", borrow)
			preview["borrow"] = req.borrow
		}
		if borrow.Sign() > 0 {
			var resp v2.BuildLendingBorrowResponse
			if err = c.Lending(ctx, "BuildLendingBorrow", &v2.BuildLendingBorrowRequest{Chain: t.caip2(), UserAddress: w.agent, BorrowVaultAddress: bv.Hex(), CollateralVaultAddress: cv.Hex(), CollateralAmount: supplyText, BorrowAmount: borrowText, SubAccountIndex: uint32(sub)}, &resp, false); err != nil {
				return nil, apiError(err, "BuildLendingBorrow")
			}
			if !strings.EqualFold(resp.SubAccountAddress, lending.SubAccount(owner, sub).Hex()) {
				return nil, output.Errorf(output.CodePayloadMismatch, "backend sub-account differs from the requested index")
			}
			calls, err = eulerSteps(t, want, addr(m.CollateralAssetAddress), cv, supply, resp.NeedsApproval, resp.ApproveTx, resp.BorrowTx)
		} else {
			// Collateral only: deposit into the sub-account and enable it.
			var resp v2.BuildLendingDepositResponse
			if err = c.Lending(ctx, "BuildLendingDeposit", &v2.BuildLendingDepositRequest{Chain: t.caip2(), UserAddress: w.agent, VaultAddress: cv.Hex(), Amount: supplyText, SubAccountIndex: uint32(sub), EnableAsCollateral: true}, &resp, false); err != nil {
				return nil, apiError(err, "BuildLendingDeposit")
			}
			calls, err = eulerSteps(t, want, addr(m.CollateralAssetAddress), cv, supply, resp.NeedsApproval, resp.ApproveTx, resp.DepositTx)
		}
		if err != nil {
			return nil, output.Wrap(output.CodePayloadMismatch, err, "Euler borrow transaction")
		}
	} else {
		repReq := &v2.BuildLendingRepayRequest{Chain: t.caip2(), UserAddress: w.agent, BorrowVaultAddress: bv.Hex(), SubAccountIndex: uint32(sub)}
		var approveCap *big.Int
		if req.repay != "" && debt.Sign() == 0 {
			return nil, output.Errorf(output.CodeInvalidArgument, "position has no debt to repay")
		}
		if req.repay == "" {
			// Collateral only (e.g. after a separate full repay).
			amountText, err := eulerCollateralWithdraw(want, preview, &pos, cv, cd, coll, newColl, req.withdraw)
			if err != nil {
				return nil, err
			}
			var resp v2.BuildLendingWithdrawResponse
			if err = c.Lending(ctx, "BuildLendingWithdraw", &v2.BuildLendingWithdrawRequest{Chain: t.caip2(), UserAddress: w.agent, VaultAddress: cv.Hex(), Amount: amountText, SubAccountIndex: uint32(sub)}, &resp, false); err != nil {
				return nil, apiError(err, "BuildLendingWithdraw")
			}
			call, err := lendingTx(resp.WithdrawTx, t)
			if err != nil {
				return nil, output.Wrap(output.CodePayloadMismatch, err, "BuildLendingWithdraw")
			}
			calls = []lending.Call{call}
		} else {
			if isMaxAmount(req.repay) {
				repReq.RepayAmount = "max"
				// The backend approves the debt plus a 2% accrual margin.
				approveCap = new(big.Int).Add(debt, new(big.Int).Div(debt, big.NewInt(50)))
				approveCap.Add(approveCap, big.NewInt(2))
				newDebt.SetInt64(0)
				want.Vault(bv, "repay", gmath.MaxBig256).Flag("vault:" + strings.ToLower(bv.Hex()) + ":disableController")
				preview["repay"] = rawToUnits(pos.DebtAssets, ld) + " (max)"
			} else {
				amt, aerr := lendingAmount(req.repay, ld, "--repay")
				if aerr != nil {
					return nil, aerr
				}
				if amt.Cmp(debt) >= 0 {
					return nil, output.Errorf(output.CodeInvalidArgument, "repay %s covers the whole debt; use --repay max", req.repay)
				}
				repReq.RepayAmount = req.repay
				approveCap = amt
				newDebt.Sub(newDebt, amt)
				want.Vault(bv, "repay", amt)
				preview["repay"] = req.repay
			}
			if req.withdraw != "" {
				cvHex := cv.Hex()
				repReq.CollateralVaultAddress = &cvHex
				withdrawText, err := eulerCollateralWithdraw(want, preview, &pos, cv, cd, coll, newColl, req.withdraw)
				if err != nil {
					return nil, err
				}
				repReq.WithdrawCollateralAmount = &withdrawText
			}
			var resp v2.BuildLendingRepayResponse
			if err = c.Lending(ctx, "BuildLendingRepay", repReq, &resp, false); err != nil {
				return nil, apiError(err, "BuildLendingRepay")
			}
			if calls, err = eulerSteps(t, want, addr(m.BorrowAssetAddress), bv, approveCap, resp.NeedsApproval, resp.ApproveTx, resp.RepayTx); err != nil {
				return nil, output.Wrap(output.CodePayloadMismatch, err, "BuildLendingRepay")
			}
		}
	}
	after, err := lending.Assess(position(newColl, newDebt))
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "projected position")
	}
	// Euler refuses new debt above the vault's borrow LTV; keep the CLI's
	// ceiling at or below it.
	if b := parseOpt(pos.BorrowLtv); b.ok && b.v < after.MaxLTV {
		after.MaxLTV = b.v
	}
	if err = guard(e, after); err != nil {
		return nil, err
	}
	plan.Risk = &after
	plan.Guard = lending.NewGuard(coll, debt, newColl, newDebt, e.cmd.Bool("accept-high-risk"))
	preview["risk"] = riskPreview(before, after)
	return a.sealLending(e, w, kindBorrow, plan, calls, want, m.BorrowAssetAddress, req.action(), preview)
}

// eulerCollateralWithdraw validates a collateral withdrawal against the
// position, records its expected effect and returns the backend amount.
func eulerCollateralWithdraw(want lending.Want, preview map[string]any, pos *v2.GetLendingPositionResponse, cv common.Address, cd int, coll, newColl *big.Int, withdraw string) (string, error) {
	preview["withdraw_collateral"] = withdraw
	if isMaxAmount(withdraw) {
		shares, ok := new(big.Int).SetString(pos.CollateralShares, 10)
		if !ok || shares.Sign() == 0 {
			return "", output.Errorf(output.CodeInvalidArgument, "position has no collateral to withdraw")
		}
		newColl.SetInt64(0)
		want.Vault(cv, "redeem", shares)
		return "max", nil
	}
	amt, err := lendingAmount(withdraw, cd, "--withdraw-collateral")
	if err != nil {
		return "", err
	}
	if amt.Cmp(coll) > 0 {
		return "", output.Errorf(output.CodeInvalidArgument, "withdraw exceeds the collateral (%s)", rawToUnits(pos.CollateralAssets, cd))
	}
	newColl.Sub(newColl, amt)
	want.Vault(cv, "withdraw", amt)
	return withdraw, nil
}

func (a *App) cmdBorrowSubmit(e *env) (*result, error) {
	return a.cmdLendingSubmit(e, kindBorrow, nil)
}
func (a *App) cmdBorrowStatus(e *env) (*result, error) { return a.cmdLendingStatus(e, kindBorrow) }
func (a *App) cmdBorrowReconcile(e *env) (*result, error) {
	return a.cmdLendingReconcile(e, kindBorrow)
}
func (a *App) cmdEarnSubmit(e *env) (*result, error)    { return a.cmdLendingSubmit(e, kindEarn, nil) }
func (a *App) cmdEarnStatus(e *env) (*result, error)    { return a.cmdLendingStatus(e, kindEarn) }
func (a *App) cmdEarnReconcile(e *env) (*result, error) { return a.cmdLendingReconcile(e, kindEarn) }
