package cli

import (
	"context"
	"math"
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

// Loops are built and verified by the CLI. The swap leg is quoted through
// the same swap service and Oku Router as `oku swap`; everything else is
// Morpho Bundler3 / GeneralAdapter1 or the Euler EVC and swap periphery,
// the same contracts the backend loop uses.

func loopFlags(open bool) []urfave.Flag {
	fs := append(lendingTargetFlags(),
		&urfave.StringFlag{Name: "market", Usage: "Morpho market id"},
		&urfave.StringFlag{Name: "collateral-vault", Usage: "Euler collateral vault"},
		&urfave.StringFlag{Name: "borrow-vault", Usage: "Euler borrow vault"},
		&urfave.IntFlag{Name: "sub-account", Usage: "Euler sub-account index (1-255) holding the loop"},
		&urfave.IntFlag{Name: "slippage-bps", Value: 50, Usage: "swap slippage in basis points, 1-300"},
		&urfave.StringFlag{Name: "route", Usage: "pin one swap router"},
		&urfave.IntFlag{Name: "max-loss-bps", Usage: "refuse when the swap's priced market loss exceeds this (default 500)"},
		&urfave.BoolFlag{Name: "allow-high-impact", Usage: "accept a swap above --max-loss-bps"},
		// The shared swap quoting reads it; loop swaps always exclude intents.
		&urfave.BoolFlag{Name: "no-intent", Value: true, Hidden: true},
		&urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"},
	)
	if open {
		return append(fs,
			&urfave.StringFlag{Name: "deposit", Usage: "collateral amount to deposit (required)"},
			&urfave.StringFlag{Name: "target-leverage", Usage: "target leverage, e.g. 3 (required)"},
			&urfave.BoolFlag{Name: "accept-high-risk", Usage: "allow a health factor below 1.333 (never above the max LTV)"},
		)
	}
	return append(fs,
		&urfave.StringFlag{Name: "target-leverage", Usage: "adjust: target leverage"},
		&urfave.StringFlag{Name: "target-ltv", Usage: "adjust: target LTV, e.g. 0.6"},
		&urfave.StringFlag{Name: "to", Value: "collateral", Usage: "unwind: what the agent ends up holding, collateral or loan"},
		&urfave.BoolFlag{Name: "accept-high-risk", Usage: "allow a health factor below 1.333 (never above the max LTV)"},
	)
}

// loopState is one loop position read on chain, in raw and human units.
type loopState struct {
	protocol                   string
	chain                      uint64
	owner                      common.Address
	collToken, loanToken       common.Address
	collSymbol, loanSymbol     string
	collDecimals, loanDecimals int
	collateral, debt           *big.Int
	price, lltv, maxLTV        float64 // loan per collateral; LTVs as fractions
	collAPY, borrowAPY         optFloat
	// Morpho
	morpho  *lending.MarketSnapshot
	deploy  lending.MorphoDeployment
	accrued lending.MarketState
	// Euler
	euler      lending.EulerLoop
	subIndex   uint8
	collShares *big.Int
	debtExists bool
	cvAddr     common.Address
	bvAddr     common.Address
}

func (s loopState) value(raw *big.Int, collateral bool) float64 {
	if collateral {
		return lending.Float(raw, s.collDecimals) * s.price
	}
	return lending.Float(raw, s.loanDecimals)
}

func (s loopState) risk(coll, debt *big.Int) (lending.Risk, error) {
	r, err := lending.Assess(lending.Position{Collateral: lending.Float(coll, s.collDecimals), Debt: lending.Float(debt, s.loanDecimals), Price: s.price, LLTV: s.lltv})
	if err == nil && s.maxLTV > 0 && s.maxLTV < r.MaxLTV {
		r.MaxLTV = s.maxLTV
	}
	return r, err
}

func leverageOf(cv, d float64) float64 {
	if cv == 0 && d == 0 {
		return 1 // closed: no exposure
	}
	if cv <= d {
		return math.Inf(1)
	}
	return cv / (cv - d)
}

func (s loopState) view(coll, debt *big.Int) map[string]any {
	cv, d := s.value(coll, true), s.value(debt, false)
	out := map[string]any{"collateral": formatUnits(coll, s.collDecimals) + " " + s.collSymbol, "debt": formatUnits(debt, s.loanDecimals) + " " + s.loanSymbol}
	r, err := s.risk(coll, debt)
	if err != nil {
		out["risk"] = "unknown: " + err.Error()
		return out
	}
	out["ltv"], out["health_factor"], out["liquidation_price"] = 0.0, optPtr(r.HealthFactor), optPtr(r.LiquidationPrice)
	if r.HealthFactor != nil {
		out["ltv"] = r.LTV
	}
	lev := leverageOf(cv, d)
	out["leverage"] = optFloat{lev, !math.IsInf(lev, 0)}
	if s.collAPY.ok && s.borrowAPY.ok && !math.IsInf(lev, 0) {
		out["net_apy"] = some(lending.LoopNetAPY(lev, s.collAPY.v, s.borrowAPY.v))
	} else {
		out["net_apy"] = optFloat{}
	}
	return out
}

func loopSlippage(e *env) (int, error) {
	bps := e.cmd.Int("slippage-bps")
	if bps < 1 || bps > 300 {
		return 0, output.Errorf(output.CodeInvalidArgument, "--slippage-bps must be 1-300")
	}
	return bps, nil
}

// readLoop reads the position the loop flags name, for either protocol.
func (a *App) readLoop(ctx context.Context, e *env, w *resolvedWallet, t lendingTarget, open bool) (loopState, error) {
	if _, err := onchain.RPCURL(a.Getenv, t.chain); err != nil {
		return loopState{}, output.Wrap(output.CodeConfigError, err, "chain RPC")
	}
	owner := addr(w.agent)
	if t.protocol == protoMorpho {
		id := str(e, "market")
		if len(id) != 66 || !strings.HasPrefix(id, "0x") {
			return loopState{}, output.Errorf(output.CodeInvalidArgument, "--market must be a Morpho market id")
		}
		d, err := lending.Morpho(t.chain)
		if err != nil {
			return loopState{}, output.Wrap(output.CodeInvalidArgument, err, "morpho")
		}
		snap, err := lending.ReadMarket(ctx, onchain.HTTPRPC{Getenv: a.Getenv}, d, common.HexToHash(id), owner)
		if err != nil {
			return loopState{}, output.Wrap(output.CodeUpstream, err, "morpho market")
		}
		if snap.OraclePrice == nil {
			return loopState{}, output.Errorf(output.CodeUpstream, "market oracle price unavailable; refusing a loop without a price")
		}
		now := a.Now().Unix()
		s := loopState{protocol: protoMorpho, chain: t.chain, owner: owner, collToken: snap.Params.CollateralToken, loanToken: snap.Params.LoanToken, collSymbol: snap.CollateralSymbol, loanSymbol: snap.LoanSymbol, collDecimals: snap.CollateralDecimals, loanDecimals: snap.LoanDecimals, collateral: snap.Position.Collateral, debt: snap.Debt(now), price: lending.MorphoPrice(snap.OraclePrice, snap.CollateralDecimals, snap.LoanDecimals), lltv: lending.Float(snap.Params.Lltv, 18), morpho: snap, deploy: d, accrued: snap.Market.AccrueBorrow(snap.BorrowRate, lending.AccrualHorizon(now))}
		s.debtExists = snap.Position.BorrowShares.Sign() > 0
		a.morphoLoopRates(ctx, e, &s, id)
		return s, nil
	}
	cv, bv := str(e, "collateral-vault"), str(e, "borrow-vault")
	if !onchain.ValidAddress(cv) || !onchain.ValidAddress(bv) {
		return loopState{}, output.Errorf(output.CodeInvalidArgument, "--collateral-vault and --borrow-vault are required for Euler")
	}
	sub := e.cmd.Int("sub-account")
	if sub < 1 || sub > 255 {
		return loopState{}, output.Errorf(output.CodeInvalidArgument, "--sub-account must be 1-255 (loops never use the main account)")
	}
	sw, err := lending.EulerSwapFor(t.chain)
	if err != nil {
		return loopState{}, output.Wrap(output.CodeRejected, err, "euler loop")
	}
	m, err := eulerMarket(ctx, e, t, addr(cv), addr(bv))
	if err != nil {
		return loopState{}, err
	}
	var pos v2.GetLendingPositionResponse
	if err = e.client(w.profile).Lending(ctx, "GetLendingPosition", &v2.GetLendingPositionRequest{Chain: t.caip2(), UserAddress: w.agent, BorrowVaultAddress: bv, CollateralVaultAddress: cv, SubAccountIndex: uint32(sub)}, &pos, false); err != nil {
		return loopState{}, apiError(err, "GetLendingPosition")
	}
	subAddr := lending.SubAccount(owner, uint8(sub))
	if !strings.EqualFold(pos.SubAccountAddress, subAddr.Hex()) {
		return loopState{}, output.Errorf(output.CodePayloadMismatch, "backend sub-account differs from the requested index")
	}
	price, lltv := parseOpt(m.OraclePrice), parseOpt(pos.LiquidationLtv)
	if !lltv.ok {
		lltv = parseOpt(m.LiquidationLtv)
	}
	coll, okc := new(big.Int).SetString(pos.CollateralAssets, 10)
	debt, okd := new(big.Int).SetString(pos.DebtAssets, 10)
	shares, oks := new(big.Int).SetString(pos.CollateralShares, 10)
	if !price.ok || price.v <= 0 || !lltv.ok || !okc || !okd || !oks {
		return loopState{}, output.Errorf(output.CodeUpstream, "Euler price, LTV or position unavailable; refusing a loop without them")
	}
	if !open && debt.Sign() == 0 && coll.Sign() == 0 {
		return loopState{}, output.Errorf(output.CodeNotFound, "no Euler position on sub-account %d", sub)
	}
	s := loopState{protocol: protoEuler, chain: t.chain, owner: owner, collToken: addr(m.CollateralAssetAddress), loanToken: addr(m.BorrowAssetAddress), collSymbol: m.CollateralAssetSymbol, loanSymbol: m.BorrowAssetSymbol, collDecimals: int(m.CollateralAssetDecimals), loanDecimals: int(m.BorrowAssetDecimals), collateral: coll, debt: debt, price: price.v, lltv: lltv.v, subIndex: uint8(sub), collShares: shares, debtExists: debt.Sign() > 0, cvAddr: addr(cv), bvAddr: addr(bv), collAPY: parseOpt(m.SupplyApy), borrowAPY: parseOpt(m.BorrowApy)}
	if b := parseOpt(m.BorrowLtv); b.ok {
		s.maxLTV = b.v
	}
	s.euler = lending.EulerLoop{Swap: sw, Owner: owner, Sub: subAddr, CollateralVault: s.cvAddr, BorrowVault: s.bvAddr, CollateralAsset: s.collToken, BorrowAsset: s.loanToken, Deadline: uint64(a.Now().Add(lendingQuoteTTL).Unix())}
	return s, nil
}

// morphoLoopRates fills the collateral yield (when the collateral is a
// vault the indexer knows) and the net borrow rate. Rates only inform the
// preview, so a missing one is reported and left N/A.
func (a *App) morphoLoopRates(ctx context.Context, e *env, s *loopState, id string) {
	warn := func(what string, err error) {
		e.diag("warning: morpho market %s on chain %d: %s unknown, net APY N/A: %v", id, s.chain, what, err)
	}
	sap, err := a.morphoIndexer(ctx)
	if err != nil {
		warn("rates", err)
		return
	}
	campaigns, err := sap.LiveCampaigns(ctx)
	if err != nil {
		warn("reward campaigns", err)
		return
	}
	now := a.Now().Unix()
	markets, err := sap.Markets(ctx, s.chain)
	if err != nil {
		warn("borrow rate", err)
	} else {
		for _, m := range markets {
			if !strings.EqualFold(m.Snapshot.MarketID, id) {
				continue
			}
			if br, err := lending.MarketBorrowRate(m, campaigns, now); err != nil {
				warn("borrow rate", err)
			} else {
				s.borrowAPY = some(br.Net)
			}
		}
	}
	// Yield-bearing collateral (an ERC-4626 vault the indexer lists); plain
	// tokens have no indexed yield and leave net APY N/A.
	vaults, err := sap.Vaults(ctx, s.chain)
	if err != nil {
		warn("collateral yield", err)
		return
	}
	for _, v := range vaults {
		if addr(v.Snapshot.Address) != s.collToken {
			continue
		}
		if y, err := lending.VaultYield(v, campaigns, now); err != nil {
			warn("collateral yield", err)
		} else {
			s.collAPY = some(y.Net)
		}
	}
}

func (s loopState) plan(action string) lending.Plan {
	router, _ := lending.OkuRouter(s.chain)
	p := lending.Plan{Action: action, Chain: s.chain, Owner: s.owner, Tokens: []common.Address{s.collToken, s.loanToken}, Router: &router}
	if s.protocol == protoMorpho {
		id := s.morpho.Params.ID()
		p.Protocol, p.MarketID = protoMorpho, &id
	} else {
		cv, bv := s.cvAddr, s.bvAddr
		p.Protocol, p.Vault, p.CollateralVault, p.SubAccount, p.SwapPeriphery = protoEuler, &bv, &cv, s.subIndex, true
	}
	return p
}

// hop is the contract that pays and receives the router swap.
func (s loopState) hop() common.Address {
	if s.protocol == protoMorpho {
		return s.deploy.GeneralAdapter1
	}
	return s.euler.Swap.Swapper
}

// k is the worst-case value kept per unit swapped: slippage plus the
// quoted market loss bound. Sizing uses it so the realized position never
// overshoots the target.
func swapKeep(bps int) float64 { return 1 - float64(bps)/10_000 }

func rawFromFloat(v float64, decimals int) *big.Int {
	if !(v > 0) || math.IsInf(v, 0) {
		return new(big.Int)
	}
	f := new(big.Float).Mul(big.NewFloat(v), new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)))
	out, _ := f.Int(nil)
	return out
}

