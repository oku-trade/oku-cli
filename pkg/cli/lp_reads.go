package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/config"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/uniswaplp"
	urfave "github.com/urfave/cli/v3"
)

// lpListLimit bounds how many v3 positions list enumerates on chain.
const lpListLimit = 50

func lpChainFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID (needs a chain RPC: oku rpc set or OKU_RPC_URL_<id>)"},
		&urfave.StringFlag{Name: "version", Value: "v3", Usage: "Uniswap version: v3 or v4"},
	}
}

func lpPoolFlags() []urfave.Flag {
	return append(lpChainFlags(),
		&urfave.StringFlag{Name: "token0", Usage: "pool token address, unambiguous symbol, or native (either order)"},
		&urfave.StringFlag{Name: "token1", Usage: "the other pool token"},
		&urfave.IntFlag{Name: "fee", Usage: "fee tier in hundredths of a bip: 100, 500, 3000, 10000"},
		&urfave.IntFlag{Name: "tick-spacing", Usage: "v4 only: tick spacing (default from the fee tier)"},
		&urfave.StringFlag{Name: "hooks", Usage: "v4 only: hooks contract address (default none)"},
	)
}

func lpPositionFlags() []urfave.Flag {
	return append(lpChainFlags(), &urfave.StringFlag{Name: "token-id", Usage: "position NFT token ID"})
}

func lpListFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID (needs a chain RPC: oku rpc set or OKU_RPC_URL_<id>)"},
		&urfave.StringFlag{Name: "version", Usage: "v3 or v4 (default both)"},
		&urfave.StringFlag{Name: "token-id", Usage: "comma-separated v4 token IDs to include when Oku portfolio data is unavailable"},
	}
}

// lpTarget is a validated chain and deployment.
type lpTarget struct {
	chain uint64
	d     uniswaplp.Deployment
	rpc   onchain.HTTPRPC
}

func (a *App) lpTarget(e *env, version string) (lpTarget, error) {
	chain, err := strconv.ParseUint(str(e, "chain"), 10, 64)
	if err != nil || !config.ValidChainID(chain) {
		return lpTarget{}, output.Errorf(output.CodeInvalidArgument, "--chain must be an EVM chain ID")
	}
	v, err := uniswaplp.ParseVersion(version)
	if err != nil {
		return lpTarget{}, output.Wrap(output.CodeInvalidArgument, err, "--version")
	}
	d, err := uniswaplp.Lookup(chain, v)
	if err != nil {
		return lpTarget{}, output.Wrap(output.CodeInvalidArgument, err, "uniswap deployment")
	}
	if _, err := a.rpcResolver().URL(chain); err != nil {
		return lpTarget{}, output.Wrap(output.CodeConfigError, err, "chain RPC")
	}
	return lpTarget{chain, d, a.httpRPC()}, nil
}

// lpToken is a resolved pool-token argument. native is true when the user
// asked for the chain's native token.
type lpToken struct {
	addr   common.Address
	native bool
}

// resolveLPToken maps "native" to address zero on v4 and to the wrapped
// native token on v3, where pools hold WETH and native is paid through it.
func (a *App) resolveLPToken(ctx context.Context, e *env, t lpTarget, name string) (lpToken, error) {
	w, err := e.profile()
	if err != nil {
		return lpToken{}, err
	}
	resolved, err := a.resolveRouteToken(ctx, e.client(w), t.chain, strings.TrimSpace(name))
	if err != nil {
		return lpToken{}, err
	}
	if resolved != "native" {
		return lpToken{addr: common.HexToAddress(resolved)}, nil
	}
	if t.d.Version == uniswaplp.V3 {
		if t.d.WrappedNative == (common.Address{}) {
			return lpToken{}, output.Errorf(output.CodeInvalidArgument, "no wrapped native token on chain %d", t.chain)
		}
		return lpToken{addr: t.d.WrappedNative, native: true}, nil
	}
	return lpToken{addr: common.Address{}, native: true}, nil
}

