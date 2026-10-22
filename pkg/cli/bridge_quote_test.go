package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestBridgeQuoteFollowsGenerationAndAcceptsAcrossWithoutSimulation(t *testing.T) {
	// The live backend answers UpdateQuoteParams with generation 0; the web
	// app then polls generation 1. An explicit generation is used as given.
	for _, tc := range []struct{ reported, polled float64 }{{7, 7}, {0, 1}} {
		t.Run(fmt.Sprint(tc.reported), func(t *testing.T) { bridgeQuoteGeneration(t, tc.reported, tc.polled) })
	}
}

func TestBridgeQuoteNativeAddressAliases(t *testing.T) {
	for _, address := range []string{"0x0000000000000000000000000000000000000000", "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE"} {
		t.Run(address, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				switch method {
				case "GetSupportedBridges":
					return map[string]any{"bridges": []any{map[string]any{"name": "lifi", "active": true, "supportedChains": []string{"eip155:8453", "eip155:42161"}}}}, true
				case "Create":
					params := body["params"].(map[string]any)
					if params["inTokenAddress"] != "native" || params["outTokenAddress"] != "native" {
						t.Errorf("native request changed: %v", params)
					}
					return map[string]any{"orderId": "order"}, true
				case "UpdateQuoteParams":
					return map[string]any{"orderId": "order", "generation": 1}, true
				case "GetNewQuotes":
					return map[string]any{"quotes": map[string]any{"lifi": map[string]any{"bridge": "lifi", "fetched": true, "quoteId": "quote", "quote": map[string]any{
						"inChainId": 8453, "outChainId": 42161, "inToken": map[string]any{"address": address, "decimals": 18}, "outToken": map[string]any{"address": address, "decimals": 18},
						"inAmount": "0.01", "minAmountOut": "0.009", "outAmount": "0.0095", "outUsdValue": 20, "feesUsdValue": 1, "gasUsdValue": 1,
					}}}}, true
				}
				return nil, false
			}
			r := h.run("bridge", "quote", "--chain", "8453", "--destination-chain", "42161", "--from", "native", "--to", "native", "--amount", "0.01", "--json")
			if r.code != 0 || r.data()["route"] != "lifi" {
				t.Fatalf("native bridge quote: %s %s", r.stdout, r.stderr)
			}
		})
	}
}

func bridgeQuoteGeneration(t *testing.T, reported, polled float64) {
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
			return map[string]any{"orderId": "order", "generation": reported, "timeline": 2}, true
		case "GetNewQuotes":
			polls++
			if body["generation"] != polled {
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
	if r.data()["target"] != "" {
		t.Fatalf("bridge quote must not infer a trade target: %v", r.data())
	}
	polls = 0
	prepared := h.run("bridge", "prepare", "--chain", "42161", "--destination-chain", "8453", "--from", "0xaf88d065e77c8cc2239327c5edb3a432268e5831", "--to", "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", "--amount", "0.5", "--recipient", h.fake.agentAddress, "--json")
	if prepared.code != 0 || prepared.data()["target"] != nil || prepared.data()["state"] != "prepared" {
		t.Fatalf("bridge prepare without executionInfo: %s %s", prepared.stdout, prepared.stderr)
	}
}