func (a *App) cmdLoopPrepare(e *env, mode string) (*result, error) {
	t, err := parseLendingTarget(e)
	if err != nil {
		return nil, err
	}
	bps, err := loopSlippage(e)
	if err != nil {
		return nil, err
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	s, err := a.readLoop(ctx, e, w, t, mode == "open")
	if err != nil {
		return nil, err
	}
	switch mode {
	case "open":
		return a.loopOpen(ctx, e, w, s, bps)
	case "adjust":
		return a.loopAdjust(ctx, e, w, s, bps)
	default:
		return a.loopUnwind(ctx, e, w, s, bps)
	}
}

func (a *App) loopOpen(ctx context.Context, e *env, w *resolvedWallet, s loopState, bps int) (*result, error) {
	deposit, err := lendingAmount(str(e, "deposit"), s.collDecimals, "--deposit")
	if err != nil {
		return nil, err
	}
	lev := parseOpt(str(e, "target-leverage"))
	if !lev.ok || lev.v <= 1 {
		return nil, output.Errorf(output.CodeInvalidArgument, "--target-leverage must be above 1")
	}
	if max := lending.MaxLoopLeverage(s.lltv); lev.v > max {
		return nil, output.Errorf(output.CodeRejected, "leverage %.2fx exceeds this market's max %.2fx", lev.v, max)
	}
	if s.protocol == protoEuler && (s.collateral.Sign() > 0 || s.debt.Sign() > 0) {
		return nil, output.Errorf(output.CodeIntentConflict, "sub-account %d already holds a position; use loop adjust, or pick a free --sub-account", s.subIndex)
	}
	// Size on the deposit alone with the worst-case swap loss, so the
	// realized leverage never overshoots the target.
	borrow := rawFromFloat(lending.LoopBorrow(s.value(deposit, true), 0, lev.v, swapKeep(bps)), s.loanDecimals)
	return a.loopIncrease(ctx, e, w, s, "open", deposit, borrow, bps, lev.v)
}

func (a *App) loopAdjust(ctx context.Context, e *env, w *resolvedWallet, s loopState, bps int) (*result, error) {
	lev, ltv := parseOpt(str(e, "target-leverage")), parseOpt(str(e, "target-ltv"))
	if lev.ok == ltv.ok {
		return nil, output.Errorf(output.CodeUsage, "pass exactly one of --target-leverage or --target-ltv")
	}
	if ltv.ok {
		if ltv.v < 0 || ltv.v >= 1 {
			return nil, output.Errorf(output.CodeInvalidArgument, "--target-ltv must be 0 to below 1")
		}
		lev = some(1 / (1 - ltv.v))
	}
	if lev.v < 1 {
		return nil, output.Errorf(output.CodeInvalidArgument, "target leverage must be at least 1")
	}
	if s.collateral.Sign() == 0 {
		return nil, output.Errorf(output.CodeNotFound, "no collateral in this position; use loop open")
	}
	cv, d := s.value(s.collateral, true), s.value(s.debt, false)
	if cv <= d {
		return nil, output.Errorf(output.CodeRejected, "position is underwater; refusing to adjust")
	}
	k := swapKeep(bps)
	cur := leverageOf(cv, d)
	if math.Abs(lev.v-cur) < 0.01 {
		return nil, output.Errorf(output.CodeInvalidArgument, "position is already at %.2fx", cur)
	}
	if lev.v > cur {
		if max := lending.MaxLoopLeverage(s.lltv); lev.v > max {
			return nil, output.Errorf(output.CodeRejected, "leverage %.2fx exceeds this market's max %.2fx", lev.v, max)
		}
		borrow := rawFromFloat(lending.LoopBorrow(cv, d, lev.v, k), s.loanDecimals)
		return a.loopIncrease(ctx, e, w, s, "adjust", nil, borrow, bps, lev.v)
	}
	if lev.v == 1 {
		return nil, output.Errorf(output.CodeUsage, "target 1x repays all debt; use loop unwind")
	}
	target := 1 - 1/lev.v
	sellValue := lending.LoopSell(cv, d, target, k)
	sell := rawFromFloat(sellValue/s.price, s.collDecimals)
	return a.loopDecrease(ctx, e, w, s, "adjust", sell, false, bps)
}

func (a *App) loopUnwind(ctx context.Context, e *env, w *resolvedWallet, s loopState, bps int) (*result, error) {
	to := str(e, "to")
	if to != "collateral" && to != "loan" {
		return nil, output.Errorf(output.CodeInvalidArgument, "--to must be collateral or loan")
	}
	if !s.debtExists {
		return nil, output.Errorf(output.CodeInvalidArgument, "position has no debt; withdraw the collateral with borrow prepare --withdraw-collateral max")
	}
	k := swapKeep(bps)
	if to == "loan" {
		return a.loopDecrease(ctx, e, w, s, "unwind", new(big.Int).Set(s.collateral), true, bps)
	}
	// Sell just enough collateral to cover the debt after slippage, plus
	// the accrual horizon margin the full repay transfers.
	need := s.debt
	if s.protocol == protoMorpho {
		need = s.accrued.ToBorrowAssetsUp(s.morpho.Position.BorrowShares)
	}
	sell := rawFromFloat(s.value(need, false)/k/s.price*1.001, s.collDecimals)
	if sell.Cmp(s.collateral) > 0 {
		return nil, output.Errorf(output.CodeRejected, "collateral cannot cover the debt plus slippage; refusing to unwind")
	}
	return a.loopDecrease(ctx, e, w, s, "unwind", sell, true, bps)
}

// loopIncrease builds open and adjust-up: borrow, swap to collateral,
// supply, in one transaction.
func (a *App) loopIncrease(ctx context.Context, e *env, w *resolvedWallet, s loopState, action string, deposit, borrow *big.Int, bps int, lev float64) (*result, error) {
	if borrow.Sign() <= 0 {
		return nil, output.Errorf(output.CodeInvalidArgument, "nothing to borrow at %.2fx", lev)
	}
	if deposit == nil {
		deposit = new(big.Int)
	}
	q, err := a.loopSwap(ctx, e, w, s.chain, s.loanToken, s.collToken, s.hop(), borrow, s.loanDecimals)
	if err != nil {
		return nil, err
	}
	coll := new(big.Int).Add(s.collateral, deposit)
	coll.Add(coll, q.MinOut)
	debt := new(big.Int).Add(s.debt, borrow)
	after, err := s.risk(coll, debt)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "projected position")
	}
	if err = guard(e, after); err != nil {
		return nil, err
	}
	plan := s.plan("loop-" + action + "-up")
	if action == "open" {
		plan.Action = "loop-open"
	}
	want := lending.Want{}.Sell(s.loanToken, s.collToken, borrow)
	var calls []lending.Call
	if s.protocol == protoMorpho {
		snap, d, mp := s.morpho, s.deploy, s.morpho.Params
		id := mp.ID()
		if deposit.Sign() > 0 {
			if snap.CollateralBalance.Cmp(deposit) < 0 {
				return nil, output.Errorf(output.CodeInsufficientBalance, "agent holds %s %s%s", formatUnits(snap.CollateralBalance, s.collDecimals), s.collSymbol, fundingHint(s.chain, s.collToken.Hex()))
			}
			calls = append(calls, want.Allow(s.collToken, d.GeneralAdapter1, snap.CollateralAllow, deposit)...)
			want.Pull(s.collToken, deposit)
		}
		if !snap.Authorized {
			calls = append(calls, d.AuthorizeAdapter())
			want.Flag("authorize")
		}
		min, err := lending.MinBorrowSharePrice(borrow, s.accrued, lending.DefaultSlippage)
		if err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "borrow")
		}
		call, err := d.LoopIncrease(mp, deposit, borrow, min, q.Swap, s.owner)
		if err != nil {
			return nil, output.Wrap(output.CodePayloadMismatch, err, "loop bundle")
		}
		calls = append(calls, call)
		want.Borrow(id, borrow).Supply(id, new(big.Int).Add(deposit, q.MinOut)).SupplyRest(id).Flash()
	} else {
		if deposit.Sign() > 0 {
			bal, allow, err := a.eulerFunds(ctx, s)
			if err != nil {
				return nil, err
			}
			if bal.Cmp(deposit) < 0 {
				return nil, output.Errorf(output.CodeInsufficientBalance, "agent holds %s %s%s", formatUnits(bal, s.collDecimals), s.collSymbol, fundingHint(s.chain, s.collToken.Hex()))
			}
			calls = append(calls, want.Allow(s.collToken, s.cvAddr, allow, deposit)...)
			want.Vault(s.cvAddr, "deposit", deposit)
		}
		call, err := s.euler.Increase(deposit, borrow, q.Swap)
		if err != nil {
			return nil, output.Wrap(output.CodePayloadMismatch, err, "loop batch")
		}
		calls = append(calls, call)
		want.Vault(s.bvAddr, "borrow", borrow).Bound("skim_min", s.cvAddr, q.MinOut)
	}
	plan.Risk = &after
	plan.Guard = lending.NewGuard(s.collateral, s.debt, coll, debt, e.cmd.Bool("accept-high-risk"))
	preview := s.loopPreview(coll, debt, q, lev)
	preview["borrow"] = formatUnits(borrow, s.loanDecimals) + " " + s.loanSymbol
	if deposit.Sign() > 0 {
		preview["deposit"] = formatUnits(deposit, s.collDecimals) + " " + s.collSymbol
	}
	preview["agent_receives"] = "nothing; any unsupplied swap surplus is returned"
	return a.sealLending(e, w, kindLoop, plan, calls, want, s.collToken.Hex(), formatUnits(deposit, s.collDecimals), preview)
}

