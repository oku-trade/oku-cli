package cli

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/lending"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func borrowMarketsFlags() []urfave.Flag {
	return append(lendingTargetFlags(),
		&urfave.StringFlag{Name: "loan-token", Usage: "only markets lending this token (address)"},
		&urfave.StringFlag{Name: "collateral-token", Usage: "only markets taking this collateral (address)"},
		&urfave.IntFlag{Name: "limit", Value: 25, Usage: "maximum markets to show, largest first"},
	)
}

// borrowMarket is one market row. Morpho markets are keyed by market id,
// Euler markets by their collateral and borrow vaults.
type borrowMarket struct {
	Protocol        string   `json:"protocol"`
	Chain           uint64   `json:"chain_id"`
	MarketID        string   `json:"market_id,omitempty"`
	CollateralVault string   `json:"collateral_vault,omitempty"`
	BorrowVault     string   `json:"borrow_vault,omitempty"`
	LoanToken       string   `json:"loan_token"`
	LoanSymbol      string   `json:"loan_symbol,omitempty"`
	CollateralToken string   `json:"collateral_token"`
	CollateralSym   string   `json:"collateral_symbol,omitempty"`
	LLTV            optFloat `json:"lltv"`
	MaxLTV          optFloat `json:"max_ltv"`
	MaxLeverage     optFloat `json:"max_loop_leverage"`
	BorrowAPY       optFloat `json:"borrow_apy"`
	BorrowRewards   optFloat `json:"borrow_rewards_apy"`
	NetBorrowAPY    optFloat `json:"net_borrow_apy"`
	SupplyAPY       optFloat `json:"supply_apy"`
	Utilization     optFloat `json:"utilization"`
	Liquidity       string   `json:"available_liquidity,omitempty"`
	Price           optFloat `json:"oracle_price"` // loan per collateral
	size            float64
}

func (a *App) cmdBorrowMarkets(e *env) (*result, error) {
	t, err := parseLendingTarget(e)
	if err != nil {
		return nil, err
	}
	loan, coll := strings.ToLower(str(e, "loan-token")), strings.ToLower(str(e, "collateral-token"))
	for _, v := range []string{loan, coll} {
		if v != "" && !onchain.ValidAddress(v) {
			return nil, output.Errorf(output.CodeInvalidArgument, "token filters must be addresses")
		}
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	var rows []borrowMarket
	if t.protocol == protoMorpho {
		rows, err = a.morphoMarkets(ctx, e, t.chain, loan, coll)
	} else {
		rows, err = eulerMarkets(ctx, e, t)
	}
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, r := range rows {
		if (loan == "" || strings.EqualFold(r.LoanToken, loan)) && (coll == "" || strings.EqualFold(r.CollateralToken, coll)) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].size > out[j].size })
	if n := e.cmd.Int("limit"); n > 0 && len(out) > n {
		out = out[:n]
	}
	return &result{data: map[string]any{"markets": out}, human: func(w io.Writer) {
		tw := newTable(w)
		fmt.Fprintln(tw, "MARKET\tCOLLATERAL\tLOAN\tLLTV\tBORROW APY\tNET\tLIQUIDITY\tMAX LEV")
		for _, r := range out {
			id := r.MarketID
			if id == "" {
				id = r.CollateralVault + "/" + r.BorrowVault
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%sx\n", id, orDash(r.CollateralSym), orDash(r.LoanSymbol), r.LLTV.pct(), r.BorrowAPY.pct(), r.NetBorrowAPY.pct(), orDash(r.Liquidity), r.MaxLeverage.num(2))
		}
		tw.Flush()
	}}, nil
}

func ltvFields(r *borrowMarket, lltv optFloat) {
	r.LLTV = lltv
	if lltv.ok {
		r.MaxLTV = some(lending.MaxBorrowLTV(lltv.v))
		r.MaxLeverage = some(lending.MaxLoopLeverage(lltv.v))
	}
}

type tokenInfo struct {
	symbol   string
	decimals int
}

