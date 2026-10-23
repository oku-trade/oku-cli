package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	urfave "github.com/urfave/cli/v3"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// meta accompanies every read result so agents can judge freshness.
type meta struct {
	Profile   string `json:"profile,omitempty"`
	Network   string `json:"network"`
	APIURL    string `json:"api_url"`
	Account   string `json:"account,omitempty"`
	FetchedAt string `json:"fetched_at"`
	Source    string `json:"source"`
}

func (e *env) meta(p *resolved, account, method string) meta {
	return meta{
		Profile:   p.Name,
		Network:   p.Network,
		APIURL:    p.APIURL,
		Account:   account,
		FetchedAt: e.app.Now().UTC().Format(time.RFC3339Nano),
		Source:    "oku:PerpsService/" + method,
	}
}

// perpsRead performs an unauthenticated or device-authenticated read. Reads
// use the device token only when the target account is the enrolled one.
func (e *env) perpsRead(p *resolved, method string, req, resp any) error {
	ctx, cancel := e.requestCtx()
	defer cancel()
	if err := e.client(p).Perps(ctx, method, req, resp, false); err != nil {
		return apiError(err, method)
	}
	return nil
}

func normCoin(c string) (string, error) {
	c = strings.TrimSpace(c)
	if c == "" || len(c) > 32 || strings.ContainsAny(c, " \t\n\"\\") {
		return "", output.Errorf(output.CodeInvalidArgument, "invalid market %q", c)
	}
	return c, nil
}

func marketFilterFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "market", Usage: "filter by market, e.g. BTC"}}
}

func (a *App) cmdMarketsList(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	req := map[string]string{}
	if m := e.cmd.String("market"); m != "" {
		if req["coin"], err = normCoin(m); err != nil {
			return nil, err
		}
	}
	var resp api.GetMarketsResponse
	if req["coin"] != "" {
		m, err := e.market(p, req["coin"])
		if err != nil {
			return nil, err
		}
		resp.Markets = []api.Market{*m}
	} else if err := e.perpsRead(p, "GetMarkets", req, &resp); err != nil {
		return nil, err
	}
	if resp.Markets == nil {
		resp.Markets = []api.Market{}
	}
	data := map[string]any{"meta": e.meta(p, "", "GetMarkets"), "markets": resp.Markets}
	return &result{data: data, human: func(w io.Writer) {
		tw := newTable(w)
		fmt.Fprintln(tw, "MARKET\tMARK\tMID\tFUNDING\tMAX LEV\tSZ DEC\tASSET")
		for _, m := range resp.Markets {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\n", m.Coin, m.MarkPx, m.MidPx, m.Funding, m.MaxLeverage, m.SzDecimals, m.AssetID)
		}
		tw.Flush()
	}}, nil
}

func (a *App) cmdMarketsQuote(e *env) (*result, error) {
	args, err := e.positional(1, "markets quote COIN")
	if err != nil {
		return nil, err
	}
	coin, err := normCoin(args[0])
	if err != nil {
		return nil, err
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if m, merr := e.market(p, coin); merr == nil {
		coin = m.Coin
	}
	var resp api.GetMarketPriceResponse
	if err := e.perpsRead(p, "GetMarketPrice", map[string]string{"coin": coin}, &resp); err != nil {
		return nil, err
	}
	if resp.MarkPx == "" {
		return nil, output.Errorf(output.CodeUpstream, "no price returned for %s", coin).WithRetry()
	}
	data := map[string]any{"meta": e.meta(p, "", "GetMarketPrice"), "quote": resp}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "%s  mark %s  mid %s  oracle %s\n", resp.Coin, resp.MarkPx, orDash(resp.MidPx), resp.OraclePx)
	}}, nil
}

func fundingFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.DurationFlag{Name: "since", Value: 24 * time.Hour, Usage: "history window (max 720h)"}}
}

