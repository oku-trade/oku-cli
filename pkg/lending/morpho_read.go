package lending

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gfx-labs/chain-config/dist/networks"
	"github.com/oku-trade/oku-cli/pkg/onchain"
)

// Aggregator runs read calls in one Multicall3 round trip.
type Aggregator interface {
	Aggregate(ctx context.Context, chain uint64, calls []onchain.Call3) ([]onchain.Result3, error)
}

// Morpho returns the chain's Morpho Blue deployment from chain-config, or
// an error when the chain has no complete Bundler3 stack.
func Morpho(chain uint64) (MorphoDeployment, error) {
	n, err := networks.NetworkByNumber(chain)
	if err != nil || n == nil {
		return MorphoDeployment{}, fmt.Errorf("chain %d is not in chain-config", chain)
	}
	d := MorphoDeployment{ChainID: chain, Morpho: n.Morpho.Morpho, Bundler3: n.Morpho.Bundler3, GeneralAdapter1: n.Morpho.GeneralAdapter1}
	if !d.Valid() {
		return MorphoDeployment{}, fmt.Errorf("morpho is not deployed with Bundler3 on chain %d", chain)
	}
	return d, nil
}

// Euler returns the chain's Ethereum Vault Connector from chain-config.
func Euler(chain uint64) (common.Address, error) {
	n, err := networks.NetworkByNumber(chain)
	if err != nil || n == nil || n.Euler.Evc == (common.Address{}) {
		return common.Address{}, fmt.Errorf("euler is not deployed on chain %d", chain)
	}
	return n.Euler.Evc, nil
}

// MarketSnapshot is everything a Morpho Blue borrow action needs, read in
// one multicall at latest.
type MarketSnapshot struct {
	Params             MarketParams
	Market             MarketState
	Position           PositionState
	OraclePrice        *big.Int // 1e36 scaled loan per collateral, raw units
	BorrowRate         *big.Int // WAD per second
	LoanDecimals       int
	CollateralDecimals int
	LoanSymbol         string
	CollateralSymbol   string
	LoanAllowance      *big.Int // user -> GeneralAdapter1
	CollateralAllow    *big.Int // user -> GeneralAdapter1
	LoanBalance        *big.Int
	CollateralBalance  *big.Int
	Authorized         bool // GeneralAdapter1 may act for the user on Morpho
}

func call(to common.Address, method string, args ...any) onchain.Call3 {
	return onchain.Call3{Target: to.Hex(), Data: pack(method, args...)}
}

func unpack(method string, r onchain.Result3) ([]any, error) {
	if !r.Success {
		return nil, fmt.Errorf("%s reverted", method)
	}
	return morphoABI.Methods[method].Outputs.Unpack(r.Data)
}

func unpackUint(method string, r onchain.Result3) (*big.Int, error) {
	v, err := unpack(method, r)
	if err != nil {
		return nil, err
	}
	n, ok := v[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("%s: unexpected result", method)
	}
	return n, nil
}

var erc20Meta = struct{ decimals, symbol []byte }{[]byte{0x31, 0x3c, 0xe5, 0x67}, []byte{0x95, 0xd8, 0x9b, 0x41}}

func decimals(r onchain.Result3) (int, error) {
	if !r.Success || len(r.Data) != 32 {
		return 0, errors.New("decimals unavailable")
	}
	d := new(big.Int).SetBytes(r.Data)
	if !d.IsInt64() || d.Int64() > 36 {
		return 0, errors.New("invalid decimals")
	}
	return int(d.Int64()), nil
}