// tokenInfos reads symbol and decimals of every token in one multicall.
// Without an RPC, or when the read fails, it reports why and returns what
// it could read; callers show N/A for the rest.
func (a *App) tokenInfos(ctx context.Context, e *env, chain uint64, tokens []string) map[string]tokenInfo {
	out := map[string]tokenInfo{}
	if len(tokens) == 0 {
		return out
	}
	if _, err := onchain.RPCURL(a.Getenv, chain); err != nil {
		e.diag("note: token symbols and decimals unknown on chain %d: %v", chain, err)
		return out
	}
	calls := make([]onchain.Call3, 0, 2*len(tokens))
	for _, tk := range tokens {
		calls = append(calls, onchain.Call3{Target: tk, Data: []byte{0x31, 0x3c, 0xe5, 0x67}}, onchain.Call3{Target: tk, Data: []byte{0x95, 0xd8, 0x9b, 0x41}})
	}
	r, err := onchain.HTTPRPC{Getenv: a.Getenv}.Aggregate(ctx, chain, calls)
	if err != nil {
		e.diag("warning: token metadata multicall failed on chain %d (%d tokens): %v", chain, len(tokens), err)
		return out
	}
	for i, tk := range tokens {
		d, s := r[2*i], r[2*i+1]
		if !d.Success || len(d.Data) != 32 {
			e.diag("warning: token %s on chain %d: decimals() failed; its amounts are unknown", tk, chain)
			continue
		}
		n := new(big.Int).SetBytes(d.Data)
		if !n.IsInt64() || !validDecimals(int(n.Int64())) {
			e.diag("warning: token %s on chain %d: decimals %s out of range", tk, chain, n)
			continue
		}
		out[strings.ToLower(tk)] = tokenInfo{onchain.DecodeSymbol(s.Data), int(n.Int64())}
	}
	return out
}

// morphoMarkets lists borrowable markets, keeping only those matching the
// optional loan and collateral token filters before any metadata is read.
func (a *App) morphoMarkets(ctx context.Context, e *env, chain uint64, loan, coll string) ([]borrowMarket, error) {
	sap, err := a.morphoIndexer(ctx)
	if err != nil {
		return nil, err
	}
	all, err := sap.Markets(ctx, chain)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "morpho markets")
	}
	var markets []lending.SAPMarket
	for _, m := range all {
		if m.Snapshot.CollateralToken == "" || strings.EqualFold(m.Snapshot.CollateralToken, common.Address{}.Hex()) {
			continue // idle market: no collateral, not borrowable
		}
		if (loan == "" || strings.EqualFold(m.Snapshot.LoanToken, loan)) && (coll == "" || strings.EqualFold(m.Snapshot.CollateralToken, coll)) {
			markets = append(markets, m)
		}
	}
	campaigns, campErr := sap.LiveCampaigns(ctx)
	if campErr != nil {
		e.diag("warning: morpho reward campaigns unavailable on chain %d; borrow rewards and net borrow APY are unknown: %v", chain, campErr)
	}
	seen := map[string]bool{}
	var tokens []string
	for _, m := range markets {
		for _, tk := range []string{m.Snapshot.LoanToken, m.Snapshot.CollateralToken} {
			if onchain.ValidAddress(tk) && !seen[strings.ToLower(tk)] {
				seen[strings.ToLower(tk)] = true
				tokens = append(tokens, tk)
			}
		}
	}
	info := a.tokenInfos(ctx, e, chain, tokens)
	now := a.Now().Unix()
	out := make([]borrowMarket, 0, len(markets))
	for _, m := range markets {
		r := borrowMarket{Protocol: protoMorpho, Chain: chain, MarketID: m.Snapshot.MarketID, LoanToken: m.Snapshot.LoanToken, CollateralToken: m.Snapshot.CollateralToken}
		li, lok := info[strings.ToLower(m.Snapshot.LoanToken)]
		ci, cok := info[strings.ToLower(m.Snapshot.CollateralToken)]
		r.LoanSymbol, r.CollateralSym = li.symbol, ci.symbol
		if v, ok := lending.WadFloat(m.Snapshot.LLTV); ok {
			ltvFields(&r, some(v))
		}
		if v, ok := lending.WadFloat(m.Calculated.SupplyAPY); ok {
			r.SupplyAPY = some(v)
		}
		if v, ok := lending.WadFloat(m.Calculated.Utilization); ok {
			r.Utilization = some(v)
		}
		var c *lending.Campaigns
		if campErr == nil {
			c = campaigns
		}
		if br, err := lending.MarketBorrowRate(m, c, now); err != nil {
			e.diag("warning: morpho market %s on chain %d: borrow APY unknown: %v", m.Snapshot.MarketID, chain, err)
		} else {
			r.BorrowAPY = some(br.Borrow)
			if campErr == nil {
				r.NetBorrowAPY, r.BorrowRewards = some(br.Net), some(br.Rewards)
			}
		}
		supply, sok := new(big.Int).SetString(m.Snapshot.TotalSupplyAssets, 10)
		borrow, bok := new(big.Int).SetString(m.Snapshot.TotalBorrowAssets, 10)
		if sok && bok && lok {
			liq := new(big.Int).Sub(supply, borrow)
			if liq.Sign() < 0 {
				liq.SetInt64(0)
			}
			r.Liquidity = formatUnits(liq, li.decimals)
			r.size = lending.Float(supply, li.decimals)
		}
		if p, ok := new(big.Int).SetString(m.Snapshot.OraclePrice, 10); ok && lok && cok && p.Sign() > 0 {
			r.Price = some(lending.MorphoPrice(p, ci.decimals, li.decimals))
		}
		out = append(out, r)
	}
	return out, nil
}

