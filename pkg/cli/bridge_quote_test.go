package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBridgeQuoteFollowsGenerationAndAcceptsAcrossWithoutSimulation(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.rpcURL = decimalsRPC(t)
	raw, err := os.ReadFile("testdata/across_arbitrum_base_quote.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	across := fixture["quote"].(map[string]any)
	otherQuote := map[string]any{}
	for key, value := range across["quote"].(map[string]any) {
		otherQuote[key] = value
	}
	otherQuote["intentBased"] = true
	other := map[string]any{"bridge": "other", "fetched": true, "quoteId": "unsafe", "quote": otherQuote}
	polls := 0
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		switch method {
		case "GetSupportedBridges":
			return map[string]any{"bridges": []any{map[string]any{"name": "across", "active": true, "supportedChains": []string{"eip155:42161", "eip155:8453"}}, map[string]any{"name": "other", "active": true, "supportedChains": []string{"eip155:42161", "eip155:8453"}}}}, true
		case "Create":
			return map[string]any{"orderId": "order"}, true
		case "UpdateQuoteParams":
			if body["orderId"] != "order" || body["params"] == nil {
				t.Errorf("params not echoed: %v", body)
			}
			return map[string]any{"orderId": "order", "generation": 7, "timeline": 2}, true
		case "GetNewQuotes":
			polls++
			if body["generation"] != float64(7) {
				t.Errorf("wrong generation: %v", body)
			}
			if polls == 1 {
				return map[string]any{"quotes": map[string]any{"other": other}}, true
			}
			if fetched, _ := body["fetchedBridges"].([]any); len(fetched) != 1 || fetched[0] != "other" {
				t.Errorf("fetched bridges: %v", body)
			}
			return map[string]any{"quotes": map[string]any{"across": across}}, true
		case "GetQuotes", "GetQuote":
			t.Errorf("quote collection used %s", method)
		case "Cancel":
			return map[string]any{}, true
		}
		return nil, false
	}
	r := h.run("bridge", "quote", "--chain", "42161", "--destination-chain", "8453", "--from", "0xaf88d065e77c8cc2239327c5edb3a432268e5831", "--to", "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", "--amount", "0.5", "--recipient", h.fake.agentAddress, "--json")
	if r.code != 0 {
		t.Fatalf("bridge quote: %s %s", r.stdout, r.stderr)
	}
	if polls != 2 || !strings.EqualFold(r.data()["route"].(string), "across") || r.data()["min_out"] != "495817" {
		t.Fatalf("unexpected quote: polls=%d data=%v", polls, r.data())
	}
}
