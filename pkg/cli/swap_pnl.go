package cli

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/pnl"
	urfave "github.com/urfave/cli/v3"
)

func swapPnLFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID (default: every chain with agent swaps in the journal)"},
		&urfave.StringFlag{Name: "token", Usage: "only this ERC-20 token address"},
	}
}

// swapBook replays the agent's journaled swaps on chain with the average-cost
// method. It is the single source of cost basis for swap pnl and swap orders.
func swapBook(j *journal.Journal, agent string, chain uint64) (*pnl.Book, pnl.Skipped, error) {
	records, err := j.ListOnchainSwaps(agent, chain)
	if err != nil {
		return nil, pnl.Skipped{}, err
	}
	swaps, skipped := pnl.FromRecords(records, agent, nativeTokenAlias(chain))
	return pnl.Compute(swaps), skipped, nil
}

func weiPerNative() *big.Rat {
	return new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

// perRaw converts a native-per-token price to wei per raw token unit.
func perRaw(price *big.Rat, decimals int) *big.Rat {
	if price == nil {
		return nil
	}
	return pnl.Scale(new(big.Rat).Mul(price, weiPerNative()), decimals)
}

// recordSwapFill measures a confirmed transaction swap's fill from its
// receipts and stores it once. It is best effort: failures only warn, and
// PnL then falls back to the journaled minimum output. It returns the record
// with the fill, or r unchanged.
func (a *App) recordSwapFill(e *env, j *journal.Journal, r *journal.OnchainRecord) *journal.OnchainRecord {
	if r == nil || r.Kind != "swap" || r.Intent || r.State != "confirmed" || r.Fill != nil || len(r.Hashes) == 0 {
		return r
	}
	if err := a.measureSwapFill(e, j, r); err != nil {
		e.diag("warning: could not record the swap fill of %s (%v); swap pnl uses its minimum output", r.ID, err)
		return r
	}
	if updated, err := j.GetOnchain(r.ID); err == nil {
		return updated
	}
	return r
}

func (a *App) measureSwapFill(e *env, j *journal.Journal, r *journal.OnchainRecord) error {
	router := r.Target
	if !onchain.ValidAddress(router) {
		router = routerFor(r.ChainID)
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	receipts := make([]*onchain.TxReceipt, 0, len(r.Hashes))
	for _, h := range r.Hashes {
		t, err := rpc.FullReceipt(ctx, r.ChainID, h)
		if err != nil {
			return err
		}
		if t == nil {
			return errors.New("receipt not found")
		}
		receipts = append(receipts, t)
	}
	f, err := onchain.MeasureSwapFill(receipts, router, r.OutToken, r.Recipient, isNativeToken(r.OutToken))
	if err != nil {
		return err
	}
	fill := journal.Fill{Received: f.Received.String(), Gas: f.Gas.String(), Source: f.Source}
	if !isNativeToken(r.Token) && !isNativeToken(r.OutToken) {
		if v := a.inputValue(e, r); v != nil {
			fill.InValue = v.RatString()
		}
	}
	return j.RecordFill(r.ID, fill)
}

// inputValue is the oracle native wei value of a token swap's input now, nil
// when it cannot be priced.
func (a *App) inputValue(e *env, r *journal.OnchainRecord) *big.Rat {
	amount, ok := new(big.Int).SetString(r.Amount, 10)
	if !ok {
		return nil
	}
	ctx, cancel := e.requestCtx()
	dec, err := onchain.HTTPRPC{Getenv: a.Getenv}.Call(ctx, r.ChainID, r.Token, "0x313ce567")
	cancel()
	if err != nil || !dec.IsInt64() || dec.Int64() < 0 || dec.Int64() > 36 {
		return nil
	}
	prices, err := a.tokenPrices(e, r.ChainID, []string{r.Token})
	if err != nil {
		return nil
	}
	price := perRaw(prices[strings.ToLower(r.Token)], int(dec.Int64()))
	if price == nil {
		return nil
	}
	return price.Mul(price, new(big.Rat).SetInt(amount))
}

type pnlRow struct {
	Token            string `json:"token"`
	Symbol           string `json:"symbol,omitempty"`
	Decimals         *int   `json:"decimals"`
	Held             string `json:"held"`
	WalletBalance    string `json:"wallet_balance,omitempty"`
	AvgCost          string `json:"avg_cost_native,omitempty"`
	CostBasis        string `json:"cost_basis_native"`
	Price            string `json:"price_native,omitempty"`
	ValueNative      string `json:"value_native,omitempty"`
	ValueUSD         string `json:"value_usd,omitempty"`
	UnrealizedNative string `json:"unrealized_native,omitempty"`
	UnrealizedUSD    string `json:"unrealized_usd,omitempty"`
	RealizedNative   string `json:"realized_native"`
	RealizedUSD      string `json:"realized_usd,omitempty"`
	FeesNative       string `json:"fees_native"`
	FeesUSD          string `json:"fees_usd,omitempty"`
	FeesUnknown      int    `json:"fees_unknown_swaps,omitempty"`
	UntrackedSold    string `json:"untracked_sold,omitempty"`
	UntrackedBought  string `json:"untracked_bought,omitempty"`
	Swaps            int    `json:"swaps"`
	Approximate      bool   `json:"approximate"`
	PriceKnown       bool   `json:"price_known"`
}

type pnlTotal struct {
	CostBasis        string `json:"cost_basis_native"`
	ValueNative      string `json:"value_native,omitempty"`
	ValueUSD         string `json:"value_usd,omitempty"`
	UnrealizedNative string `json:"unrealized_native,omitempty"`
	UnrealizedUSD    string `json:"unrealized_usd,omitempty"`
	RealizedNative   string `json:"realized_native"`
	RealizedUSD      string `json:"realized_usd,omitempty"`
	FeesNative       string `json:"fees_native"`
	FeesUSD          string `json:"fees_usd,omitempty"`
	Approximate      bool   `json:"approximate"`
}

type pnlChain struct {
	ChainID        uint64      `json:"chain_id"`
	NativeSymbol   string      `json:"native_symbol"`
	NativePriceUSD string      `json:"native_price_usd,omitempty"`
	Tokens         []pnlRow    `json:"tokens"`
	Total          pnlTotal    `json:"total"`
	Skipped        pnl.Skipped `json:"skipped"`
	Warnings       []string    `json:"warnings,omitempty"`
}

const pnlMethod = "average cost over the agent's own confirmed (or intent-submitted) swaps in the local journal; realized PnL is net of the swap gas in fees; unrealized uses Oku oracle prices and is omitted when a held token has no price"

func (a *App) cmdSwapPnL(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(str(e, "token"))
	if token != "" && (isNativeToken(token) || !onchain.ValidAddress(token)) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--token must be an ERC-20 address")
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	var chains []uint64
	if raw := strings.TrimSpace(str(e, "chain")); raw != "" {
		c, perr := strconv.ParseUint(raw, 10, 64)
		if perr != nil || !onchain.Allowed(c) {
			return nil, output.Errorf(output.CodeInvalidArgument, "--chain must be an EVM chain ID")
		}
		chains = []uint64{c}
	} else if chains, err = j.ListOnchainSwapChains(w.agent); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	out := make([]pnlChain, 0, len(chains))
	for _, c := range chains {
		pc, cerr := a.chainPnL(e, j, w.agent, c, token)
		if cerr != nil {
			return nil, cerr
		}
		out = append(out, pc)
	}
	return &result{data: map[string]any{"profile": w.profile.Name, "agent": w.agent, "method": pnlMethod, "realized_includes_gas": true, "chains": out}, human: func(wr io.Writer) { writePnL(wr, out) }}, nil
}

func (a *App) chainPnL(e *env, j *journal.Journal, agent string, chain uint64, token string) (pnlChain, error) {
	book, skipped, err := swapBook(j, agent, chain)
	if err != nil {
		return pnlChain{}, output.Wrap(output.CodeInternal, err, "journal")
	}
	pc := pnlChain{ChainID: chain, NativeSymbol: nativeSymbol(chain), Tokens: []pnlRow{}, Skipped: skipped}
	positions := []*pnl.Position{}
	addrs := []string{}
	for _, p := range book.Positions() {
		if token != "" && !strings.EqualFold(p.Token, token) {
			continue
		}
		positions = append(positions, p)
		addrs = append(addrs, p.Token)
	}
	meta := map[string]onchain.TokenMetadata{}
	if len(addrs) > 0 {
		rpc := onchain.HTTPRPC{Getenv: a.Getenv}
		ctx, cancel := e.requestCtx()
		details, merr := rpc.TokenDetails(ctx, chain, agent, addrs)
		cancel()
		for _, d := range details {
			meta[strings.ToLower(d.Address)] = d
		}
		var unknown []string
		for _, t := range addrs {
			if _, ok := meta[t]; ok {
				continue
			}
			ctx, cancel := e.requestCtx()
			dec, derr := rpc.Call(ctx, chain, t, "0x313ce567")
			cancel()
			if derr != nil || !dec.IsInt64() || dec.Int64() < 0 || dec.Int64() > 36 {
				unknown = append(unknown, t)
				continue
			}
			meta[t] = onchain.TokenMetadata{Address: t, Decimals: int(dec.Int64())}
		}
		if merr != nil {
			pc.Warnings = append(pc.Warnings, "wallet token balances unavailable ("+merr.Error()+")")
		}
		if len(unknown) > 0 {
			pc.Warnings = append(pc.Warnings, "no decimals for "+strings.Join(unknown, ", ")+"; amounts are raw units and value is unknown")
		}
	}
	native, prices, perr := a.oraclePrices(e, chain, addrs)
	if perr != nil {
		pc.Warnings = append(pc.Warnings, "prices unavailable ("+output.As(perr).Message+"); value and unrealized PnL unknown")
		prices = map[string]*big.Rat{}
		native = nil
	}
	if native != nil {
		pc.NativePriceUSD = pnl.Decimal(native, 8)
	}
	usd := func(wei *big.Rat) string {
		if wei == nil || native == nil {
			return ""
		}
		return pnl.Decimal(new(big.Rat).Mul(pnl.Scale(wei, 18), native), 2)
	}
	nat := func(wei *big.Rat) string { return pnl.Decimal(pnl.Scale(wei, 18), 18) }
	cost, value, unreal, realized, fees := new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat)
	unrealKnown := true
	for _, p := range positions {
		m, hasMeta := meta[p.Token]
		dec := 0
		row := pnlRow{Token: p.Token, Swaps: p.Swaps, Approximate: p.Approximate, FeesUnknown: p.GasUnknown}
		if hasMeta {
			dec = m.Decimals
			row.Symbol, row.Decimals = m.Symbol, &m.Decimals
			if m.Balance != nil {
				row.WalletBalance = formatUnits(m.Balance, dec)
			}
		}
		qty := func(r *big.Rat) string { return pnl.Decimal(pnl.Scale(r, dec), dec) }
		row.Held = qty(p.Held)
		if p.UntrackedSold.Sign() > 0 {
			row.UntrackedSold = qty(p.UntrackedSold)
		}
		if p.UntrackedBought.Sign() > 0 {
			row.UntrackedBought = qty(p.UntrackedBought)
		}
		if avg := p.AvgCost(); avg != nil {
			whole := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dec)), nil))
			row.AvgCost = nat(avg.Mul(avg, whole))
		}
		row.CostBasis = nat(p.Cost)
		gas := new(big.Rat).SetInt(p.Gas)
		row.FeesNative, row.FeesUSD = nat(gas), usd(gas)
		row.RealizedNative, row.RealizedUSD = nat(p.Realized), usd(p.Realized)
		var price *big.Rat
		if hasMeta {
			price = perRaw(prices[p.Token], dec)
		}
		row.PriceKnown = price != nil
		if price != nil {
			row.Price = pnl.Decimal(prices[p.Token], 18)
			v := new(big.Rat).Mul(p.Held, price)
			row.ValueNative, row.ValueUSD = nat(v), usd(v)
			value.Add(value, v)
		} else if p.Held.Sign() == 0 {
			row.ValueNative, row.ValueUSD = "0", usd(new(big.Rat))
		}
		if u := p.Unrealized(price); u != nil {
			row.UnrealizedNative, row.UnrealizedUSD = nat(u), usd(u)
			unreal.Add(unreal, u)
		} else {
			unrealKnown = false
		}
		cost.Add(cost, p.Cost)
		realized.Add(realized, p.Realized)
		fees.Add(fees, gas)
		pc.Total.Approximate = pc.Total.Approximate || p.Approximate
		pc.Tokens = append(pc.Tokens, row)
	}
	pc.Total.CostBasis = nat(cost)
	pc.Total.RealizedNative, pc.Total.RealizedUSD = nat(realized), usd(realized)
	pc.Total.FeesNative, pc.Total.FeesUSD = nat(fees), usd(fees)
	if unrealKnown {
		pc.Total.ValueNative, pc.Total.ValueUSD = nat(value), usd(value)
		pc.Total.UnrealizedNative, pc.Total.UnrealizedUSD = nat(unreal), usd(unreal)
	}
	return pc, nil
}

