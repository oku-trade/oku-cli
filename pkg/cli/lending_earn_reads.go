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

func earnVaultsFlags() []urfave.Flag {
	return append(lendingTargetFlags(),
		&urfave.StringFlag{Name: "asset", Usage: "only vaults of this asset (token address)"},
		&urfave.IntFlag{Name: "limit", Value: 25, Usage: "maximum vaults to show, highest net APY first"},
	)
}

// earnVault is one vault row. Unknown values are null, never zero.
type earnVault struct {
	Protocol     string   `json:"protocol"`
	Chain        uint64   `json:"chain_id"`
	Vault        string   `json:"vault"`
	Name         string   `json:"name"`
	Asset        string   `json:"asset"`
	AssetSymbol  string   `json:"asset_symbol,omitempty"`
	Decimals     *int     `json:"asset_decimals"`
	TotalAssets  string   `json:"total_assets,omitempty"`
	Liquidity    string   `json:"available_liquidity,omitempty"`
	GrossAPY     optFloat `json:"gross_apy"`
	BaseAPY      optFloat `json:"base_apy"`
	RewardsAPY   optFloat `json:"rewards_apy"`
	NetAPY       optFloat `json:"net_apy"`
	Rewards      []string `json:"reward_sources,omitempty"`
	PerfFee      optFloat `json:"performance_fee"`
	Version      int      `json:"version,omitempty"`
	DepositsOpen bool     `json:"deposits_open"`
}

func (a *App) cmdEarnVaults(e *env) (*result, error) {
	t, err := parseLendingTarget(e)
	if err != nil {
		return nil, err
	}
	asset := strings.ToLower(str(e, "asset"))
	if asset != "" && !onchain.ValidAddress(asset) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--asset must be a token address")
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	var rows []earnVault
	if t.protocol == protoMorpho {
		rows, err = a.morphoVaults(ctx, e, t.chain)
	} else {
		rows, err = eulerVaults(ctx, e, t)
	}
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, r := range rows {
		if asset == "" || strings.EqualFold(r.Asset, asset) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].NetAPY.v > out[j].NetAPY.v })
	if n := e.cmd.Int("limit"); n > 0 && len(out) > n {
		out = out[:n]
	}
	return &result{data: map[string]any{"vaults": out}, human: func(w io.Writer) {
		tw := newTable(w)
		fmt.Fprintln(tw, "VAULT\tNAME\tASSET\tNET APY\tBASE\tREWARDS\tTVL")
		for _, r := range out {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Vault, r.Name, orDash(r.AssetSymbol), r.NetAPY.pct(), r.BaseAPY.pct(), r.RewardsAPY.pct(), orDash(r.TotalAssets))
		}
		tw.Flush()
	}}, nil
}