// loopDecrease builds adjust-down and unwind: repay with the proceeds of
// selling `sell` collateral, atomically. full repays everything; with
// sell == all collateral the agent ends up holding the loan asset.
func (a *App) loopDecrease(ctx context.Context, e *env, w *resolvedWallet, s loopState, action string, sell *big.Int, full bool, bps int) (*result, error) {
	if sell.Sign() <= 0 || sell.Cmp(s.collateral) > 0 {
		return nil, output.Errorf(output.CodeInvalidArgument, "computed collateral sale %s is outside the position", formatUnits(sell, s.collDecimals))
	}
	q, err := a.loopSwap(ctx, e, w, s.chain, s.collToken, s.loanToken, s.hop(), sell, s.collDecimals)
	if err != nil {
		return nil, err
	}
	coll := new(big.Int).Sub(s.collateral, sell)
	debt := new(big.Int).Sub(s.debt, q.MinOut)
	if full {
		coll, debt = new(big.Int), new(big.Int)
		if q.MinOut.Cmp(s.debt) < 0 {
			return nil, output.Errorf(output.CodeRejected, "swap minimum %s %s does not cover the debt %s; refusing to unwind", formatUnits(q.MinOut, s.loanDecimals), s.loanSymbol, formatUnits(s.debt, s.loanDecimals))
		}
	} else if debt.Sign() <= 0 {
		return nil, output.Errorf(output.CodeInvalidArgument, "this sale repays all debt; use loop unwind")
	}
	after, err := s.risk(coll, debt)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "projected position")
	}
	if err = guard(e, after); err != nil {
		return nil, err
	}
	plan := s.plan("loop-" + action)
	if action == "adjust" {
		plan.Action = "loop-adjust-down"
	}
	want := lending.Want{}.Sell(s.collToken, s.loanToken, sell)
	var calls []lending.Call
	if s.protocol == protoMorpho {
		mp, id := s.morpho.Params, s.morpho.Params.ID()
		if !s.morpho.Authorized {
			// A position opened elsewhere may not have authorized the
			// adapter that withdraws its collateral.
			calls = append(calls, s.deploy.AuthorizeAdapter())
			want.Flag("authorize")
		}
		var r lending.Repay
		withdraw := sell
		if full {
			// Repay by shares so no dust debt remains; the flash covers
			// the debt accrued over the SDK horizon.
			transfer := s.accrued.ToBorrowAssetsUp(s.morpho.Position.BorrowShares)
			maxPrice, err := lending.MaxRepaySharePrice(nil, s.morpho.Position.BorrowShares, s.accrued, lending.DefaultSlippage)
			if err != nil {
				return nil, output.Wrap(output.CodeInvalidArgument, err, "repay")
			}
			r, withdraw = lending.Repay{Shares: s.morpho.Position.BorrowShares, Transfer: transfer, MaxSharePrice: maxPrice}, new(big.Int).Set(s.collateral)
			if q.MinOut.Cmp(transfer) < 0 {
				return nil, output.Errorf(output.CodeRejected, "swap minimum does not cover the debt plus accrual; refusing to unwind")
			}
		} else {
			maxPrice, err := lending.MaxRepaySharePrice(q.MinOut, nil, s.accrued, lending.DefaultSlippage)
			if err != nil {
				return nil, output.Wrap(output.CodeInvalidArgument, err, "repay")
			}
			r = lending.Repay{Transfer: q.MinOut, MaxSharePrice: maxPrice}
		}
		call, err := s.deploy.LoopDecrease(mp, r, withdraw, q.Swap, s.owner)
		if err != nil {
			return nil, output.Wrap(output.CodePayloadMismatch, err, "loop bundle")
		}
		calls = append(calls, call)
		want.Repaid(id).Withdraw(id, withdraw).Flash()
	} else {
		debtMax := new(big.Int)
		var withdraw = sell
		if !full {
			debtMax = new(big.Int).Sub(s.debt, q.MinOut)
			// Interest accrues until inclusion; allow the TTL's worth.
			debtMax.Add(debtMax, new(big.Int).Div(s.debt, big.NewInt(1000)))
			want.Vault(s.bvAddr, "swap_repay", q.MinOut)
		} else {
			want.Vault(s.bvAddr, "swap_repay", gmath.MaxBig256).Flag("vault:"+strings.ToLower(s.bvAddr.Hex())+":disableController").Vault(s.cvAddr, "redeem", gmath.MaxBig256)
			if sell.Cmp(s.collateral) == 0 {
				withdraw = nil
			}
		}
		if withdraw != nil {
			want.Vault(s.cvAddr, "withdraw", withdraw)
		}
		call, err := s.euler.Decrease(withdraw, q.Swap, debtMax)
		if err != nil {
			return nil, output.Wrap(output.CodePayloadMismatch, err, "loop batch")
		}
		calls = append(calls, call)
		want.Bound("debt_max", s.bvAddr, debtMax)
	}
	plan.Risk = &after
	plan.Guard = lending.NewGuard(s.collateral, s.debt, coll, debt, e.cmd.Bool("accept-high-risk"))
	preview := s.loopPreview(coll, debt, q, leverageOf(s.value(coll, true), s.value(debt, false)))
	preview["sell_collateral"] = formatUnits(sell, s.collDecimals) + " " + s.collSymbol
	switch {
	case full && sell.Cmp(s.collateral) == 0:
		preview["agent_receives"] = "at least " + formatUnits(new(big.Int).Sub(q.MinOut, s.debt), s.loanDecimals) + " " + s.loanSymbol + " after repaying all debt"
	case full:
		preview["agent_receives"] = formatUnits(new(big.Int).Sub(s.collateral, sell), s.collDecimals) + " " + s.collSymbol + " plus any swap surplus in " + s.loanSymbol
	default:
		preview["agent_receives"] = "nothing; the position stays open"
	}
	return a.sealLending(e, w, kindLoop, plan, calls, want, s.collToken.Hex(), formatUnits(sell, s.collDecimals), preview)
}

