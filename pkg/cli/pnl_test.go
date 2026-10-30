package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/journal"
)

// pnlFake is an account API answering the pnl sources by method name. A nil
// handler answers 503 so that source fails.
type pnlFake struct {
	netWorth func(body map[string]any) any
	perps    func(body map[string]any) any
	usd      map[string]float64
	calls    []string
	bodies   map[string]map[string]any
}

func (f *pnlFake) serve(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.calls = append(f.calls, method)
		f.bodies[method] = body
		var h func(map[string]any) any
		switch method {
		case "GetNetWorthChart":
			if !strings.HasSuffix(r.URL.Path, "/gfxcafe.oku.account.v2.PortfolioService/GetNetWorthChart") {
				t.Errorf("net worth path %s", r.URL.Path)
			}
			h = f.netWorth
		case "GetPerpsPnlSummary":
			h = f.perps
		case "GetTokenPrice":
			h = func(body map[string]any) any {
				results := []map[string]any{}
				tokens, _ := body["tokens"].([]any)
				for _, raw := range tokens {
					tk, _ := raw.(map[string]any)
					addr, _ := tk["address"].(string)
					key := strings.ToLower(addr)
					if isNativeToken(addr) {
						key = "native"
					}
					if p, ok := f.usd[key]; ok {
						results = append(results, map[string]any{"chain": "eip155:42161", "address": addr, "priceUsd": p})
					}
				}
				return map[string]any{"results": results}
			}
		default:
			t.Errorf("unexpected API call %s", method)
		}
		if r.Header.Get("x-oku-device-token") != "" || r.Header.Get("x-oku-account-session") != "" {
			t.Errorf("%s sent credentials", method)
		}
		if h == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "unavailable", "message": "upstream down"})
			return
		}
		_ = json.NewEncoder(w).Encode(h(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// pnlSetup journals one agent buy of 1000 USDC for 1 ETH (gas 0.001 ETH)
// and returns an env whose profile talks to f.
func pnlSetup(t *testing.T, f *pnlFake) (*App, *env, *resolvedWallet) {
	t.Helper()
	f.bodies = map[string]map[string]any{}
	t.Setenv("OKU_STATE_DIR", t.TempDir())
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	url := f.serve(t)
	cfg := &config.File{Version: 1, DefaultProfile: "main", Profiles: map[string]*config.Profile{"main": {Network: "mainnet", APIURL: url, Account: acct}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	rpcURL := pnlRPC(t)
	a := &App{Getenv: func(k string) string {
		if k == "OKU_RPC_URL_42161" {
			return rpcURL
		}
		return ""
	}, Version: "test", Stderr: &bytes.Buffer{}, Now: time.Now}
	e := &env{app: a, ctx: context.Background(), g: globals{timeout: 5 * time.Second}}
	p, err := e.profile()
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	journalSwap(t, j, "buy-1", soAgent, "native", pnlUSDC, "1000000000000000000", "1000000000", "confirmed")
	if err := j.RecordFill("buy-1", journal.Fill{Received: "1000000000", Gas: "1000000000000000", Source: "transfer_logs"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	return a, e, &resolvedWallet{profile: p, agent: soAgent}
}

func perpsSummary(map[string]any) any {
	return map[string]any{"dayPnl": "1.5", "weekPnl": "-20.25", "monthPnl": "100.125", "allTimePnl": "1234.567891", "asOfMs": "1700000000000"}
}

func netWorthChart(map[string]any) any {
	return map[string]any{"walletAddress": soAgent, "points": []any{
		map[string]any{"timestampUnix": "1", "valueUsd": 1000.1},
		map[string]any{"timestampUnix": "2", "valueUsd": 900},
		map[string]any{"timestampUnix": "3", "valueUsd": 1200.3},
	}, "valueUsd": 1250.55, "changeUsd": 200.2}
}

func sourceByName(t *testing.T, sources []pnlSource, name string) pnlSource {
	t.Helper()
	for _, s := range sources {
		if s.Source == name {
			return s
		}
	}
	t.Fatalf("no %s source in %+v", name, sources)
	return pnlSource{}
}

// Every --range reads the matching net worth chart period and perps window.
// Launchpad and agent swaps stay lifetime and are labeled all-time.
func TestPnLRangeMapping(t *testing.T) {
	cases := []struct {
		rng, period, perps string
	}{
		{"1d", "NET_WORTH_CHART_PERIOD_DAY", "1.5"},
		{"7d", "NET_WORTH_CHART_PERIOD_WEEK", "-20.25"},
		{"30d", "NET_WORTH_CHART_PERIOD_MONTH", "100.125"},
		{"all", "NET_WORTH_CHART_PERIOD_MAX", "1234.567891"},
	}
	for _, c := range cases {
		t.Run(c.rng, func(t *testing.T) {
			f := &pnlFake{netWorth: netWorthChart, perps: perpsSummary, usd: map[string]float64{"native": 2000, strings.ToLower(pnlUSDC): 2.5}}
			a, e, w := pnlSetup(t, f)
			sources, total := a.pnlReport(e, w.profile, w, nil, c.rng, pnlRanges[c.rng], 0)
			if got := f.bodies["GetNetWorthChart"]; got["period"] != c.period || got["walletAddress"] != soAgent {
				t.Fatalf("net worth request: %v", got)
			}
			if got := f.bodies["GetPerpsPnlSummary"]; got["address"] != strings.ToLower(acct) {
				t.Fatalf("perps request: %v", got)
			}
			perps := sourceByName(t, sources, "perps")
			if perps.Status != pnlStatusOK || perps.PnLUSD != c.perps || perps.AsOfMs != 1700000000000 || perps.Range != c.rng {
				t.Fatalf("perps: %+v", perps)
			}
			swaps, lp := sourceByName(t, sources, "agent_swaps"), sourceByName(t, sources, "launchpad")
			if swaps.Range != "all" || lp.Range != "all" {
				t.Fatalf("lifetime ranges: %q %q", swaps.Range, lp.Range)
			}
			allTime := strings.HasSuffix(swaps.Label, "(all-time)") && strings.HasSuffix(lp.Label, "(all-time)")
			if allTime != (c.rng != "all") {
				t.Fatalf("all-time labels for %s: %q %q", c.rng, swaps.Label, lp.Label)
			}
			// 1000 USDC at 2.5 = 2500 USD against a 1 ETH (2000 USD) cost:
			// unrealized 500; realized is only the 0.001 ETH gas, -2 USD.
			if swaps.Status != pnlStatusOK || swaps.RealizedUSD != "-2" || swaps.UnrealizedUSD != "500" || swaps.PnLUSD != "498" {
				t.Fatalf("swaps: %+v", swaps)
			}
			want := map[string]string{"1d": "499.5", "7d": "477.75", "30d": "598.125", "all": "1732.567891"}[c.rng]
			if total.Status != pnlStatusOK || total.PnLUSD != want || strings.Join(total.Included, ",") != "perps,agent_swaps" {
				t.Fatalf("total: %+v want %s", total, want)
			}
		})
	}
}

// Net worth change is last minus first point, exact in decimal, and never
// part of the total. Launchpad has no backend endpoint yet.
func TestPnLNetWorthAndLaunchpad(t *testing.T) {
	f := &pnlFake{netWorth: netWorthChart, perps: perpsSummary, usd: map[string]float64{"native": 2000, strings.ToLower(pnlUSDC): 2.5}}
	a, e, w := pnlSetup(t, f)
	sources, total := a.pnlReport(e, w.profile, w, nil, "all", pnlRanges["all"], 0)
	nw := sourceByName(t, sources, "net_worth")
	if nw.Status != pnlStatusOK || nw.InTotal || nw.ChangeUSD != "200.2" || nw.StartValueUSD != "1000.1" || nw.EndValueUSD != "1200.3" || nw.ValueUSD != "1250.55" || nw.Label != netWorthLabel || *nw.Points != 3 {
		t.Fatalf("net worth: %+v", nw)
	}
	lp := sourceByName(t, sources, "launchpad")
	if lp.Status != pnlStatusUnavailable || lp.Error != launchpadUnavailable || lp.InTotal {
		t.Fatalf("launchpad: %+v", lp)
	}
	if len(total.Omitted) != 1 || total.Omitted[0].Source != "launchpad" || total.Omitted[0].Reason != launchpadUnavailable {
		t.Fatalf("omitted: %+v", total.Omitted)
	}
	var human bytes.Buffer
	writePnLSummary(&human, "all", 0, sources, total)
	for _, s := range []string{"perps", "1234.57", "agent swaps", "498.00", "launchpad", "unavailable: backend endpoint not available yet", "TOTAL", "1732.57", "net worth change (includes deposits/withdrawals): 200.20", "not in total", "omitted from total: launchpad"} {
		if !strings.Contains(human.String(), s) {
			t.Errorf("human output missing %q:\n%s", s, human.String())
		}
	}
}

// One failing source never fails the command: it reports its error, is
// listed as omitted and the total covers the rest.
func TestPnLSourceFailureIsIsolated(t *testing.T) {
	f := &pnlFake{netWorth: nil, perps: nil, usd: map[string]float64{"native": 2000, strings.ToLower(pnlUSDC): 2.5}}
	a, e, w := pnlSetup(t, f)
	sources, total := a.pnlReport(e, w.profile, w, nil, "7d", pnlRanges["7d"], 42161)
	for _, name := range []string{"net_worth", "perps"} {
		s := sourceByName(t, sources, name)
		if s.Status != pnlStatusUnavailable || !strings.Contains(s.Error, "upstream down") || s.InTotal || s.PnLUSD != "" {
			t.Fatalf("%s: %+v", name, s)
		}
	}
	if total.Status != pnlStatusOK || total.PnLUSD != "498" || strings.Join(total.Included, ",") != "agent_swaps" || len(total.Omitted) != 2 {
		t.Fatalf("total: %+v", total)
	}
	var human bytes.Buffer
	writePnLSummary(&human, "7d", 42161, sources, total)
	for _, s := range []string{"perps", "unavailable: GetPerpsPnlSummary", "net worth change (includes deposits/withdrawals): unavailable:", "omitted from total: perps, launchpad", "--chain 42161 filters agent swaps and launchpad only; net worth and perps are unaffected"} {
		if !strings.Contains(human.String(), s) {
			t.Errorf("human output missing %q:\n%s", s, human.String())
		}
	}

	// Without an agent wallet only the wallet sources fail; perps still adds up.
	f2 := &pnlFake{netWorth: netWorthChart, perps: perpsSummary}
	a, e, w = pnlSetup(t, f2)
	sources, total = a.pnlReport(e, w.profile, nil, errNoWallet, "30d", pnlRanges["30d"], 0)
	if s := sourceByName(t, sources, "agent_swaps"); s.Status != pnlStatusUnavailable || s.Error != "no agent wallet" {
		t.Fatalf("swaps without wallet: %+v", s)
	}
	if s := sourceByName(t, sources, "net_worth"); s.Status != pnlStatusUnavailable {
		t.Fatalf("net worth without wallet: %+v", s)
	}
	if total.Status != pnlStatusOK || total.PnLUSD != "100.125" {
		t.Fatalf("total without wallet: %+v", total)
	}
	for _, c := range f2.calls {
		if c == "GetNetWorthChart" {
			t.Fatal("net worth read without a wallet address")
		}
	}

	// Every source unavailable: the total is unavailable, not zero.
	f3 := &pnlFake{}
	a, e, w = pnlSetup(t, f3)
	_, total = a.pnlReport(e, w.profile, nil, errNoWallet, "all", pnlRanges["all"], 0)
	if total.Status != pnlStatusUnavailable || total.PnLUSD != "" || len(total.Omitted) != 3 {
		t.Fatalf("total with nothing: %+v", total)
	}
}

// A chain whose swaps cannot be valued in USD makes the swaps source
// unavailable rather than adding a partial number.
func TestPnLSwapsUnpricedIsUnavailable(t *testing.T) {
	f := &pnlFake{netWorth: netWorthChart, perps: perpsSummary, usd: map[string]float64{"native": 2000}}
	a, e, w := pnlSetup(t, f)
	sources, total := a.pnlReport(e, w.profile, w, nil, "all", pnlRanges["all"], 0)
	s := sourceByName(t, sources, "agent_swaps")
	if s.Status != pnlStatusUnavailable || !strings.Contains(s.Error, "42161") || len(s.Chains) != 1 {
		t.Fatalf("swaps: %+v", s)
	}
	if total.PnLUSD != "1234.567891" {
		t.Fatalf("total: %+v", total)
	}
}

func TestPnLDecimalsAreExact(t *testing.T) {
	r, places, ok := floatDecimal(0.1)
	if !ok || places != 1 || r.RatString() != "1/10" {
		t.Fatalf("0.1 -> %v %d", r, places)
	}
	sum := newUSDSum()
	for _, s := range []string{"0.1", "0.2", "1234567890123.000000001"} {
		v, p, _ := exactDecimal(s)
		sum.add(v, p)
	}
	if sum.String() != "1234567890123.300000001" {
		t.Fatalf("sum %s", sum)
	}
	for _, bad := range []string{"", "1e3", "1/2", "abc"} {
		if _, _, ok := exactDecimal(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestPnLRejectsBadFlags(t *testing.T) {
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	s := NewService("test", &bytes.Buffer{}, func(string) string { return "" })
	if _, err := s.Call(context.Background(), "pnl", Globals{}, map[string]any{"range": "90d"}, nil); err == nil || !strings.Contains(err.Error(), "1d, 7d, 30d, all") {
		t.Fatalf("bad range: %v", err)
	}
	if _, err := s.Call(context.Background(), "pnl", Globals{}, map[string]any{"chain": "abc"}, nil); err == nil || !strings.Contains(err.Error(), "--chain") {
		t.Fatalf("bad chain: %v", err)
	}
}

var errNoWallet = &noWalletError{}

type noWalletError struct{}

func (*noWalletError) Error() string { return "no agent wallet" }
