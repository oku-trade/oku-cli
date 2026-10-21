package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Swap quotes come from the same GetFlowstreamQuotes long-poll the web app uses.
// Routers arrive across polls; the CLI must send the routers it already has,
// honour the server timeline, drop stale-timeline quotes, and pick the best
// safe route only from the current timeline.
func TestSwapQuoteCollectsFlowstreamAcrossPolls(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	input := "0x3333333333333333333333333333333333333333"
	output := "0x2222222222222222222222222222222222222222"
	agent := h.fake.agentAddress
	quote := func(router, quoteID, net string) map[string]any {
		return map[string]any{"router": router, "fetched": true, "quoteId": quoteID, "quote": map[string]any{
			"chainId": 42161, "isExactIn": true, "inAmountRaw": "10000000", "minAmountOutRaw": "90", "outAmountRaw": "100", "netUsdValue": net, "priceImpact": -0.001,
			"inToken": map[string]any{"address": input}, "outToken": map[string]any{"address": output},
			"simulation": map[string]any{"amountOutRaw": "100"},
		}}
	}
	pending := func(router string) map[string]any { return map[string]any{"router": router, "fetched": false} }
	var polls []map[string]any
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		switch method {
		case "GetSupportedMarkets":
			return map[string]any{"markets": []any{
				map[string]any{"name": "zeroex", "active": true, "supportedChainIds": []int{42161}},
				map[string]any{"name": "fynd", "active": true, "supportedChainIds": []int{42161}},
				map[string]any{"name": "akka", "active": true, "supportedChainIds": []int{42161}},
				map[string]any{"name": "basonly", "active": true, "supportedChainIds": []int{8453}},
			}}, true
		case "Create":
			params := body["params"].(map[string]any)
			if markets, _ := params["enabledMarkets"].([]any); len(markets) != 3 || params["useOkuRouter"] != true {
				t.Errorf("Create must send the chain's active markets and use the Oku Router: %v", params)
			}
			return map[string]any{"orderId": "01a15585-427e-70f2-a7e4-7457069b9c0c", "timeline": 1}, true
		case "GetFlowstreamQuotes":
			polls = append(polls, body)
			switch len(polls) {
			case 1:
				// Old timeline: must be discarded once the server reports timeline 2.
				return map[string]any{"timeline": 1, "quotes": map[string]any{"akka": quote("akka", "stale", "1000")}}, true
			case 2:
				// The server returns every router's current state, including unfinished ones.
				return map[string]any{"timeline": 2, "quotes": map[string]any{"zeroex": quote("zeroex", "q-zeroex", "10"), "fynd": pending("fynd"), "akka": pending("akka")}}, true
			default:
				return map[string]any{"timeline": 2, "quotes": map[string]any{"fynd": quote("fynd", "q-fynd", "20"), "akka": quote("akka", "q-akka", "5")}}, true
			}
		case "GetQuotes", "GetQuote":
			t.Errorf("quote collection must use GetFlowstreamQuotes, not %s", method)
		}
		return nil, false
	}
	h.rpcURL = decimalsRPC(t)
	r := h.run("swap", "quote", "--chain", "42161", "--from", input, "--to", output, "--amount", "10", "--recipient", agent, "--json")
	if r.code != 0 {
		t.Fatalf("swap quote: %s %s", r.stdout, r.stderr)
	}
	if len(polls) != 3 {
		t.Fatalf("polls = %d, want 3 (stop once every enabled market answered)", len(polls))
	}
	if polls[1]["timeline"] != float64(1) || polls[2]["timeline"] != float64(2) {
		t.Fatalf("timeline not followed: %v / %v", polls[1]["timeline"], polls[2]["timeline"])
	}
	if fetched, _ := polls[2]["fetchedRouters"].([]any); len(fetched) != 1 || fetched[0] != "zeroex" {
		t.Fatalf("third poll must report only fetched current-timeline routers: %v", polls[2]["fetchedRouters"])
	}
	data := r.data()
	if data["route"] != "fynd" || !strings.EqualFold(data["target"].(string), okuRouter) || !strings.EqualFold(data["recipient"].(string), common.HexToAddress(agent).Hex()) {
		t.Fatalf("best current-timeline route not selected: %v", data)
	}
}

// decimalsRPC answers the chain-ID check and the ERC-20 decimals() call (6).
func decimalsRPC(t *testing.T) string {
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_call":
			fmt.Fprint(w, `{"result":"0x0000000000000000000000000000000000000000000000000000000000000006"}`)
		default:
			t.Errorf("unexpected RPC %s during quote", req.Method)
		}
	}))
	t.Cleanup(rpc.Close)
	return rpc.URL
}

