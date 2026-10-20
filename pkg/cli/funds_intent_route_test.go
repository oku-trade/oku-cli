package cli

import (
	"testing"
)

// Selection remains a CLI behavior: intent and transaction routes compete by
// net value, while route and no-intent constrain the backend request/choice.
func TestSwapIntentRouteFlags(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	input := "0x3333333333333333333333333333333333333333"
	output := "0x2222222222222222222222222222222222222222"
	typed := map[string]any{"types": map[string]any{"Order": []any{map[string]any{"name": "sellToken", "type": "address"}}}, "domain": map[string]any{"name": "CoW"}, "message": map[string]any{"sellToken": input}, "primaryType": "Order"}
	quote := func(route string, intent bool) map[string]any {
		q := map[string]any{"intentBased": intent, "chainId": 42161, "isExactIn": true, "inAmountRaw": "10000000", "minAmountOutRaw": "90", "outAmountRaw": "100", "netUsdValue": "25", "priceImpact": 0, "inToken": map[string]any{"address": input}, "outToken": map[string]any{"address": output}}
		if intent {
			q["signingRequest"] = map[string]any{"typedData": []any{map[string]any{"payload": typed}}}
		} else {
			q["executionInfo"] = map[string]any{"trade": map[string]any{"to": okuRouter}}
			q["simulation"] = map[string]any{"amountOutRaw": "100"}
			q["netUsdValue"] = "20"
		}
		return map[string]any{"router": route, "quoteId": route + "-quote", "fetched": true, "quote": q}
	}
	var markets []any
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		switch method {
		case "GetSupportedMarkets":
			return map[string]any{"markets": []any{map[string]any{"name": "cow", "active": true, "supportedChainIds": []int{42161}}, map[string]any{"name": "zeroex", "active": true, "supportedChainIds": []int{42161}}}}, true
		case "Create":
			markets = body["params"].(map[string]any)["enabledMarkets"].([]any)
			return map[string]any{"orderId": "order", "timeline": 1}, true
		case "GetFlowstreamQuotes":
			return map[string]any{"timeline": 1, "quotes": map[string]any{"cow": quote("cow", true), "zeroex": quote("zeroex", false)}}, true
		case "Cancel":
			return map[string]any{}, true
		}
		return nil, false
	}
	h.rpcURL = decimalsRPC(t)
	base := []string{"swap", "quote", "--chain", "42161", "--from", input, "--to", output, "--amount", "10", "--json"}
	for _, tc := range []struct {
		extra   []string
		route   string
		intent  bool
		enabled int
	}{{nil, "cow", true, 2}, {[]string{"--no-intent"}, "zeroex", false, 2}, {[]string{"--route", "cow"}, "cow", true, 1}} {
		r := h.run(append(append([]string{}, base...), tc.extra...)...)
		if r.code != 0 || r.data()["route"] != tc.route || r.data()["intent_based"] != tc.intent || len(markets) != tc.enabled {
			t.Fatalf("quote flags %v: %s %s markets=%v", tc.extra, r.stdout, r.stderr, markets)
		}
	}
}