func (a *App) cmdMarketsFunding(e *env) (*result, error) {
	args, err := e.positional(1, "markets funding COIN [--since 24h]")
	if err != nil {
		return nil, err
	}
	coin, err := normCoin(args[0])
	if err != nil {
		return nil, err
	}
	since := e.cmd.Duration("since")
	if since <= 0 || since > 720*time.Hour {
		return nil, output.Errorf(output.CodeInvalidArgument, "--since must be between 1s and 720h")
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if m, merr := e.market(p, coin); merr == nil {
		coin = m.Coin
	}
	now := e.app.Now()
	var hist api.GetMarketFundingHistoryResponse
	req := map[string]any{"coin": coin, "startTime": api.Int64(now.Add(-since).UnixMilli())}
	if err := e.perpsRead(p, "GetMarketFundingHistory", req, &hist); err != nil {
		return nil, err
	}
	var mk api.GetMarketsResponse
	if err := e.perpsRead(p, "GetMarkets", map[string]string{"coin": coin}, &mk); err != nil {
		return nil, err
	}
	current := ""
	for _, m := range mk.Markets {
		if m.Coin == coin {
			current = m.Funding
		}
	}
	if hist.Rates == nil {
		hist.Rates = []api.FundingRate{}
	}
	data := map[string]any{
		"meta":            e.meta(p, "", "GetMarketFundingHistory"),
		"market":          coin,
		"current_funding": current,
		"interval":        "1h",
		"rates":           hist.Rates,
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "%s current hourly funding: %s\n", coin, orDash(current))
		tw := newTable(w)
		fmt.Fprintln(tw, "TIME\tRATE\tPREMIUM")
		for _, r := range hist.Rates {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", time.UnixMilli(int64(r.Time)).UTC().Format(time.RFC3339), r.FundingRate, r.Premium)
		}
		tw.Flush()
	}}, nil
}

func (a *App) cmdAccountSummary(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	acct, err := e.readAccount(p)
	if err != nil {
		return nil, err
	}
	var bal api.GetAccountBalancesResponse
	if err := e.perpsRead(p, "GetAccountBalances", map[string]string{"address": acct}, &bal); err != nil {
		return nil, err
	}
	var pos api.GetPerpPositionsResponse
	if err := e.perpsRead(p, "GetPerpPositions", map[string]string{"address": acct}, &pos); err != nil {
		return nil, err
	}
	var fee api.GetBuilderFeeStatusResponse
	if err := e.perpsRead(p, "GetBuilderFeeStatus", map[string]string{"address": acct}, &fee); err != nil {
		return nil, err
	}
	if pos.MarginSummary == nil || bal.AccountAbstraction == "" {
		return nil, output.Errorf(output.CodeUpstream, "incomplete account data from Oku; not reporting partial balances").WithRetry()
	}
	data := map[string]any{
		"meta":                 e.meta(p, acct, "GetAccountBalances+GetPerpPositions+GetBuilderFeeStatus"),
		"account_mode":         bal.AccountAbstraction,
		"margin_summary":       pos.MarginSummary,
		"cross_margin_summary": pos.CrossMarginSummary,
		"withdrawable":         pos.Withdrawable,
		"open_positions":       len(pos.Positions),
		"balances":             bal,
		"builder_fee":          fee,
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "account       %s (%s)\n", acct, p.Network)
		fmt.Fprintf(w, "mode          %s\n", bal.AccountAbstraction)
		fmt.Fprintf(w, "account value %s\n", pos.MarginSummary.AccountValue)
		fmt.Fprintf(w, "margin used   %s\n", pos.MarginSummary.TotalMarginUsed)
		fmt.Fprintf(w, "withdrawable  %s\n", pos.Withdrawable)
		fmt.Fprintf(w, "spot USDC     %s\n", orDash(bal.SpotUsdcBalance))
		fmt.Fprintf(w, "positions     %d\n", len(pos.Positions))
		fmt.Fprintf(w, "builder fee   approved=%v (charged %d ppm)\n", fee.BuilderFeeApproved, fee.BuilderFeeChargedPpm)
	}}, nil
}

