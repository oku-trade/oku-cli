package cli

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

// tokenRPC answers the chain-ID check, ERC-20 decimals() (6) and
// balanceOf() with balance raw units. Other calls return 6, like decimalsRPC.
func tokenRPC(t *testing.T, balance *big.Int) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_call":
			call, _ := req.Params[0].(map[string]any)
			data, _ := call["data"].(string)
			switch {
			case strings.HasPrefix(data, "0x313ce567"):
				fmt.Fprintf(w, `{"result":"0x%064x"}`, 6)
			case strings.HasPrefix(data, "0x70a08231"):
				fmt.Fprintf(w, `{"result":"0x%064x"}`, balance)
			default:
				fmt.Fprintf(w, `{"result":"0x%064x"}`, 6)
			}
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// End to end through the executable: create an order, dry-run watch, then a
// real watch tick that sells through the real swap prepare (guardrails) and
// submit paths against the fake Oku backend.
func TestSwapOrdersExecutableLifecycle(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	token := "0x3333333333333333333333333333333333333333"
	price := 0.05 // USD per token; native is 2000 USD
	inUSD, outUSD := 1.0, 0.99
	calls := map[string]int{}
	var created map[string]any
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		calls[method]++
		switch method {
		case "GetTokenPrice":
			return map[string]any{"results": []any{
				map[string]any{"chain": "eip155:42161", "address": eip7528Address, "priceUsd": 2000},
				map[string]any{"chain": "eip155:42161", "address": token, "priceUsd": price},
			}}, true
		case "GetSupportedMarkets":
			return map[string]any{"markets": []any{map[string]any{"name": "zeroex", "active": true, "supportedChainIds": []int{42161}}}}, true
		case "Create":
			created = body
			return map[string]any{"orderId": "01a15585-427e-70f2-a7e4-7457069b9c0c", "timeline": 1}, true
		case "GetFlowstreamQuotes":
			return map[string]any{"timeline": 1, "quotes": map[string]any{"zeroex": map[string]any{"router": "zeroex", "fetched": true, "quoteId": "q", "quote": map[string]any{
				"chainId": 42161, "isExactIn": true, "inAmountRaw": "10000000", "minAmountOutRaw": "90", "outAmountRaw": "100", "netUsdValue": 1,
				"inUsdValue": inUSD, "outUsdValue": outUSD,
				"inToken": map[string]any{"address": token}, "outToken": map[string]any{"address": "0x0000000000000000000000000000000000000000"},
				"simulation": map[string]any{"amountOutRaw": "100"},
			}}}}, true
		case "UpdateQuoteSelect":
			return nil, false // unhandled: fake returns an error before anything is signed
		case "Ping":
			return map[string]any{"orderId": body["orderId"], "status": "failed"}, true
		}
		return nil, false
	}
	h.rpcURL = tokenRPC(t, big.NewInt(25_000_000)) // 25 tokens, decimals 6

	if r := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--amount", "10", "--stop-loss-pct", "10", "--json"); r.errCode() != "USAGE" || !strings.Contains(r.stdout, "--basis") {
		t.Fatalf("missing basis must be USAGE: %s", r.stdout)
	}
	if r := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--amount", "10", "--json"); r.errCode() != "USAGE" {
		t.Fatalf("no trigger must be USAGE: %s", r.stdout)
	}
	if r := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--amount", "10", "--all", "--trailing-stop-pct", "5", "--json"); r.errCode() != "USAGE" {
		t.Fatalf("--amount with --all must be USAGE: %s", r.stdout)
	}
	if r := h.run("swap", "orders", "create", "--chain", "42161", "--token", "native", "--amount", "10", "--trailing-stop-pct", "5", "--json"); r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("native token must be refused: %s", r.stdout)
	}
	if r := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--amount", "25.000001", "--trailing-stop-pct", "5", "--json"); r.errCode() != "INSUFFICIENT_BALANCE" || !strings.Contains(r.stdout, "balance of 25") {
		t.Fatalf("amount above balance must be INSUFFICIENT_BALANCE: %s", r.stdout)
	}
	c := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--amount", "10", "--stop-loss-pct", "10", "--trailing-stop-pct", "20", "--basis", "0.00025", "--id", "so-e2e", "--json")
	d := c.data()
	// 10 tokens * 0.05 / 2000 = 0.00025 native
	if c.code != 0 || d["state"] != "active" || d["basis_source"] != "override" || d["last_value"] != "0.00025" || d["peak_value"] != "0.00025" || d["stop_loss_value"] != "0.000225" || c.env["command"] != "swap.orders.create" {
		t.Fatalf("create: %s %s", c.stdout, c.stderr)
	}
	if calls["Create"] != 0 {
		t.Fatal("create must not create backend swap orders")
	}

	price = 0.04 // value 0.0002: below the stop-loss
	dry := h.run("swap", "orders", "watch", "--once", "--dry-run", "--json")
	last := dry.data()["last"].(map[string]any)["orders"].([]any)[0].(map[string]any)
	if dry.code != 0 || last["would_trigger"] != true || last["trigger"] != "stop_loss" || calls["Create"] != 0 {
		t.Fatalf("dry run: %s", dry.stdout)
	}
	if l := h.run("swap", "orders", "list", "--json"); l.data()["orders"].([]any)[0].(map[string]any)["last_value"] != "0.00025" {
		t.Fatalf("dry run changed the journal: %s", l.stdout)
	}

	// High impact: the real prepare guardrail refuses; nothing is signed and
	// the order stays armed.
	inUSD, outUSD = 1, 0.5
	w := h.run("swap", "orders", "watch", "--once", "--json")
	o := w.data()["last"].(map[string]any)["orders"].([]any)[0].(map[string]any)
	if w.code != 0 || o["state"] != "active" || !strings.Contains(o["error"].(string), "high impact") || calls["UpdateQuoteSelect"] != 0 {
		t.Fatalf("high impact: %s", w.stdout)
	}
	params := created["params"].(map[string]any)
	if params["inTokenAddress"] != token || params["outTokenAddress"] != "0x0000000000000000000000000000000000000000" || params["tokenAmount"] != "10" || params["slippagePpm"] != float64(5000) {
		t.Fatalf("sell params: %v", params)
	}

	// Acceptable quote: prepare journals a sell intent, submit fails before
	// broadcast, so the order re-arms and retries with a new intent ID until
	// it gives up, without needs_reconcile, and never re-sells after that.
	inUSD, outUSD = 1, 0.99
	w = h.run("swap", "orders", "watch", "--once", "--json")
	o = w.data()["last"].(map[string]any)["orders"].([]any)[0].(map[string]any)
	if o["state"] != "active" || o["sell_intent_id"] != "so-e2e-sell-1" || !strings.Contains(o["error"].(string), "not broadcast") {
		t.Fatalf("sell: %s %s", w.stdout, w.stderr)
	}
	for i := 2; i <= maxSwapOrderAttempts; i++ {
		w = h.run("swap", "orders", "watch", "--once", "--json")
	}
	o = w.data()["last"].(map[string]any)["orders"].([]any)[0].(map[string]any)
	if o["state"] != "failed" || o["sell_intent_id"] != fmt.Sprintf("so-e2e-sell-%d", maxSwapOrderAttempts) || !strings.Contains(o["error"].(string), "not broadcast") {
		t.Fatalf("final sell: %s %s", w.stdout, w.stderr)
	}
	creates := calls["Create"]
	h.run("swap", "orders", "watch", "--once", "--json")
	if calls["Create"] != creates {
		t.Fatal("failed order sold again")
	}
	if r := h.run("swap", "orders", "cancel", "--order", "so-e2e", "--json"); r.errCode() != "INTENT_STATE" {
		t.Fatalf("cancel after trigger: %s", r.stdout)
	}
	l := h.run("swap", "orders", "list", "--json").data()["orders"].([]any)[0].(map[string]any)
	if l["needs_reconcile"] != false || l["attempts"] != float64(maxSwapOrderAttempts) || l["trigger"] != "stop_loss" {
		t.Fatalf("list: %v", l)
	}
	if s := h.run("swap", "status", "--intent", "so-e2e-sell-1", "--json"); !strings.Contains(s.stdout, "so-e2e-sell-1") {
		t.Fatalf("sell intent not in swap journal: %s", s.stdout)
	}
}