func eulerMarketList(ctx context.Context, e *env, t lendingTarget) ([]*v2.LendingMarket, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	var resp v2.GetLendingMarketsResponse
	if err = e.client(p).Lending(ctx, "GetLendingMarkets", &v2.GetLendingMarketsRequest{Chain: t.caip2(), Protocol: v2.LendingProtocol_LENDING_PROTOCOL_EULER_V2}, &resp, false); err != nil {
		return nil, apiError(err, "GetLendingMarkets")
	}
	return resp.Markets, nil
}

func eulerMarkets(ctx context.Context, e *env, t lendingTarget) ([]borrowMarket, error) {
	markets, err := eulerMarketList(ctx, e, t)
	if err != nil {
		return nil, err
	}
	out := make([]borrowMarket, 0, len(markets))
	for _, m := range markets {
		r := borrowMarket{Protocol: protoEuler, Chain: t.chain, CollateralVault: m.CollateralVaultAddress, BorrowVault: m.BorrowVaultAddress, LoanToken: m.BorrowAssetAddress, LoanSymbol: m.BorrowAssetSymbol, CollateralToken: m.CollateralAssetAddress, CollateralSym: m.CollateralAssetSymbol}
		ltvFields(&r, parseOpt(m.LiquidationLtv))
		// Euler caps new borrows at the vault's borrow LTV, below liquidation.
		if b := parseOpt(m.BorrowLtv); b.ok && r.MaxLTV.ok && b.v < r.MaxLTV.v {
			r.MaxLTV = b
		}
		r.BorrowAPY, r.NetBorrowAPY, r.SupplyAPY = parseOpt(m.BorrowApy), parseOpt(m.BorrowApy), parseOpt(m.SupplyApy)
		if r.BorrowAPY.ok {
			r.BorrowRewards = some(0)
		}
		if p := parseOpt(m.OraclePrice); p.ok && p.v > 0 {
			r.Price = p
		}
		r.Liquidity = rawToUnits(m.AvailableLiquidity, int(m.BorrowAssetDecimals))
		if liq := parseOpt(r.Liquidity); liq.ok {
			r.size = liq.v
		}
		out = append(out, r)
	}
	return out, nil
}