func (a *App) cmdPositionsList(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	acct, err := e.readAccount(p)
	if err != nil {
		return nil, err
	}
	var pos api.GetPerpPositionsResponse
	if err := e.perpsRead(p, "GetPerpPositions", map[string]string{"address": acct}, &pos); err != nil {
		return nil, err
	}
	if pos.MarginSummary == nil {
		return nil, output.Errorf(output.CodeUpstream, "incomplete position data from Oku").WithRetry()
	}
	if pos.Positions == nil {
		pos.Positions = []api.Position{}
	}
	data := map[string]any{"meta": e.meta(p, acct, "GetPerpPositions"), "positions": pos.Positions}
	return &result{data: data, human: func(w io.Writer) {
		if len(pos.Positions) == 0 {
			fmt.Fprintln(w, "no open positions")
			return
		}
		tw := newTable(w)
		fmt.Fprintln(tw, "MARKET\tSIZE\tENTRY\tVALUE\tUPNL\tLIQ\tLEVERAGE")
		for _, x := range pos.Positions {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s %d\n", x.Coin, x.Szi, x.EntryPx, x.PositionValue, x.UnrealizedPnl, orDash(x.LiquidationPx), x.LeverageType, x.LeverageValue)
		}
		tw.Flush()
	}}, nil
}

func fresh() []urfave.Flag {
	return []urfave.Flag{&urfave.BoolFlag{Name: "fresh", Usage: "bypass the Oku server cache"}}
}

func (a *App) cmdOrdersList(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	acct, err := e.readAccount(p)
	if err != nil {
		return nil, err
	}
	req := map[string]any{"address": acct, "skipCache": e.cmd.Bool("fresh")}
	var resp api.GetOpenOrdersResponse
	if err := e.perpsRead(p, "GetOpenOrders", req, &resp); err != nil {
		return nil, err
	}
	if resp.OpenOrders == nil {
		resp.OpenOrders = []api.Order{}
	}
	data := map[string]any{"meta": e.meta(p, acct, "GetOpenOrders"), "orders": resp.OpenOrders}
	return &result{data: data, human: func(w io.Writer) {
		if len(resp.OpenOrders) == 0 {
			fmt.Fprintln(w, "no open orders")
			return
		}
		tw := newTable(w)
		fmt.Fprintln(tw, "OID\tMARKET\tSIDE\tSIZE\tPRICE\tTYPE\tREDUCE\tCLOID")
		for _, o := range resp.OpenOrders {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%v\t%s\n", o.Oid, o.Coin, sideName(o.Side), o.Sz, o.LimitPx, o.OrderType, o.ReduceOnly, orDash(o.Cloid))
		}
		tw.Flush()
	}}, nil
}

func (a *App) cmdFillsList(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	acct, err := e.readAccount(p)
	if err != nil {
		return nil, err
	}
	req := map[string]string{"address": acct}
	if m := e.cmd.String("market"); m != "" {
		if req["coin"], err = normCoin(m); err != nil {
			return nil, err
		}
	}
	var resp api.GetUserFillsResponse
	if err := e.perpsRead(p, "GetUserFills", req, &resp); err != nil {
		return nil, err
	}
	if resp.Fills == nil {
		resp.Fills = []api.Fill{}
	}
	data := map[string]any{"meta": e.meta(p, acct, "GetUserFills"), "fills": resp.Fills}
	return &result{data: data, human: func(w io.Writer) {
		if len(resp.Fills) == 0 {
			fmt.Fprintln(w, "no fills")
			return
		}
		tw := newTable(w)
		fmt.Fprintln(tw, "TIME\tMARKET\tDIR\tSIZE\tPRICE\tFEE\tPNL\tOID")
		for _, f := range resp.Fills {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s %s\t%s\t%d\n", time.UnixMilli(int64(f.Time)).UTC().Format(time.RFC3339), f.Coin, f.Dir, f.Sz, f.Px, f.Fee, f.FeeToken, f.ClosedPnl, f.Oid)
		}
		tw.Flush()
	}}, nil
}

func sideName(s string) string {
	switch s {
	case "B":
		return "buy"
	case "A":
		return "sell"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