func TestSwapOrdersCreateRefusesSecondAllOrder(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	token := "0x33333333333333333333333333333333333abcde"
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		if method == "GetTokenPrice" {
			return map[string]any{"results": []any{
				map[string]any{"chain": "eip155:42161", "address": eip7528Address, "priceUsd": 2000},
				map[string]any{"chain": "eip155:42161", "address": token, "priceUsd": 0.05},
			}}, true
		}
		return nil, false
	}
	h.rpcURL = tokenRPC(t, big.NewInt(25_000_000))
	if r := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--all", "--trailing-stop-pct", "5", "--id", "so-all-1", "--json"); r.code != 0 || r.data()["size"] != "25" {
		t.Fatalf("first --all: %s %s", r.stdout, r.stderr)
	}
	r := h.run("swap", "orders", "create", "--chain", "42161", "--token", "0x"+strings.ToUpper(token[2:]), "--all", "--trailing-stop-pct", "10", "--id", "so-all-2", "--json")
	if r.errCode() != "INTENT_CONFLICT" || !strings.Contains(r.stdout, "so-all-1") {
		t.Fatalf("second --all must be INTENT_CONFLICT: %s", r.stdout)
	}
	if r := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--amount", "5", "--trailing-stop-pct", "10", "--id", "so-part", "--json"); r.code != 0 {
		t.Fatalf("--amount order next to --all: %s", r.stdout)
	}
	h.run("swap", "orders", "cancel", "--order", "so-all-1", "--json")
	if r := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--all", "--trailing-stop-pct", "10", "--id", "so-all-3", "--json"); r.code != 0 {
		t.Fatalf("--all after cancel: %s", r.stdout)
	}
}