func (a *App) lpLoadPool(ctx context.Context, e *env, t lpTarget) (uniswaplp.Pool, error) {
	var toks [2]lpToken
	for i, name := range []string{str(e, "token0"), str(e, "token1")} {
		tok, err := a.resolveLPToken(ctx, e, t, name)
		if err != nil {
			return uniswaplp.Pool{}, err
		}
		toks[i] = tok
	}
	hooks := common.Address{}
	if h := str(e, "hooks"); h != "" {
		if t.d.Version != uniswaplp.V4 || !onchain.ValidAddress(h) {
			return uniswaplp.Pool{}, output.Errorf(output.CodeInvalidArgument, "--hooks must be a v4 hooks contract address")
		}
		hooks = common.HexToAddress(h)
	}
	spacing := e.cmd.Int("tick-spacing")
	if spacing != 0 && t.d.Version != uniswaplp.V4 {
		return uniswaplp.Pool{}, output.Errorf(output.CodeInvalidArgument, "--tick-spacing applies to v4 only; v3 spacing follows the fee tier")
	}
	pool, err := uniswaplp.LoadPool(ctx, t.rpc, t.d, uniswaplp.PoolQuery{Token0: toks[0].addr, Token1: toks[1].addr, Fee: e.cmd.Int("fee"), TickSpacing: spacing, Hooks: hooks})
	if errors.Is(err, uniswaplp.ErrPoolNotFound) {
		return pool, output.Wrap(output.CodeNotFound, err, "pool")
	}
	if err != nil {
		return pool, lpReadError(err, "pool")
	}
	meta, err := uniswaplp.TokenMeta(ctx, t.rpc, t.chain, t.d.NativeSymbol, []string{pool.Token0.Address, pool.Token1.Address})
	if err != nil {
		return pool, lpReadError(err, "token metadata")
	}
	if err = pool.Fill(meta); err != nil {
		return pool, lpReadError(err, "token metadata")
	}
	return pool, nil
}

// lpReadError classifies a chain read failure. Input validation errors are
// raised before any read, so anything here is upstream.
func lpReadError(err error, what string) error {
	return output.Wrap(output.CodeUpstream, err, "reading "+what)
}

// formatPrice renders a price with 6 significant digits, without exponent.
func formatPrice(r *big.Rat) string {
	if r.Sign() == 0 {
		return "0"
	}
	f, _ := r.Float64()
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if g := strconv.FormatFloat(f, 'g', 6, 64); !strings.ContainsAny(g, "e") {
		s = g
	} else if p, err := strconv.ParseFloat(strconv.FormatFloat(f, 'g', 6, 64), 64); err == nil {
		s = strconv.FormatFloat(p, 'f', -1, 64)
	}
	return s
}

type lpPoolView struct {
	Version      string `json:"version"`
	ChainID      uint64 `json:"chain_id"`
	Pool         string `json:"pool,omitempty"`
	PoolID       string `json:"pool_id,omitempty"`
	Token0       any    `json:"token0"`
	Token1       any    `json:"token1"`
	Fee          int    `json:"fee"`
	FeePercent   string `json:"fee_percent"`
	TickSpacing  int    `json:"tick_spacing"`
	Hooks        string `json:"hooks,omitempty"`
	Tick         int    `json:"tick"`
	SqrtPriceX96 string `json:"sqrt_price_x96"`
	Price        string `json:"price"`
	PriceInverse string `json:"price_inverse"`
	PriceUnit    string `json:"price_unit"`
	Liquidity    string `json:"liquidity"`
	FullRange    [2]int `json:"full_range_ticks"`
}