// The server may replace the requested markets with its own router set (e.g.
// DirectWrap for wraps). Once every router it reports is fetched, quoting must
// stop instead of waiting out the whole window for markets that will never answer.
func TestSwapQuoteStopsWhenServerSubstitutesRouters(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	input := "0x3333333333333333333333333333333333333333"
	output := "0x2222222222222222222222222222222222222222"
	polls := 0
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		switch method {
		case "GetSupportedMarkets":
			return map[string]any{"markets": []any{
				map[string]any{"name": "zeroex", "active": true, "supportedChainIds": []int{42161}},
				map[string]any{"name": "fynd", "active": true, "supportedChainIds": []int{42161}},
			}}, true
		case "Create":
			return map[string]any{"orderId": "01a15585-427e-70f2-a7e4-7457069b9c0c", "timeline": 1}, true
		case "GetFlowstreamQuotes":
			polls++
			return map[string]any{"timeline": 1, "quotes": map[string]any{"directwrap": map[string]any{"router": "directwrap", "fetched": true, "quoteId": "q-wrap", "quote": map[string]any{
				"chainId": 42161, "isExactIn": true, "inAmountRaw": "10000000", "minAmountOutRaw": "90", "outAmountRaw": "100", "netUsdValue": "10", "priceImpact": 0,
				"inToken": map[string]any{"address": input}, "outToken": map[string]any{"address": output},
				"simulation": map[string]any{"amountOutRaw": "100"},
			}}}}, true
		}
		return nil, false
	}
	h.rpcURL = decimalsRPC(t)
	r := h.run("swap", "quote", "--chain", "42161", "--from", input, "--to", output, "--amount", "10", "--recipient", h.fake.agentAddress, "--json")
	if r.code != 0 {
		t.Fatalf("swap quote: %s %s", r.stdout, r.stderr)
	}
	if polls != 1 {
		t.Fatalf("polls = %d, want 1 (all server-reported routers fetched)", polls)
	}
	if route := r.data()["route"]; route != "directwrap" {
		t.Fatalf("route = %v, want directwrap", route)
	}
}

// Swaps work on any chain with active Oku markets and a pinned OkuRouter, not
// a fixed list, and use that chain's router (Mantle's differs from the common
// address). A chain without any RPC fails before contacting the backend, and
// a chain without an official router is refused.
func TestSwapQuoteUsesPerChainRouter(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	input := "0x3333333333333333333333333333333333333333"
	output := "0x2222222222222222222222222222222222222222"
	backend := 0
	var chain int
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		backend++
		switch method {
		case "GetSupportedMarkets":
			return map[string]any{"markets": []any{map[string]any{"name": "zeroex", "active": true, "supportedChainIds": []int{5000, 324}}}}, true
		case "Create":
			return map[string]any{"orderId": "01a15585-427e-70f2-a7e4-7457069b9c0c", "timeline": 1}, true
		case "GetFlowstreamQuotes":
			return map[string]any{"timeline": 1, "quotes": map[string]any{"zeroex": map[string]any{"router": "zeroex", "fetched": true, "quoteId": "q", "quote": map[string]any{
				"chainId": chain, "isExactIn": true, "inAmountRaw": "10000000", "minAmountOutRaw": "90", "outAmountRaw": "100", "netUsdValue": "10", "priceImpact": 0,
				"inToken": map[string]any{"address": input}, "outToken": map[string]any{"address": output},
				"simulation": map[string]any{"amountOutRaw": "100"},
			}}}}, true
		}
		return nil, false
	}
	missing := h.run("swap", "quote", "--chain", "1101", "--from", input, "--to", output, "--amount", "10", "--json")
	if missing.errCode() != "CONFIG_ERROR" || !strings.Contains(missing.stdout, "OKU_RPC_URL_1101") || backend != 0 {
		t.Fatalf("chain without RPC must fail before the backend: %s (backend calls %d)", missing.stdout, backend)
	}
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprintf(w, `{"result":"0x%x"}`, chain)
		case "eth_call":
			fmt.Fprint(w, `{"result":"0x0000000000000000000000000000000000000000000000000000000000000006"}`)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	defer rpc.Close()
	h.rpcURL = rpc.URL
	mantleRouter := "0x86d61277970685075a0BF66023c219dc4A6e3F6d"
	for _, tc := range []struct {
		chain  int
		router string
		ok     bool
		why    string
	}{
		{5000, mantleRouter, true, "Mantle's own router"},
		{324, okuRouter, false, "zkSync has no official OkuRouter"},
	} {
		chain, h.rpcChain = tc.chain, fmt.Sprint(tc.chain)
		r := h.run("swap", "quote", "--chain", fmt.Sprint(tc.chain), "--from", input, "--to", output, "--amount", "10", "--json")
		if tc.ok != (r.code == 0) {
			t.Fatalf("%s: %s %s", tc.why, r.stdout, r.stderr)
		}
		if tc.ok && !strings.EqualFold(r.data()["target"].(string), tc.router) {
			t.Fatalf("%s: target %v", tc.why, r.data()["target"])
		}
	}
}