// The default cost basis sums confirmed native buys of the token to the agent
// and ignores non-native and unconfirmed buys.
func TestSwapOrdersCreateJournalBasis(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	token := "0x3333333333333333333333333333333333333333"
	agent := h.fake.agentAddress
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		if method == "GetTokenPrice" {
			return map[string]any{"results": []any{
				map[string]any{"chain": "eip155:42161", "address": eip7528Address, "priceUsd": 2000},
				map[string]any{"chain": "eip155:42161", "address": token, "priceUsd": 0.05},
			}}, true
		}
		return nil, false
	}
	h.rpcURL = tokenRPC(t, big.NewInt(25_000_000))
	j, err := journal.Open("main")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []struct {
		id, in, amount, minOut string
		confirmed              bool
	}{
		{"buy-native-1", "native", "1000000000000000000", "10000000", true},
		{"buy-native-2", "native", "2000000000000000000", "20000000", true},
		{"buy-usdc", "0xaf88d065e77c8cC2239327C5EDb3A432268e5831", "9000000000000000000", "5000000", true},
		{"buy-pending", "native", "7000000000000000000", "5000000", false},
	} {
		in := journal.OnchainIntent{ID: b.id, Kind: "swap", ChainID: 42161, Sender: agent, Recipient: agent, Token: b.in, OutToken: token, Amount: b.amount, MinOut: b.minOut, Templates: []journal.OnchainTemplate{{ChainID: 42161, To: token, Value: "0x0"}}}
		if _, err = j.CreateOnchain(in); err != nil {
			t.Fatal(err)
		}
		if b.confirmed {
			if err = j.AdvanceOnchain(b.id, "prepared", "confirmed", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	// 3 native for 30 tokens: 15 tokens cost 1.5 native.
	r := h.run("swap", "orders", "create", "--chain", "42161", "--token", token, "--amount", "15", "--take-profit-pct", "50", "--id", "so-basis", "--json")
	d := r.data()
	if r.code != 0 || d["basis_source"] != "journal" || d["basis"] != "1.5" || d["take_profit_value"] != "2.25" {
		t.Fatalf("journal basis: %s %s", r.stdout, r.stderr)
	}
}