func (a *App) morphoVaults(ctx context.Context, e *env, chain uint64) ([]earnVault, error) {
	sap, err := a.morphoIndexer(ctx)
	if err != nil {
		return nil, err
	}
	vaults, err := sap.Vaults(ctx, chain)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "morpho vaults")
	}
	// Rewards are additive. Without them the base APY is still exact, so a
	// rewards outage reports rewards and net APY as unknown instead of
	// failing the list.
	campaigns, campErr := sap.LiveCampaigns(ctx)
	if campErr != nil {
		e.diag("warning: morpho reward campaigns unavailable on chain %d; rewards and net APY are unknown: %v", chain, campErr)
	}
	now := a.Now().Unix()
	out := make([]earnVault, 0, len(vaults))
	for _, v := range vaults {
		row := earnVault{Protocol: protoMorpho, Chain: chain, Vault: v.Snapshot.Address, Name: v.Snapshot.Name, Asset: v.Snapshot.Token, Decimals: v.Snapshot.TokenDecimals, Version: v.Snapshot.Version, DepositsOpen: v.Snapshot.Version == 2 || len(v.Snapshot.SupplyQueue) > 0}
		if v.Snapshot.TokenDecimals != nil {
			row.TotalAssets = rawToUnits(v.Snapshot.LastTotalAssets, *v.Snapshot.TokenDecimals)
			row.Liquidity = rawToUnits(v.Calculated.AvailableLiquidity, *v.Snapshot.TokenDecimals)
		}
		if fee, ok := lending.WadFloat(v.Snapshot.Fee); ok {
			row.PerfFee = some(fee)
		}
		var c *lending.Campaigns
		if campErr == nil {
			c = campaigns
		}
		if y, err := lending.VaultYield(v, c, now); err != nil {
			e.diag("warning: morpho vault %s on chain %d: APY unknown: %v", v.Snapshot.Address, chain, err)
		} else {
			row.GrossAPY, row.BaseAPY = some(y.Gross), some(y.Base)
			if campErr == nil {
				row.NetAPY, row.RewardsAPY, row.Rewards = some(y.Net), some(y.Rewards), y.Sources
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func eulerVaults(ctx context.Context, e *env, t lendingTarget) ([]earnVault, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	var resp v2.GetLendingVaultsResponse
	if err = e.client(p).Lending(ctx, "GetLendingVaults", &v2.GetLendingVaultsRequest{Chain: t.caip2(), Protocol: v2.LendingProtocol_LENDING_PROTOCOL_EULER_V2}, &resp, false); err != nil {
		return nil, apiError(err, "GetLendingVaults")
	}
	out := make([]earnVault, 0, len(resp.Vaults))
	for _, v := range resp.Vaults {
		d := int(v.AssetDecimals)
		row := earnVault{Protocol: protoEuler, Chain: t.chain, Vault: v.Address, Name: v.Name, Asset: v.AssetAddress, AssetSymbol: v.AssetSymbol, Decimals: &d, TotalAssets: rawToUnits(v.TotalAssets, d), Liquidity: rawToUnits(v.AvailableLiquidity, d), DepositsOpen: true}
		// The backend reports supply APY from the on-chain rate; Euler has no
		// reward programs in the web app, so net equals base.
		row.BaseAPY, row.NetAPY, row.GrossAPY = parseOpt(v.SupplyApy), parseOpt(v.SupplyApy), parseOpt(v.SupplyApy)
		if row.BaseAPY.ok {
			row.RewardsAPY = some(0)
		}
		out = append(out, row)
	}
	return out, nil
}

// rawToUnits renders a raw integer string in token units, or "" if malformed.
func rawToUnits(raw string, decimals int) string {
	v, ok := new(big.Int).SetString(raw, 10)
	if !ok || v.Sign() < 0 {
		return ""
	}
	return formatUnits(v, decimals)
}

type earnPosition struct {
	Protocol    string `json:"protocol"`
	Chain       uint64 `json:"chain_id"`
	Vault       string `json:"vault"`
	Asset       string `json:"asset"`
	AssetSymbol string `json:"asset_symbol,omitempty"`
	Shares      string `json:"shares"`
	Assets      string `json:"assets"`
	SubAccount  *int   `json:"sub_account,omitempty"`
}

func (a *App) cmdEarnPositions(e *env) (*result, error) {
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
	var out []earnPosition
	if t.protocol == protoMorpho {
		out, err = a.morphoEarnPositions(ctx, t.chain, w.agent)
	} else {
		out, err = eulerEarnPositions(ctx, e, w, t)
	}
	if err != nil {
		return nil, err
	}
	return &result{data: map[string]any{"agent": w.agent, "positions": out}, human: func(wr io.Writer) {
		tw := newTable(wr)
		fmt.Fprintln(tw, "VAULT\tASSET\tASSETS\tSHARES")
		for _, p := range out {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Vault, orDash(p.AssetSymbol), p.Assets, p.Shares)
		}
		tw.Flush()
	}}, nil
}

func (a *App) morphoEarnPositions(ctx context.Context, chain uint64, agent string) ([]earnPosition, error) {
	sap, err := a.morphoIndexer(ctx)
	if err != nil {
		return nil, err
	}
	active, err := sap.ActiveVaults(ctx, agent)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "morpho active vaults")
	}
	var vaults []common.Address
	for _, v := range active {
		if v.ChainID == chain {
			vaults = append(vaults, addr(v.Address))
		}
	}
	if len(vaults) == 0 {
		return []earnPosition{}, nil
	}
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	holdings, err := lending.ReadHoldings(ctx, rpc, chain, addr(agent), vaults)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "vault balances")
	}
	all, err := sap.Vaults(ctx, chain)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "morpho vaults")
	}
	meta := map[common.Address]lending.SAPVault{}
	for _, v := range all {
		meta[addr(v.Snapshot.Address)] = v
	}
	out := []earnPosition{}
	for _, h := range holdings {
		if h.Shares.Sign() == 0 {
			continue
		}
		v := meta[h.Vault]
		p := earnPosition{Protocol: protoMorpho, Chain: chain, Vault: h.Vault.Hex(), Asset: v.Snapshot.Token, Shares: h.Shares.String(), Assets: h.Assets.String()}
		if v.Snapshot.TokenDecimals != nil {
			p.Assets = formatUnits(h.Assets, *v.Snapshot.TokenDecimals)
		}
		out = append(out, p)
	}
	return out, nil
}

func eulerEarnPositions(ctx context.Context, e *env, w *resolvedWallet, t lendingTarget) ([]earnPosition, error) {
	c := e.client(w.profile)
	var resp v2.GetLendingUserPositionsResponse
	if err := c.Lending(ctx, "GetLendingUserPositions", &v2.GetLendingUserPositionsRequest{Chain: t.caip2(), UserAddress: w.agent, MaxSubAccounts: eulerSubAccounts, IncludeVaultScan: true}, &resp, false); err != nil {
		return nil, apiError(err, "GetLendingUserPositions")
	}
	var vaults v2.GetLendingVaultsResponse
	if err := c.Lending(ctx, "GetLendingVaults", &v2.GetLendingVaultsRequest{Chain: t.caip2(), Protocol: v2.LendingProtocol_LENDING_PROTOCOL_EULER_V2}, &vaults, false); err != nil {
		return nil, apiError(err, "GetLendingVaults")
	}
	meta := map[string]*v2.LendingVault{}
	for _, v := range vaults.Vaults {
		meta[strings.ToLower(v.Address)] = v
	}
	out := []earnPosition{}
	for _, pos := range resp.Positions {
		for _, h := range pos.Holdings {
			// Earn positions are plain deposits: not collateral for a borrow.
			if h.EnabledAsCollateral && pos.BorrowVaultAddress != "" {
				continue
			}
			p := earnPosition{Protocol: protoEuler, Chain: t.chain, Vault: h.VaultAddress, Shares: h.Shares, Assets: h.Assets}
			if v := meta[strings.ToLower(h.VaultAddress)]; v != nil {
				p.Asset, p.AssetSymbol, p.Assets = v.AssetAddress, v.AssetSymbol, rawToUnits(h.Assets, int(v.AssetDecimals))
			}
			if pos.SubAccountIndex > 0 {
				i := int(pos.SubAccountIndex)
				p.SubAccount = &i
			}
			out = append(out, p)
		}
	}
	return out, nil
}
