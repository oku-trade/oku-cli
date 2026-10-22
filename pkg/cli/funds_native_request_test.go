package cli

import "testing"

func TestNativeQuoteRequestAddresses(t *testing.T) {
	for _, kind := range []string{"swap", "bridge"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			h.rpcURL = decimalsRPC(t)
			calls := 0
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				switch method {
				case "GetSupportedMarkets":
					return map[string]any{"markets": []any{map[string]any{"name": "zeroex", "active": true, "supportedChainIds": []int{42161}}}}, true
				case "GetSupportedBridges":
					return map[string]any{"bridges": []any{map[string]any{"name": "across", "active": true, "supportedChains": []string{"eip155:42161", "eip155:8453"}}}}, true
				case "Create", "UpdateQuoteParams":
					if method == "Create" {
						calls++
					}
					params := body["params"].(map[string]any)
					fields := []string{"inTokenAddress", "outTokenAddress"}
					if kind == "swap" {
						fields = fields[:1]
					}
					for _, field := range fields {
						if got := params[field]; got != "0x0000000000000000000000000000000000000000" {
							t.Errorf("%s %s %s = %v, want zero address", kind, method, field, got)
						}
					}
					return map[string]any{"orderId": "01a15585-427e-70f2-a7e4-7457069b9c0c", "timeline": 1, "generation": 1}, true
				}
				return nil, false
			}
			args := []string{kind, "quote", "--chain", "42161", "--from", "native", "--to", "0x2222222222222222222222222222222222222222", "--amount", "0.01", "--json"}
			if kind == "bridge" {
				args[7] = "native"
				args = append(args, "--destination-chain", "8453")
			}
			result := h.run(args...)
			if calls != 1 {
				t.Fatalf("Create calls = %d, result: %s %s", calls, result.stdout, result.stderr)
			}
		})
	}
}