func poolView(p uniswaplp.Pool) lpPoolView {
	price := uniswaplp.HumanPrice(p.SqrtPriceX96, p.Token0.Decimals, p.Token1.Decimals)
	inverse := "0"
	if price.Sign() > 0 {
		inverse = formatPrice(new(big.Rat).Inv(price))
	}
	full := uniswaplp.FullRange(p.TickSpacing)
	return lpPoolView{
		Version: string(p.Version), ChainID: p.ChainID, Pool: p.Address, PoolID: p.PoolID, Token0: p.Token0, Token1: p.Token1,
		Fee: p.Fee, FeePercent: strconv.FormatFloat(float64(p.Fee)/10000, 'f', -1, 64) + "%", TickSpacing: p.TickSpacing, Hooks: p.Hooks,
		Tick: p.Tick, SqrtPriceX96: p.SqrtPriceX96.String(), Price: formatPrice(price), PriceInverse: inverse,
		PriceUnit: tokenLabel(p.Token1) + " per " + tokenLabel(p.Token0), Liquidity: p.Liquidity.String(), FullRange: [2]int{full.Lower, full.Upper},
	}
}

func tokenLabel(t uniswaplp.Token) string {
	if t.Symbol != "" {
		return t.Symbol
	}
	return t.Address
}

