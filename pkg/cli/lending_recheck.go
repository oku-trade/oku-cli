package cli

import (
	"context"
	"math/big"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/lending"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// recheckRisk re-reads the plan's position and oracle and re-applies the
// guardrails to the change prepare authorized, so price, debt or collateral
// moves after prepare cannot carry a position past the limits. Plans with
// no guard end debt-free and are always safe.
func (a *App) recheckRisk(ctx context.Context, e *env, w *resolvedWallet, plan lending.Plan) error {
	if plan.Guard == nil {
		return nil
	}
	now, err := a.positionRisk(ctx, e, w, plan)
	if err != nil {
		return output.Wrap(output.CodeUpstream, err, "re-reading the position before submit; nothing broadcast")
	}
	coll, debt := plan.Guard.Apply(now.coll, now.debt)
	r, err := lending.Assess(lending.Position{Collateral: lending.Float(coll, now.collDecimals), Debt: lending.Float(debt, now.loanDecimals), Price: now.price, LLTV: now.lltv})
	if err != nil {
		return output.Wrap(output.CodeUpstream, err, "re-assessing the position before submit; nothing broadcast")
	}
	if now.maxLTV > 0 && now.maxLTV < r.MaxLTV {
		r.MaxLTV = now.maxLTV
	}
	if err = lending.Check(r, plan.Guard.AcceptHighRisk); err != nil {
		return output.Wrap(output.CodeRejected, err, "position changed since prepare; nothing broadcast, prepare again")
	}
	return nil
}

type liveRisk struct {
	coll, debt                 *big.Int
	collDecimals, loanDecimals int
	price, lltv, maxLTV        float64
}

func (a *App) positionRisk(ctx context.Context, e *env, w *resolvedWallet, plan lending.Plan) (liveRisk, error) {
	if plan.Protocol == protoMorpho {
		if plan.MarketID == nil {
			return liveRisk{}, errUnguardable
		}
		d, err := lending.Morpho(plan.Chain)
		if err != nil {
			return liveRisk{}, err
		}
		s, err := lending.ReadMarket(ctx, a.httpRPC(), d, *plan.MarketID, plan.Owner)
		if err != nil {
			return liveRisk{}, err
		}
		if s.OraclePrice == nil {
			return liveRisk{}, errNoPrice
		}
		return liveRisk{coll: s.Position.Collateral, debt: s.Debt(a.Now().Unix()), collDecimals: s.CollateralDecimals, loanDecimals: s.LoanDecimals, price: lending.MorphoPrice(s.OraclePrice, s.CollateralDecimals, s.LoanDecimals), lltv: lending.Float(s.Params.Lltv, 18)}, nil
	}
	if plan.Vault == nil || plan.CollateralVault == nil {
		return liveRisk{}, errUnguardable
	}
	t := lendingTarget{chain: plan.Chain, protocol: protoEuler}
	m, err := eulerMarket(ctx, e, t, *plan.CollateralVault, *plan.Vault)
	if err != nil {
		return liveRisk{}, err
	}
	var pos v2.GetLendingPositionResponse
	if err = e.client(w.profile).Lending(ctx, "GetLendingPosition", &v2.GetLendingPositionRequest{Chain: t.caip2(), UserAddress: w.agent, BorrowVaultAddress: plan.Vault.Hex(), CollateralVaultAddress: plan.CollateralVault.Hex(), SubAccountIndex: uint32(plan.SubAccount)}, &pos, false); err != nil {
		return liveRisk{}, apiError(err, "GetLendingPosition")
	}
	price, lltv := parseOpt(m.OraclePrice), parseOpt(pos.LiquidationLtv)
	if !lltv.ok {
		lltv = parseOpt(m.LiquidationLtv)
	}
	coll, okc := new(big.Int).SetString(pos.CollateralAssets, 10)
	debt, okd := new(big.Int).SetString(pos.DebtAssets, 10)
	if !price.ok || price.v <= 0 || !lltv.ok || !okc || !okd {
		return liveRisk{}, errNoPrice
	}
	out := liveRisk{coll: coll, debt: debt, collDecimals: int(m.CollateralAssetDecimals), loanDecimals: int(m.BorrowAssetDecimals), price: price.v, lltv: lltv.v}
	if b := parseOpt(pos.BorrowLtv); b.ok {
		out.maxLTV = b.v
	}
	return out, nil
}

var (
	errUnguardable = output.Errorf(output.CodePayloadMismatch, "guarded plan names no market")
	errNoPrice     = output.Errorf(output.CodeUpstream, "oracle price, liquidation LTV or position unavailable")
)