// borrowPosition is one collateral/debt position with its risk. Unknown
// values (no oracle price, no debt) are null, never zero.
type borrowPosition struct {
	Protocol         string   `json:"protocol"`
	Chain            uint64   `json:"chain_id"`
	MarketID         string   `json:"market_id,omitempty"`
	CollateralVault  string   `json:"collateral_vault,omitempty"`
	BorrowVault      string   `json:"borrow_vault,omitempty"`
	SubAccount       *int     `json:"sub_account,omitempty"`
	CollateralSymbol string   `json:"collateral_symbol,omitempty"`
	LoanSymbol       string   `json:"loan_symbol,omitempty"`
	Collateral       string   `json:"collateral"`
	Debt             string   `json:"debt"`
	LTV              optFloat `json:"ltv"`
	LLTV             optFloat `json:"lltv"`
	MaxLTV           optFloat `json:"max_ltv"`
	HealthFactor     optFloat `json:"health_factor"`
	LiquidationPrice optFloat `json:"liquidation_price"`
	Leverage         optFloat `json:"leverage"`
	Risk             string   `json:"risk"`
}

func riskFields(p *borrowPosition, pos lending.Position) {
	r, err := lending.Assess(pos)
	if err != nil {
		p.Risk = "unknown: " + err.Error()
		return
	}
	p.LLTV, p.MaxLTV = some(r.LLTV), some(r.MaxLTV)
	if r.HealthFactor == nil {
		p.Risk = "no debt"
		return
	}
	p.LTV, p.HealthFactor, p.LiquidationPrice = some(r.LTV), optPtr(r.HealthFactor), optPtr(r.LiquidationPrice)
	if r.LTV < 1 {
		p.Leverage = some(1 / (1 - r.LTV))
	}
	p.Risk = map[lending.Verdict]string{lending.Safe: "ok", lending.HighRisk: "high", lending.Refused: "above max LTV"}[lending.Classify(r)]
}

func (a *App) cmdBorrowPositions(e *env) (*result, error) {
	t, err := parseLendingTarget(e)
	if err != nil {
		return nil, err
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	var out []borrowPosition
	if t.protocol == protoMorpho {
		out, err = a.morphoBorrowPositions(ctx, t.chain, w.agent)
	} else {
		out, err = eulerBorrowPositions(ctx, e, w, t)
	}
	if err != nil {
		return nil, err
	}
	return &result{data: map[string]any{"agent": w.agent, "positions": out}, human: func(wr io.Writer) {
		tw := newTable(wr)
		fmt.Fprintln(tw, "MARKET\tCOLLATERAL\tDEBT\tLTV\tLLTV\tHF\tLIQ PRICE\tRISK")
		for _, p := range out {
			id := p.MarketID
			if id == "" {
				id = p.CollateralVault + "/" + p.BorrowVault
			}
			fmt.Fprintf(tw, "%s\t%s %s\t%s %s\t%s\t%s\t%s\t%s\t%s\n", id, p.Collateral, p.CollateralSymbol, p.Debt, p.LoanSymbol, p.LTV.pct(), p.LLTV.pct(), p.HealthFactor.num(3), p.LiquidationPrice.num(4), p.Risk)
		}
		tw.Flush()
	}}, nil
}

func morphoPosition(chain uint64, s *lending.MarketSnapshot, debt *big.Int) borrowPosition {
	p := borrowPosition{Protocol: protoMorpho, Chain: chain, MarketID: s.Params.ID().Hex(), CollateralSymbol: s.CollateralSymbol, LoanSymbol: s.LoanSymbol, Collateral: formatUnits(s.Position.Collateral, s.CollateralDecimals), Debt: formatUnits(debt, s.LoanDecimals)}
	pos := lending.Position{Collateral: lending.Float(s.Position.Collateral, s.CollateralDecimals), Debt: lending.Float(debt, s.LoanDecimals), LLTV: lending.Float(s.Params.Lltv, 18)}
	if s.OraclePrice != nil {
		pos.Price = lending.MorphoPrice(s.OraclePrice, s.CollateralDecimals, s.LoanDecimals)
	}
	riskFields(&p, pos)
	return p
}

func (a *App) morphoBorrowPositions(ctx context.Context, chain uint64, agent string) ([]borrowPosition, error) {
	d, err := lending.Morpho(chain)
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "morpho")
	}
	sap, err := a.morphoIndexer(ctx)
	if err != nil {
		return nil, err
	}
	active, err := sap.ActiveMarkets(ctx, agent)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "morpho active markets")
	}
	var ids []common.Hash
	for _, m := range active {
		if m.ChainID == chain {
			ids = append(ids, common.HexToHash(m.MarketID))
		}
	}
	out := []borrowPosition{}
	if len(ids) == 0 {
		return out, nil
	}
	snaps, err := lending.ReadMarkets(ctx, onchain.HTTPRPC{Getenv: a.Getenv}, d, ids, addr(agent))
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "morpho positions")
	}
	now := a.Now().Unix()
	for _, s := range snaps {
		if s.Position.Collateral.Sign() == 0 && s.Position.BorrowShares.Sign() == 0 {
			continue // supply-only or closed
		}
		out = append(out, morphoPosition(chain, s, s.Debt(now)))
	}
	return out, nil
}