func (a *App) cmdLPPool(e *env) (*result, error) {
	t, err := a.lpTarget(e, str(e, "version"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	pool, err := a.lpLoadPool(ctx, e, t)
	if err != nil {
		return nil, err
	}
	v := poolView(pool)
	return &result{data: v, human: func(w io.Writer) {
		id := v.Pool
		if id == "" {
			id = v.PoolID
		}
		fmt.Fprintf(w, "Uniswap %s %s/%s %s on chain %d (%s)\n", v.Version, tokenLabel(pool.Token0), tokenLabel(pool.Token1), v.FeePercent, v.ChainID, id)
		fmt.Fprintf(w, "Price: %s %s (inverse %s)\nTick: %d  Tick spacing: %d  Liquidity: %s\n", v.Price, v.PriceUnit, v.PriceInverse, v.Tick, v.TickSpacing, v.Liquidity)
		if v.Hooks != "" {
			fmt.Fprintf(w, "Hooks: %s\n", v.Hooks)
		}
	}}, nil
}

type lpPositionView struct {
	TokenID      string `json:"token_id"`
	Version      string `json:"version"`
	ChainID      uint64 `json:"chain_id"`
	Owner        string `json:"owner"`
	Pool         any    `json:"pool"`
	TickLower    int    `json:"tick_lower"`
	TickUpper    int    `json:"tick_upper"`
	PriceLower   string `json:"price_lower"`
	PriceUpper   string `json:"price_upper"`
	PriceCurrent string `json:"price_current"`
	PriceUnit    string `json:"price_unit"`
	InRange      bool   `json:"in_range"`
	Closed       bool   `json:"closed"`
	Liquidity    string `json:"liquidity"`
	Amount0      string `json:"amount0"`
	Amount1      string `json:"amount1"`
	Fees0        string `json:"uncollected_fees0"`
	Fees1        string `json:"uncollected_fees1"`
}

func positionView(p uniswaplp.Position) lpPositionView {
	t0, t1 := p.Pool.Token0, p.Pool.Token1
	price := func(tick int) string {
		s, err := uniswaplp.SqrtRatioAtTick(tick)
		if err != nil {
			return "N/A"
		}
		return formatPrice(uniswaplp.HumanPrice(s, t0.Decimals, t1.Decimals))
	}
	return lpPositionView{
		TokenID: p.TokenID, Version: string(p.Pool.Version), ChainID: p.Pool.ChainID, Owner: p.Owner, Pool: poolView(p.Pool),
		TickLower: p.Range.Lower, TickUpper: p.Range.Upper, PriceLower: price(p.Range.Lower), PriceUpper: price(p.Range.Upper),
		PriceCurrent: formatPrice(uniswaplp.HumanPrice(p.Pool.SqrtPriceX96, t0.Decimals, t1.Decimals)), PriceUnit: tokenLabel(t1) + " per " + tokenLabel(t0),
		InRange: p.InRange() && p.Liquidity.Sign() > 0, Closed: p.Liquidity.Sign() == 0, Liquidity: p.Liquidity.String(),
		Amount0: formatUnits(p.Principal.Amount0, t0.Decimals), Amount1: formatUnits(p.Principal.Amount1, t1.Decimals),
		Fees0: formatUnits(p.Fees.Amount0, t0.Decimals), Fees1: formatUnits(p.Fees.Amount1, t1.Decimals),
	}
}

// lpLoadPositions reads positions and fills token metadata in two rounds.
// missing lists v4 IDs that have no NFT.
func lpLoadPositions(ctx context.Context, t lpTarget, ids []*big.Int) ([]uniswaplp.Position, []*big.Int, error) {
	ps, missing, err := uniswaplp.LoadPositions(ctx, t.rpc, t.d, ids)
	if err != nil {
		return nil, nil, lpReadError(err, "positions")
	}
	addrs := make([]string, 0, len(ps)*2)
	for _, p := range ps {
		addrs = append(addrs, p.Pool.Token0.Address, p.Pool.Token1.Address)
	}
	meta, err := uniswaplp.TokenMeta(ctx, t.rpc, t.chain, t.d.NativeSymbol, addrs)
	if err != nil {
		return nil, nil, lpReadError(err, "token metadata")
	}
	for i := range ps {
		if err := ps[i].Pool.Fill(meta); err != nil {
			return nil, nil, lpReadError(err, "token metadata")
		}
	}
	return ps, missing, nil
}

// lpLoadPosition reads one position, reporting a nonexistent NFT as not found.
func lpLoadPosition(ctx context.Context, t lpTarget, id *big.Int) (uniswaplp.Position, error) {
	ps, missing, err := lpLoadPositions(ctx, t, []*big.Int{id})
	if err != nil {
		return uniswaplp.Position{}, err
	}
	if len(missing) > 0 || len(ps) == 0 {
		return uniswaplp.Position{}, output.Errorf(output.CodeNotFound, "position %s does not exist", id)
	}
	return ps[0], nil
}

func parseTokenID(s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok || v.Sign() < 0 || v.BitLen() > 256 {
		return nil, output.Errorf(output.CodeInvalidArgument, "invalid token ID %q", s)
	}
	return v, nil
}

func (a *App) cmdLPPosition(e *env) (*result, error) {
	t, err := a.lpTarget(e, str(e, "version"))
	if err != nil {
		return nil, err
	}
	id, err := parseTokenID(str(e, "token-id"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	p, err := lpLoadPosition(ctx, t, id)
	if err != nil {
		return nil, err
	}
	v := positionView(p)
	return &result{data: v, human: func(w io.Writer) { printPositions(w, []lpPositionView{v}) }}, nil
}

func printPositions(w io.Writer, vs []lpPositionView) {
	if len(vs) == 0 {
		fmt.Fprintln(w, "no positions")
		return
	}
	tw := newTable(w)
	fmt.Fprintln(tw, "ID\tVER\tPAIR\tFEE\tRANGE\tPRICE\tSTATUS\tAMOUNTS\tFEES")
	for _, v := range vs {
		pool := v.Pool.(lpPoolView)
		pair := tokenLabel(pool.Token0.(uniswaplp.Token)) + "/" + tokenLabel(pool.Token1.(uniswaplp.Token))
		status := "out of range"
		switch {
		case v.Closed:
			status = "closed"
		case v.InRange:
			status = "in range"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s-%s\t%s\t%s\t%s / %s\t%s / %s\n", v.TokenID, v.Version, pair, pool.FeePercent, v.PriceLower, v.PriceUpper, v.PriceCurrent, status, v.Amount0, v.Amount1, v.Fees0, v.Fees1)
	}
	tw.Flush()
	fmt.Fprintf(w, "Prices are %s.\n", vs[0].PriceUnit)
}

// v4PortfolioIDs reads the agent's Uniswap v4 position token IDs from Oku's
// portfolio data, the same source the web app's portfolio uses. v4 position
// NFTs are not enumerable on chain.
func (a *App) v4PortfolioIDs(ctx context.Context, e *env, chain uint64, owner string) ([]*big.Int, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	var res v2.GetHoldingsAndPositionsResponse
	if err := e.client(p).Portfolio(ctx, "GetHoldingsAndPositions", &v2.GetHoldingsAndPositionsRequest{WalletAddress: owner, Chains: []string{"eip155:" + strconv.FormatUint(chain, 10)}}, &res, true); err != nil {
		return nil, apiError(err, "GetHoldingsAndPositions")
	}
	var ids []*big.Int
	seen := map[string]bool{}
	for _, c := range res.GetChains() {
		for _, pos := range c.GetPositions() {
			if !strings.Contains(strings.ToLower(pos.GetProtocol()), "uniswap v4") || pos.GetRewardId() == "" || seen[pos.GetRewardId()] {
				continue
			}
			id, err := parseTokenID(pos.GetRewardId())
			if err != nil {
				return nil, err
			}
			seen[pos.GetRewardId()] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (a *App) cmdLPList(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	versions := []string{"v3", "v4"}
	if v := str(e, "version"); v != "" {
		versions = []string{v}
	}
	var explicit []*big.Int
	if raw := str(e, "token-id"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			id, err := parseTokenID(s)
			if err != nil {
				return nil, err
			}
			explicit = append(explicit, id)
		}
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	owner := common.HexToAddress(w.agent)
	out := []lpPositionView{}
	warnings := []string{}
	supported := 0
	for _, version := range versions {
		t, err := a.lpTarget(e, version)
		if err != nil {
			var oe *output.Error
			if len(versions) > 1 && errors.As(err, &oe) && oe.Code == output.CodeInvalidArgument && errors.Is(err, uniswaplp.ErrUnsupported) {
				continue
			}
			return nil, err
		}
		supported++
		var ids []*big.Int
		if t.d.Version == uniswaplp.V3 {
			var total int
			ids, total, err = uniswaplp.V3OwnedTokenIDs(ctx, t.rpc, t.d, owner, lpListLimit)
			if err != nil {
				return nil, lpReadError(err, "v3 positions")
			}
			if total > len(ids) {
				warnings = append(warnings, fmt.Sprintf("showing the first %d of %d v3 positions", len(ids), total))
			}
		} else {
			ids = explicit
			found, perr := a.v4PortfolioIDs(ctx, e, t.chain, w.agent)
			if perr != nil {
				warnings = append(warnings, "v4 positions not discovered from Oku portfolio data ("+perr.Error()+"); pass --token-id to include them")
			}
			for _, id := range found {
				dup := false
				for _, x := range ids {
					dup = dup || x.Cmp(id) == 0
				}
				if !dup {
					ids = append(ids, id)
				}
			}
		}
		ps, missing, err := lpLoadPositions(ctx, t, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range missing {
			warnings = append(warnings, "skipped v4 token "+id.String()+": no such position (burned or never minted)")
		}
		for _, p := range ps {
			if !strings.EqualFold(p.Owner, w.agent) {
				warnings = append(warnings, "skipped token "+p.TokenID+": not owned by the agent")
				continue
			}
			out = append(out, positionView(p))
		}
	}
	if supported == 0 {
		return nil, output.Errorf(output.CodeInvalidArgument, "no Uniswap liquidity deployment on chain %s", str(e, "chain"))
	}
	return &result{data: map[string]any{"agent": w.agent, "positions": out, "warnings": warnings}, human: func(wr io.Writer) {
		printPositions(wr, out)
		for _, m := range warnings {
			fmt.Fprintln(wr, "warning: "+m)
		}
	}}, nil
}
