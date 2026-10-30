package cli

import (
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	urfave "github.com/urfave/cli/v3"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/pnl"
)

func pnlFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "range", Value: "all", Usage: "window: 1d, 7d, 30d or all (launchpad and agent swaps are always all-time)"},
		&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID for agent swaps and launchpad only (net worth and perps are unaffected)"},
	}
}

// pnlRange maps --range to the net worth chart period and the perps summary
// window it reads.
type pnlRange struct {
	period v2.NetWorthChartPeriod
	perps  func(*v2.GetPerpsPnlSummaryResponse) string
}

var pnlRanges = map[string]pnlRange{
	"1d":  {v2.NetWorthChartPeriod_NET_WORTH_CHART_PERIOD_DAY, (*v2.GetPerpsPnlSummaryResponse).GetDayPnl},
	"7d":  {v2.NetWorthChartPeriod_NET_WORTH_CHART_PERIOD_WEEK, (*v2.GetPerpsPnlSummaryResponse).GetWeekPnl},
	"30d": {v2.NetWorthChartPeriod_NET_WORTH_CHART_PERIOD_MONTH, (*v2.GetPerpsPnlSummaryResponse).GetMonthPnl},
	"all": {v2.NetWorthChartPeriod_NET_WORTH_CHART_PERIOD_MAX, (*v2.GetPerpsPnlSummaryResponse).GetAllTimePnl},
}

const (
	pnlStatusOK          = "ok"
	pnlStatusUnavailable = "unavailable"
	netWorthLabel        = "net worth change (includes deposits/withdrawals)"
	launchpadUnavailable = "backend endpoint not available yet"
)

type pnlSwapChain struct {
	ChainID       uint64   `json:"chain_id"`
	RealizedUSD   string   `json:"realized_usd,omitempty"`
	UnrealizedUSD string   `json:"unrealized_usd,omitempty"`
	PnLUSD        string   `json:"pnl_usd,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

// pnlSource is one PnL source with its own status: a failing source reports
// its error and is left out of the total instead of failing the command.
type pnlSource struct {
	Source  string `json:"source"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	Range   string `json:"range"`
	InTotal bool   `json:"in_total"`
	Address string `json:"address,omitempty"`
	PnLUSD  string `json:"pnl_usd,omitempty"`

	ChangeUSD     string `json:"change_usd,omitempty"`
	StartValueUSD string `json:"start_value_usd,omitempty"`
	EndValueUSD   string `json:"end_value_usd,omitempty"`
	ValueUSD      string `json:"value_usd,omitempty"`
	Points        *int   `json:"points,omitempty"`

	AsOfMs int64 `json:"as_of_ms,omitempty"`

	RealizedUSD   string         `json:"realized_usd,omitempty"`
	UnrealizedUSD string         `json:"unrealized_usd,omitempty"`
	Chains        []pnlSwapChain `json:"chains,omitempty"`
}

type pnlOmitted struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

type pnlTotalSummary struct {
	Status   string       `json:"status"`
	PnLUSD   string       `json:"pnl_usd,omitempty"`
	Included []string     `json:"included"`
	Omitted  []pnlOmitted `json:"omitted"`
	Note     string       `json:"note"`
}

const pnlTotalNote = "total = perps + launchpad + agent swaps over the sources available; net worth change is shown but never added"

func (s *pnlSource) fail(reason string) {
	s.Status, s.Error, s.InTotal = pnlStatusUnavailable, reason, false
}

func errReason(err error) string { return output.As(err).Message }

// exactDecimal parses a decimal USD string exactly.
func exactDecimal(s string) (*big.Rat, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "eE/") {
		return nil, 0, false
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, 0, false
	}
	_, frac, _ := strings.Cut(s, ".")
	return r, len(frac), true
}

// floatDecimal converts an API double to the shortest decimal that
// round-trips it, so no further float arithmetic touches the amount.
func floatDecimal(f float64) (*big.Rat, int, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, 0, false
	}
	return exactDecimal(strconv.FormatFloat(f, 'f', -1, 64))
}

// usdSum adds exact decimals and renders the result with no rounding.
type usdSum struct {
	r      *big.Rat
	places int
}

func newUSDSum() *usdSum { return &usdSum{r: new(big.Rat)} }

func (u *usdSum) add(r *big.Rat, places int) {
	u.r.Add(u.r, r)
	u.places = max(u.places, places)
}

func (u *usdSum) sub(r *big.Rat, places int) {
	u.r.Sub(u.r, r)
	u.places = max(u.places, places)
}

func (u *usdSum) String() string { return pnl.Decimal(u.r, u.places) }