func eulerBorrowPositions(ctx context.Context, e *env, w *resolvedWallet, t lendingTarget) ([]borrowPosition, error) {
	var resp v2.GetLendingUserPositionsResponse
	if err := e.client(w.profile).Lending(ctx, "GetLendingUserPositions", &v2.GetLendingUserPositionsRequest{Chain: t.caip2(), UserAddress: w.agent, MaxSubAccounts: eulerSubAccounts}, &resp, false); err != nil {
		return nil, apiError(err, "GetLendingUserPositions")
	}
	markets, err := eulerMarketList(ctx, e, t)
	if err != nil {
		return nil, err
	}
	byPair := map[string]*v2.LendingMarket{}
	for _, m := range markets {
		byPair[strings.ToLower(m.CollateralVaultAddress+m.BorrowVaultAddress)] = m
	}
	out := []borrowPosition{}
	for _, pos := range resp.Positions {
		if pos.BorrowVaultAddress == "" {
			continue
		}
		for _, h := range pos.Holdings {
			if !h.EnabledAsCollateral {
				continue
			}
			m := byPair[strings.ToLower(h.VaultAddress+pos.BorrowVaultAddress)]
			if m == nil {
				continue // not a listed pair
			}
			out = append(out, eulerPosition(t.chain, pos, h.Assets, m))
		}
	}
	return out, nil
}

func eulerPosition(chain uint64, pos *v2.LendingUserPosition, collRaw string, m *v2.LendingMarket) borrowPosition {
	cd, ld := int(m.CollateralAssetDecimals), int(m.BorrowAssetDecimals)
	sub := int(pos.SubAccountIndex)
	p := borrowPosition{Protocol: protoEuler, Chain: chain, CollateralVault: m.CollateralVaultAddress, BorrowVault: m.BorrowVaultAddress, SubAccount: &sub, CollateralSymbol: m.CollateralAssetSymbol, LoanSymbol: m.BorrowAssetSymbol, Collateral: rawToUnits(collRaw, cd), Debt: rawToUnits(pos.DebtAssets, ld)}
	coll, okc := new(big.Int).SetString(collRaw, 10)
	debt, okd := new(big.Int).SetString(pos.DebtAssets, 10)
	if !okc || !okd || !validDecimals(cd) || !validDecimals(ld) {
		p.Risk = "unknown: position amounts or decimals unavailable"
		return p
	}
	lp := lending.Position{Collateral: lending.Float(coll, cd), Debt: lending.Float(debt, ld), Price: parseOpt(m.OraclePrice).v, LLTV: parseOpt(m.LiquidationLtv).v}
	riskFields(&p, lp)
	return p
}