func writePnL(w io.Writer, chains []pnlChain) {
	if len(chains) == 0 {
		fmt.Fprintln(w, "no agent swaps in the journal")
		return
	}
	short := func(s string, places int) string {
		if s == "" {
			return "?"
		}
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return s
		}
		return pnl.Decimal(r, places)
	}
	for i, c := range chains {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "chain %d (values in %s", c.ChainID, c.NativeSymbol)
		if c.NativePriceUSD != "" {
			fmt.Fprintf(w, ", %s = $%s", c.NativeSymbol, short(c.NativePriceUSD, 2))
		}
		fmt.Fprintln(w, ")")
		tw := newTable(w)
		fmt.Fprintln(tw, "TOKEN\tHELD\tAVG COST\tCOST BASIS\tVALUE\tVALUE USD\tUNREALIZED\tREALIZED\tFEES")
		for _, r := range c.Tokens {
			name := r.Symbol
			if name == "" {
				name = r.Token
			}
			if r.Approximate {
				name += " ~"
			}
			avg := r.AvgCost
			if avg == "" {
				avg = "-"
			} else {
				avg = short(avg, 8)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, short(r.Held, 6), avg, short(r.CostBasis, 6), short(r.ValueNative, 6), short(r.ValueUSD, 2), short(r.UnrealizedNative, 6), short(r.RealizedNative, 6), short(r.FeesNative, 6))
		}
		t := c.Total
		name := "TOTAL"
		if t.Approximate {
			name += " ~"
		}
		fmt.Fprintf(tw, "%s\t\t\t%s\t%s\t%s\t%s\t%s\t%s\n", name, short(t.CostBasis, 6), short(t.ValueNative, 6), short(t.ValueUSD, 2), short(t.UnrealizedNative, 6), short(t.RealizedNative, 6), short(t.FeesNative, 6))
		tw.Flush()
		for _, warn := range c.Warnings {
			fmt.Fprintf(w, "warning: %s\n", warn)
		}
	}
	fmt.Fprintln(w, "Realized PnL is net of swap gas (FEES). ? = no price. ~ = approximate: uses a swap's minimum output (no recorded fill) or a token swap without an input price.")
}