func (a *App) cmdPnL(e *env) (*result, error) {
	rangeName := strings.TrimSpace(str(e, "range"))
	if rangeName == "" {
		rangeName = "all"
	}
	rg, ok := pnlRanges[rangeName]
	if !ok {
		return nil, output.Errorf(output.CodeInvalidArgument, "--range must be one of 1d, 7d, 30d, all")
	}
	var chain uint64
	if raw := strings.TrimSpace(str(e, "chain")); raw != "" {
		c, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || !onchain.Allowed(c) {
			return nil, output.Errorf(output.CodeInvalidArgument, "--chain must be an EVM chain ID")
		}
		chain = c
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	w, walletErr := agentWalletMetadata(e)
	agent := ""
	if walletErr == nil {
		agent = w.agent
	}
	sources, total := a.pnlReport(e, p, w, walletErr, rangeName, rg, chain)
	data := map[string]any{
		"profile":     p.Name,
		"range":       rangeName,
		"sources":     sources,
		"total":       total,
		"fetched_at":  e.app.Now().UTC().Format(time.RFC3339Nano),
		"read_only":   true,
		"agent":       agent,
		"chain_scope": "--chain filters agent swaps and launchpad only; net worth and perps are unaffected",
	}
	if chain != 0 {
		data["chain"] = chain
	}
	return &result{data: data, human: func(wr io.Writer) { writePnLSummary(wr, rangeName, chain, sources, total) }}, nil
}

// pnlReport reads every source. walletErr is why the agent wallet is
// unknown; it makes only the sources that need it unavailable.
func (a *App) pnlReport(e *env, p *resolved, w *resolvedWallet, walletErr error, rangeName string, rg pnlRange, chain uint64) ([]pnlSource, pnlTotalSummary) {
	agent := ""
	if walletErr == nil {
		agent = w.agent
	}
	lifetime := "all"
	suffix := ""
	if rangeName != "all" {
		suffix = " (all-time)"
	}
	netWorth := pnlSource{Source: "net_worth", Label: netWorthLabel, Status: pnlStatusOK, Range: rangeName, Address: agent}
	perps := pnlSource{Source: "perps", Label: "perps", Status: pnlStatusOK, Range: rangeName, InTotal: true}
	launchpad := pnlSource{Source: "launchpad", Label: "launchpad" + suffix, Status: pnlStatusOK, Range: lifetime, InTotal: true, Address: agent}
	swaps := pnlSource{Source: "agent_swaps", Label: "agent swaps" + suffix, Status: pnlStatusOK, Range: lifetime, InTotal: true, Address: agent}

	if walletErr != nil {
		netWorth.fail(errReason(walletErr))
	} else {
		a.pnlNetWorth(e, p, rg, &netWorth)
	}
	a.pnlPerps(e, p, rg, &perps)
	launchpad.fail(launchpadUnavailable)
	if walletErr != nil {
		swaps.fail(errReason(walletErr))
	} else {
		a.pnlSwaps(e, w, chain, &swaps)
	}

	sources := []pnlSource{netWorth, perps, launchpad, swaps}
	total := pnlTotalSummary{Status: pnlStatusUnavailable, Included: []string{}, Omitted: []pnlOmitted{}, Note: pnlTotalNote}
	sum := newUSDSum()
	for _, s := range sources[1:] {
		if s.Status != pnlStatusOK {
			total.Omitted = append(total.Omitted, pnlOmitted{Source: s.Source, Reason: s.Error})
			continue
		}
		r, places, _ := exactDecimal(s.PnLUSD)
		sum.add(r, places)
		total.Included = append(total.Included, s.Source)
	}
	if len(total.Included) > 0 {
		total.Status, total.PnLUSD = pnlStatusOK, sum.String()
	}
	return sources, total
}

func (a *App) pnlNetWorth(e *env, p *resolved, rg pnlRange, s *pnlSource) {
	var res v2.GetNetWorthChartResponse
	ctx, cancel := e.requestCtx()
	err := e.client(p).Portfolio(ctx, "GetNetWorthChart", &v2.GetNetWorthChartRequest{WalletAddress: s.Address, Period: rg.period}, &res, false)
	cancel()
	if err != nil {
		s.fail(errReason(apiError(err, "GetNetWorthChart")))
		return
	}
	pts := res.GetPoints()
	n := len(pts)
	s.Points = &n
	if n == 0 {
		s.fail("no net worth history for this range")
		return
	}
	first, fp, ok1 := floatDecimal(pts[0].GetValueUsd())
	last, lp, ok2 := floatDecimal(pts[n-1].GetValueUsd())
	current, cp, ok3 := floatDecimal(res.GetValueUsd())
	if !ok1 || !ok2 || !ok3 {
		s.fail("invalid net worth value from Oku")
		return
	}
	change := newUSDSum()
	change.add(last, lp)
	change.sub(first, fp)
	s.StartValueUSD = pnl.Decimal(first, fp)
	s.EndValueUSD = pnl.Decimal(last, lp)
	s.ValueUSD = pnl.Decimal(current, cp)
	s.ChangeUSD = change.String()
}

func (a *App) pnlPerps(e *env, p *resolved, rg pnlRange, s *pnlSource) {
	acct, err := e.readAccount(p)
	if err != nil {
		s.fail(errReason(err))
		return
	}
	s.Address = acct
	var res v2.GetPerpsPnlSummaryResponse
	if err := e.perpsRead(p, "GetPerpsPnlSummary", &v2.GetPerpsPnlSummaryRequest{Address: acct}, &res); err != nil {
		s.fail(errReason(err))
		return
	}
	raw := rg.perps(&res)
	r, places, ok := exactDecimal(raw)
	if !ok {
		s.fail(fmt.Sprintf("invalid perps PnL %q from Oku", raw))
		return
	}
	s.PnLUSD = pnl.Decimal(r, places)
	s.AsOfMs = res.GetAsOfMs()
}

// pnlSwaps sums realized and unrealized USD of the agent's journaled swaps
// with the same average-cost book as swap pnl.
func (a *App) pnlSwaps(e *env, w *resolvedWallet, chain uint64, s *pnlSource) {
	j, err := openJournal(w.profile)
	if err != nil {
		s.fail(errReason(err))
		return
	}
	defer j.Close()
	chains := []uint64{chain}
	if chain == 0 {
		if chains, err = j.ListOnchainSwapChains(w.agent); err != nil {
			s.fail("journal: " + err.Error())
			return
		}
	}
	realized, unrealized := newUSDSum(), newUSDSum()
	s.Chains = []pnlSwapChain{}
	var unknown []string
	for _, c := range chains {
		pc, cerr := a.chainPnL(e, j, w.agent, c, "")
		if cerr != nil {
			s.fail(errReason(cerr))
			return
		}
		row := pnlSwapChain{ChainID: c, RealizedUSD: pc.Total.RealizedUSD, UnrealizedUSD: pc.Total.UnrealizedUSD, Warnings: pc.Warnings}
		rr, rp, rok := exactDecimal(pc.Total.RealizedUSD)
		ur, up, uok := exactDecimal(pc.Total.UnrealizedUSD)
		if rok && uok {
			sum := newUSDSum()
			sum.add(rr, rp)
			sum.add(ur, up)
			row.PnLUSD = sum.String()
			realized.add(rr, rp)
			unrealized.add(ur, up)
		} else {
			unknown = append(unknown, strconv.FormatUint(c, 10))
		}
		s.Chains = append(s.Chains, row)
	}
	if len(unknown) > 0 {
		s.fail("USD value unknown on chain " + strings.Join(unknown, ", ") + " (missing oracle price; see oku swap pnl)")
		return
	}
	s.RealizedUSD, s.UnrealizedUSD = realized.String(), unrealized.String()
	total := newUSDSum()
	total.add(realized.r, realized.places)
	total.add(unrealized.r, unrealized.places)
	s.PnLUSD = total.String()
}

func usd2(s string) string {
	r, _, ok := exactDecimal(s)
	if !ok {
		return s
	}
	out := r.FloatString(2)
	if out == "-0.00" {
		out = "0.00"
	}
	return out
}

func writePnLSummary(w io.Writer, rangeName string, chain uint64, sources []pnlSource, total pnlTotalSummary) {
	fmt.Fprintf(w, "PnL (range %s, USD)\n", rangeName)
	tw := newTable(w)
	fmt.Fprintln(tw, "SOURCE\tPNL USD\tNOTE")
	for _, s := range sources[1:] {
		if s.Status != pnlStatusOK {
			fmt.Fprintf(tw, "%s\tunavailable: %s\t\n", s.Label, s.Error)
			continue
		}
		note := ""
		switch s.Source {
		case "perps":
			if s.AsOfMs > 0 {
				note = "as of " + time.UnixMilli(s.AsOfMs).UTC().Format(time.RFC3339)
			}
		case "agent_swaps":
			note = fmt.Sprintf("realized %s, unrealized %s", usd2(s.RealizedUSD), usd2(s.UnrealizedUSD))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Label, usd2(s.PnLUSD), note)
	}
	if total.Status == pnlStatusOK {
		fmt.Fprintf(tw, "TOTAL\t%s\t%s\n", usd2(total.PnLUSD), strings.Join(total.Included, " + "))
	} else {
		fmt.Fprintln(tw, "TOTAL\tunavailable: no source available\t")
	}
	tw.Flush()
	if nw := sources[0]; nw.Status == pnlStatusOK {
		fmt.Fprintf(w, "\n%s: %s (now %s; not in total)\n", nw.Label, usd2(nw.ChangeUSD), usd2(nw.ValueUSD))
	} else {
		fmt.Fprintf(w, "\n%s: unavailable: %s\n", nw.Label, nw.Error)
	}
	if len(total.Omitted) > 0 {
		names := make([]string, 0, len(total.Omitted))
		for _, o := range total.Omitted {
			names = append(names, o.Source)
		}
		fmt.Fprintf(w, "omitted from total: %s\n", strings.Join(names, ", "))
	}
	if chain != 0 {
		fmt.Fprintf(w, "--chain %d filters agent swaps and launchpad only; net worth and perps are unaffected.\n", chain)
	}
}