func (s loopState) loopPreview(coll, debt *big.Int, q loopSwapQuote, target float64) map[string]any {
	return map[string]any{
		"before":            s.view(s.collateral, s.debt),
		"after":             s.view(coll, debt),
		"target_leverage":   target,
		"swap_route":        q.Quote.Route,
		"swap_min_out":      q.MinOut.String(),
		"swap_loss_bps":     q.Quote.LossBps,
		"swap_price_impact": q.Quote.PriceImpact,
		"swap_fee":          q.Quote.OkuFeeRaw,
	}
}

// eulerFunds reads the agent's collateral balance and its allowance to
// the collateral vault (an ERC-4626 vault) in one read.
func (a *App) eulerFunds(ctx context.Context, s loopState) (*big.Int, *big.Int, error) {
	v, err := lending.ReadVault(ctx, onchain.HTTPRPC{Getenv: a.Getenv}, s.chain, s.cvAddr, s.owner, s.cvAddr, nil)
	if err != nil {
		return nil, nil, output.Wrap(output.CodeUpstream, err, "collateral balance")
	}
	if v.Asset != s.collToken {
		return nil, nil, output.Errorf(output.CodePayloadMismatch, "collateral vault asset differs from the market")
	}
	return v.Balance, v.Allowance, nil
}

func (a *App) cmdLoopOpen(e *env) (*result, error)   { return a.cmdLoopPrepare(e, "open") }
func (a *App) cmdLoopAdjust(e *env) (*result, error) { return a.cmdLoopPrepare(e, "adjust") }
func (a *App) cmdLoopUnwind(e *env) (*result, error) { return a.cmdLoopPrepare(e, "unwind") }
func (a *App) cmdLoopSubmit(e *env) (*result, error) { return a.cmdLendingSubmit(e, kindLoop, nil) }
func (a *App) cmdLoopStatus(e *env) (*result, error) { return a.cmdLendingStatus(e, kindLoop) }
func (a *App) cmdLoopReconcile(e *env) (*result, error) {
	return a.cmdLendingReconcile(e, kindLoop)
}
