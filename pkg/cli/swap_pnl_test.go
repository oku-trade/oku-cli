package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/journal"
)

const pnlUSDC = "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"

// pnlRPC answers chain ID, receipts from the OkuRouter fixture, decimals() = 6
// and fails the multicall, so PnL reads decimals token by token.
func pnlRPC(t *testing.T) string {
	raw, err := os.ReadFile("../onchain/testdata/okurouter_arbitrum_receipts.json")
	if err != nil {
		t.Fatal(err)
	}
	receipts := map[string]json.RawMessage{}
	if err = json.Unmarshal(raw, &receipts); err != nil {
		t.Fatal(err)
	}
	byHash := map[string]json.RawMessage{}
	for _, r := range receipts {
		var h struct {
			TransactionHash string `json:"transactionHash"`
		}
		_ = json.Unmarshal(r, &h)
		byHash[strings.ToLower(h.TransactionHash)] = r
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_getTransactionReceipt":
			h, _ := req.Params[0].(string)
			if rec, ok := byHash[strings.ToLower(h)]; ok {
				fmt.Fprintf(w, `{"result":%s}`, rec)
				return
			}
			fmt.Fprint(w, `{"result":null}`)
		case "eth_call":
			call, _ := req.Params[0].(map[string]any)
			if to, _ := call["to"].(string); strings.EqualFold(to, "0xcA11bde05977b3631167028862bE2a173976CA11") {
				fmt.Fprint(w, `{"error":{"message":"no multicall"}}`)
				return
			}
			fmt.Fprintf(w, `{"result":"0x%064x"}`, 6)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// pnlEnv is an env whose profile talks to a fake TokenService answering
// prices (USD) by lower-case address, with native under "native".
func pnlEnv(t *testing.T, rpcURL string, usd map[string]float64) (*App, *env, *bytes.Buffer) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tokens []struct {
				Address string `json:"address"`
			} `json:"tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := []map[string]any{}
		for _, tk := range req.Tokens {
			key := strings.ToLower(tk.Address)
			if isNativeToken(tk.Address) {
				key = "native"
			}
			if p, ok := usd[key]; ok {
				results = append(results, map[string]any{"chain": "eip155:42161", "address": tk.Address, "priceUsd": p})
			} else {
				results = append(results, map[string]any{"chain": "eip155:42161", "address": tk.Address, "error": "no price"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(api.Close)
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	f := &config.File{Version: 1, DefaultProfile: "main", Profiles: map[string]*config.Profile{"main": {Network: "mainnet", APIURL: api.URL, Account: acct}}}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	a := &App{Getenv: func(k string) string {
		if k == "OKU_RPC_URL_42161" {
			return rpcURL
		}
		return ""
	}, Version: "test", Stderr: &stderr, Now: time.Now}
	return a, &env{app: a, ctx: context.Background(), g: globals{timeout: 5 * time.Second}}, &stderr
}

func journalSwap(t *testing.T, j *journal.Journal, id, agent, in, out, amount, minOut, state string, hashes ...string) {
	t.Helper()
	rec := journal.OnchainIntent{ID: id, Kind: "swap", ChainID: 42161, Sender: agent, Recipient: agent, Token: in, OutToken: out, Amount: amount, MinOut: minOut, Target: okuRouter, Templates: []journal.OnchainTemplate{{ChainID: 42161, To: okuRouter, Value: "0x0"}}}
	if _, err := j.CreateOnchain(rec); err != nil {
		t.Fatal(err)
	}
	from := "prepared"
	for _, h := range hashes {
		if err := j.AdvanceOnchain(id, from, "sent", h); err != nil {
			t.Fatal(err)
		}
		from = "sent"
	}
	if err := j.AdvanceOnchain(id, from, state, ""); err != nil {
		t.Fatal(err)
	}
}

// The received amount and gas of a confirmed swap come from its receipt's
// Transfer logs into the agent, not from the journaled minimum output.
func TestRecordSwapFillFromReceipt(t *testing.T) {
	j := openSwapJournal(t)
	a, e, stderr := pnlEnv(t, pnlRPC(t), map[string]float64{"native": 2000})
	agent := "0x1f84833dcb4e7421fe691cb64369580c1e48b287"
	journalSwap(t, j, "buy-live", agent, "native", pnlUSDC, "88800000000000000", "200000000", "confirmed", "0x3049ab9fad07df0672fff44e164b1ad595698372ee79124cb4d4d4b8ffbc65f9")
	r, err := j.GetOnchain("buy-live")
	if err != nil {
		t.Fatal(err)
	}
	r = a.recordSwapFill(e, j, r)
	if r.Fill == nil || r.Fill.Received != "210021894" || r.Fill.Gas != "10633920000000" || r.Fill.Source != "transfer_logs" || r.Fill.InValue != "" {
		t.Fatalf("fill: %+v stderr %s", r.Fill, stderr)
	}
	if again := a.recordSwapFill(e, j, r); again.Fill.Received != "210021894" {
		t.Fatal("fill rewritten")
	}
	journalSwap(t, j, "buy-missing", agent, "native", pnlUSDC, "1", "1", "confirmed", "0x"+strings.Repeat("ab", 32))
	missing, _ := j.GetOnchain("buy-missing")
	if got := a.recordSwapFill(e, j, missing); got.Fill != nil || !strings.Contains(stderr.String(), "minimum output") {
		t.Fatalf("missing receipt: %+v %s", got.Fill, stderr)
	}
}

func TestSwapPnLRows(t *testing.T) {
	j := openSwapJournal(t)
	other := "0x2222222222222222222222222222222222222222"
	a, e, _ := pnlEnv(t, pnlRPC(t), map[string]float64{"native": 2000, strings.ToLower(pnlUSDC): 0.5})
	journalSwap(t, j, "buy-1", soAgent, "native", pnlUSDC, "1000000000000000000", "990000000", "confirmed")
	if err := j.RecordFill("buy-1", journal.Fill{Received: "1000000000", Gas: "1000000000000000", Source: "transfer_logs"}); err != nil {
		t.Fatal(err)
	}
	journalSwap(t, j, "buy-legacy", soAgent, "native", pnlUSDC, "3000000000000000000", "1000000000", "confirmed")
	journalSwap(t, j, "sell-1", soAgent, pnlUSDC, "native", "500000000", "1400000000000000000", "confirmed")
	if err := j.RecordFill("sell-1", journal.Fill{Received: "1500000000000000000", Gas: "1000000000000000", Source: "router_event"}); err != nil {
		t.Fatal(err)
	}
	journalSwap(t, j, "buy-unknown", soAgent, "native", pnlUSDC, "9000000000000000000", "1", "unknown")
	journalSwap(t, j, "buy-failed", soAgent, "native", pnlUSDC, "9000000000000000000", "1", "failed")
	journalSwap(t, j, "buy-unpriced", soAgent, "native", other, "100000000000000000", "10000000", "confirmed")
	if err := j.RecordFill("buy-unpriced", journal.Fill{Received: "10000000", Gas: "0", Source: "transfer_logs"}); err != nil {
		t.Fatal(err)
	}
	pc, err := a.chainPnL(e, j, soAgent, 42161, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pc.Tokens) != 2 {
		t.Fatalf("rows: %+v", pc.Tokens)
	}
	usdc, unpriced := pc.Tokens[1], pc.Tokens[0]
	if usdc.Token != strings.ToLower(pnlUSDC) {
		usdc, unpriced = unpriced, usdc
	}
	want := pnlRow{Token: strings.ToLower(pnlUSDC), Held: "1500", AvgCost: "0.002", CostBasis: "3", Price: "0.00025", ValueNative: "0.375", ValueUSD: "750", UnrealizedNative: "-2.625", UnrealizedUSD: "-5250", RealizedNative: "0.498", RealizedUSD: "996", FeesNative: "0.002", FeesUSD: "4", FeesUnknown: 1, Swaps: 3, Approximate: true, PriceKnown: true}
	usdc.Decimals = nil
	if usdc != want {
		t.Fatalf("usdc row:\n got %+v\nwant %+v", usdc, want)
	}
	if unpriced.PriceKnown || unpriced.UnrealizedNative != "" || unpriced.ValueNative != "" || unpriced.Held != "10" || unpriced.CostBasis != "0.1" || unpriced.Approximate {
		t.Fatalf("unpriced row: %+v", unpriced)
	}
	if pc.Total.UnrealizedNative != "" || pc.Total.ValueNative != "" || pc.Total.CostBasis != "3.1" || pc.Total.RealizedNative != "0.498" || !pc.Total.Approximate {
		t.Fatalf("total: %+v", pc.Total)
	}
	only, err := a.chainPnL(e, j, soAgent, 42161, pnlUSDC)
	if err != nil || len(only.Tokens) != 1 || only.Total.UnrealizedNative != "-2.625" || only.Total.ValueUSD != "750" {
		t.Fatalf("--token: %+v %v", only, err)
	}
	var human bytes.Buffer
	writePnL(&human, []pnlChain{pc})
	for _, s := range []string{"TOKEN", "AVG COST", "TOTAL ~", "-2.625", "?", "net of swap gas"} {
		if !strings.Contains(human.String(), s) {
			t.Errorf("human output missing %q:\n%s", s, human.String())
		}
	}
}

func TestSwapPnLFullExitWithoutPrice(t *testing.T) {
	j := openSwapJournal(t)
	a, e, _ := pnlEnv(t, pnlRPC(t), map[string]float64{"native": 2000})
	journalSwap(t, j, "buy", soAgent, "native", pnlUSDC, "1000000000000000000", "1000000", "confirmed")
	journalSwap(t, j, "sell", soAgent, pnlUSDC, "native", "1000000", "900000000000000000", "confirmed")
	pc, err := a.chainPnL(e, j, soAgent, 42161, "")
	if err != nil || len(pc.Tokens) != 1 {
		t.Fatalf("%+v %v", pc, err)
	}
	r := pc.Tokens[0]
	if r.Held != "0" || r.AvgCost != "" || r.CostBasis != "0" || r.UnrealizedNative != "0" || r.ValueNative != "0" || r.RealizedNative != "-0.1" || !r.Approximate || pc.Total.UnrealizedNative != "0" {
		t.Fatalf("exit row: %+v total %+v", r, pc.Total)
	}
}

// swap pnl --json through the executable: oku.cli/v1 envelope, one chain and
// row per traded token, and the shared basis fields.
func TestSwapPnLExecutableJSON(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	agent := h.fake.agentAddress
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		if method == "GetTokenPrice" {
			return map[string]any{"results": []any{
				map[string]any{"chain": "eip155:42161", "address": eip7528Address, "priceUsd": 2000},
				map[string]any{"chain": "eip155:42161", "address": strings.ToLower(pnlUSDC), "priceUsd": 0.5},
			}}, true
		}
		return nil, false
	}
	h.rpcURL = pnlRPC(t)
	j, err := journal.Open("main")
	if err != nil {
		t.Fatal(err)
	}
	journalSwap(t, j, "exe-buy", agent, "native", pnlUSDC, "1000000000000000000", "1000000000", "confirmed")
	journalSwap(t, j, "exe-unknown", agent, "native", pnlUSDC, "5000000000000000000", "1", "unknown")
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	r := h.run("swap", "pnl", "--chain", "42161", "--json")
	d := r.data()
	if r.code != 0 || r.env["schema_version"] != "oku.cli/v1" || r.env["command"] != "swap.pnl" || d["realized_includes_gas"] != true {
		t.Fatalf("pnl: %s %s", r.stdout, r.stderr)
	}
	chains, _ := d["chains"].([]any)
	if len(chains) != 1 {
		t.Fatalf("chains: %s", r.stdout)
	}
	c := chains[0].(map[string]any)
	rows, _ := c["tokens"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows: %s", r.stdout)
	}
	row := rows[0].(map[string]any)
	if row["held"] != "1000" || row["cost_basis_native"] != "1" || row["value_native"] != "0.25" || row["unrealized_native"] != "-0.75" || row["approximate"] != true {
		t.Fatalf("row: %v", row)
	}
	if bad := h.run("swap", "pnl", "--token", "native", "--json"); bad.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("native --token: %s", bad.stdout)
	}
}

// swap orders takes its default basis from the shared average-cost book:
// measured fills, net of tracked sells.
func TestJournalBasisUsesFillsAndSells(t *testing.T) {
	j := openSwapJournal(t)
	journalSwap(t, j, "basis-b1", soAgent, "native", soToken, "1000", "5", "confirmed")
	if err := j.RecordFill("basis-b1", journal.Fill{Received: "10", Gas: "0", Source: "transfer_logs"}); err != nil {
		t.Fatal(err)
	}
	journalSwap(t, j, "basis-s1", soAgent, soToken, "native", "5", "1", "confirmed")
	basis, err := journalBasis(j, soAgent, 42161, soToken, big.NewInt(5))
	if err != nil || basis == nil || basis.Cmp(big.NewRat(500, 1_000_000_000_000_000_000)) != 0 {
		t.Fatalf("basis: %v %v", basis, err)
	}
	journalSwap(t, j, "basis-s2", soAgent, soToken, "native", "5", "1", "confirmed")
	if basis, err = journalBasis(j, soAgent, 42161, soToken, big.NewInt(5)); err != nil || basis != nil {
		t.Fatalf("basis after exit: %v %v", basis, err)
	}
}