// ReadMarket reads one Morpho Blue market and the user's position in it.
func ReadMarket(ctx context.Context, rpc Aggregator, d MorphoDeployment, id common.Hash, user common.Address) (*MarketSnapshot, error) {
	out, err := ReadMarkets(ctx, rpc, d, []common.Hash{id}, user)
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

type marketBorrowState struct {
	TotalSupplyAssets *big.Int `abi:"totalSupplyAssets"`
	TotalSupplyShares *big.Int `abi:"totalSupplyShares"`
	TotalBorrowAssets *big.Int `abi:"totalBorrowAssets"`
	TotalBorrowShares *big.Int `abi:"totalBorrowShares"`
	LastUpdate        *big.Int `abi:"lastUpdate"`
	Fee               *big.Int `abi:"fee"`
}

const marketCalls = 4

// ReadMarkets reads many markets and the user's positions in two multicall
// round trips in total, whatever the number of markets.
func ReadMarkets(ctx context.Context, rpc Aggregator, d MorphoDeployment, ids []common.Hash, user common.Address) ([]*MarketSnapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	first := make([]onchain.Call3, 0, len(ids)*marketCalls)
	for _, id := range ids {
		first = append(first, call(d.Morpho, "idToMarketParams", id), call(d.Morpho, "market", id), call(d.Morpho, "position", id, user), call(d.Morpho, "isAuthorized", user, d.GeneralAdapter1))
	}
	r1, err := rpc.Aggregate(ctx, d.ChainID, first)
	if err != nil {
		return nil, err
	}
	out := make([]*MarketSnapshot, len(ids))
	second := []onchain.Call3{}
	type slot struct{ start, n int }
	slots := make([]slot, len(ids))
	for i, id := range ids {
		r := r1[i*marketCalls : (i+1)*marketCalls]
		p, err := unpack("idToMarketParams", r[0])
		if err != nil {
			return nil, err
		}
		mp := MarketParams{LoanToken: p[0].(common.Address), CollateralToken: p[1].(common.Address), Oracle: p[2].(common.Address), Irm: p[3].(common.Address), Lltv: p[4].(*big.Int)}
		if mp.LoanToken == (common.Address{}) || mp.ID() != id {
			return nil, fmt.Errorf("morpho market %s does not exist on chain %d", id.Hex(), d.ChainID)
		}
		m, err := unpack("market", r[1])
		if err != nil {
			return nil, err
		}
		pos, err := unpack("position", r[2])
		if err != nil {
			return nil, err
		}
		auth, err := unpack("isAuthorized", r[3])
		if err != nil {
			return nil, err
		}
		s := &MarketSnapshot{
			Params:     mp,
			Market:     MarketState{m[0].(*big.Int), m[1].(*big.Int), m[2].(*big.Int), m[3].(*big.Int), m[4].(*big.Int), m[5].(*big.Int)},
			Position:   PositionState{pos[0].(*big.Int), pos[1].(*big.Int), pos[2].(*big.Int)},
			Authorized: auth[0].(bool),
		}
		out[i] = s
		calls := []onchain.Call3{
			{Target: mp.LoanToken.Hex(), Data: erc20Meta.decimals},
			{Target: mp.CollateralToken.Hex(), Data: erc20Meta.decimals},
			{Target: mp.LoanToken.Hex(), Data: erc20Meta.symbol},
			{Target: mp.CollateralToken.Hex(), Data: erc20Meta.symbol},
			call(mp.LoanToken, "allowance", user, d.GeneralAdapter1),
			call(mp.CollateralToken, "allowance", user, d.GeneralAdapter1),
			call(mp.LoanToken, "balanceOf", user),
			call(mp.CollateralToken, "balanceOf", user),
		}
		if mp.Oracle != (common.Address{}) {
			calls = append(calls, call(mp.Oracle, "price"))
		}
		if mp.Irm != (common.Address{}) {
			st := s.Market
			calls = append(calls, call(mp.Irm, "borrowRateView", mp, marketBorrowState{st.TotalSupplyAssets, st.TotalSupplyShares, st.TotalBorrowAssets, st.TotalBorrowShares, st.LastUpdate, st.Fee}))
		}
		slots[i] = slot{len(second), len(calls)}
		second = append(second, calls...)
	}
	r2, err := rpc.Aggregate(ctx, d.ChainID, second)
	if err != nil {
		return nil, err
	}
	for i, s := range out {
		r := r2[slots[i].start : slots[i].start+slots[i].n]
		if s.LoanDecimals, err = decimals(r[0]); err != nil {
			return nil, fmt.Errorf("loan token: %w", err)
		}
		if s.CollateralDecimals, err = decimals(r[1]); err != nil {
			return nil, fmt.Errorf("collateral token: %w", err)
		}
		s.LoanSymbol, s.CollateralSymbol = onchain.DecodeSymbol(r[2].Data), onchain.DecodeSymbol(r[3].Data)
		for k, dst := range []**big.Int{&s.LoanAllowance, &s.CollateralAllow, &s.LoanBalance, &s.CollateralBalance} {
			method := "allowance"
			if k >= 2 {
				method = "balanceOf"
			}
			if *dst, err = unpackUint(method, r[4+k]); err != nil {
				return nil, err
			}
		}
		next := 8
		if s.Params.Oracle != (common.Address{}) {
			// A reverting oracle leaves the price unknown (N/A), never zero.
			if p, perr := unpackUint("price", r[next]); perr == nil && p.Sign() > 0 {
				s.OraclePrice = p
			}
			next++
		}
		if s.Params.Irm != (common.Address{}) {
			if s.BorrowRate, err = unpackUint("borrowRateView", r[next]); err != nil {
				return nil, fmt.Errorf("borrow rate: %w", err)
			}
		}
	}
	return out, nil
}

// Debt is the user's current debt in loan-token raw units, accrued to `at`.
func (s *MarketSnapshot) Debt(at int64) *big.Int {
	return s.Market.AccrueBorrow(s.BorrowRate, at).ToBorrowAssetsUp(s.Position.BorrowShares)
}

// VaultSnapshot is an ERC-4626 vault, its asset and the user's holding.
type VaultSnapshot struct {
	Asset         common.Address
	AssetDecimals int
	AssetSymbol   string
	Shares        *big.Int // user share balance
	Assets        *big.Int // convertToAssets(Shares)
	Allowance     *big.Int // asset allowance user -> spender
	Balance       *big.Int // user asset balance
	SharesFor     *big.Int // convertToShares(amount), nil when amount is nil
}

// ReadVault reads an ERC-4626 vault for a deposit or withdraw. spender is
// the address the deposit pulls through (GeneralAdapter1 for Morpho).
func ReadVault(ctx context.Context, rpc Aggregator, chain uint64, vault, user, spender common.Address, amount *big.Int) (*VaultSnapshot, error) {
	r, err := rpc.Aggregate(ctx, chain, []onchain.Call3{call(vault, "asset"), call(vault, "balanceOf", user)})
	if err != nil {
		return nil, err
	}
	a, err := unpack("asset", r[0])
	if err != nil {
		return nil, fmt.Errorf("%s is not an ERC-4626 vault: %w", vault.Hex(), err)
	}
	s := &VaultSnapshot{Asset: a[0].(common.Address)}
	if s.Shares, err = unpackUint("balanceOf", r[1]); err != nil {
		return nil, err
	}
	calls := []onchain.Call3{
		{Target: s.Asset.Hex(), Data: erc20Meta.decimals},
		{Target: s.Asset.Hex(), Data: erc20Meta.symbol},
		call(vault, "convertToAssets", s.Shares),
		call(s.Asset, "allowance", user, spender),
		call(s.Asset, "balanceOf", user),
	}
	if amount != nil {
		calls = append(calls, call(vault, "convertToShares", amount))
	}
	r, err = rpc.Aggregate(ctx, chain, calls)
	if err != nil {
		return nil, err
	}
	if s.AssetDecimals, err = decimals(r[0]); err != nil {
		return nil, fmt.Errorf("vault asset: %w", err)
	}
	s.AssetSymbol = onchain.DecodeSymbol(r[1].Data)
	if s.Assets, err = unpackUint("convertToAssets", r[2]); err != nil {
		return nil, err
	}
	if s.Allowance, err = unpackUint("allowance", r[3]); err != nil {
		return nil, err
	}
	if s.Balance, err = unpackUint("balanceOf", r[4]); err != nil {
		return nil, err
	}
	if amount != nil {
		if s.SharesFor, err = unpackUint("convertToShares", r[5]); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// VaultHolding is one vault share balance valued in assets.
type VaultHolding struct {
	Vault  common.Address
	Shares *big.Int
	Assets *big.Int
}

// ReadHoldings values the user's shares in each vault with one multicall
// for balances and one for conversions (no per-vault round trips).
func ReadHoldings(ctx context.Context, rpc Aggregator, chain uint64, user common.Address, vaults []common.Address) ([]VaultHolding, error) {
	if len(vaults) == 0 {
		return nil, nil
	}
	calls := make([]onchain.Call3, len(vaults))
	for i, v := range vaults {
		calls[i] = call(v, "balanceOf", user)
	}
	r, err := rpc.Aggregate(ctx, chain, calls)
	if err != nil {
		return nil, err
	}
	out := make([]VaultHolding, len(vaults))
	conv := make([]onchain.Call3, len(vaults))
	for i, v := range vaults {
		if out[i].Shares, err = unpackUint("balanceOf", r[i]); err != nil {
			return nil, fmt.Errorf("vault %s: %w", v.Hex(), err)
		}
		out[i].Vault = v
		conv[i] = call(v, "convertToAssets", out[i].Shares)
	}
	r, err = rpc.Aggregate(ctx, chain, conv)
	if err != nil {
		return nil, err
	}
	for i := range vaults {
		if out[i].Assets, err = unpackUint("convertToAssets", r[i]); err != nil {
			return nil, fmt.Errorf("vault %s: %w", vaults[i].Hex(), err)
		}
	}
	return out, nil
}
