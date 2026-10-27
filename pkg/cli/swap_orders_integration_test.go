package cli

import (
	"fmt"
	"strings"
	"testing"
)

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
		}
		return nil, false
	}
	h.rpcURL = decimalsRPC(t) // decimals 6

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
