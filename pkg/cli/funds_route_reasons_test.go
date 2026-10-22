package cli

import (
	"strings"
	"testing"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
)

func TestSwapRejectReasons(t *testing.T) {
	q := routeQuote{Chain: 42161, InToken: arbitrumUSDC, OutToken: "native", InAmount: "1000000"}
	cases := map[string]*v2.SimpleSwapQuoteInformation{
		"not_answered":                        nil,
		"backend_error: ":                     {Fetched: true, Error: &v2.OrderError{Message: strings.Repeat("x", 500)}},
		"not_fetched":                         {},
		"chain_mismatch":                      {Fetched: true, Quote: &v2.SimpleSwapQuote{ChainId: 1}},
		"not_exact_in":                        {Fetched: true, Quote: &v2.SimpleSwapQuote{ChainId: 42161}},
		"intent_excluded":                     {Fetched: true, Quote: &v2.SimpleSwapQuote{IntentBased: true}},
		"amount_mismatch: got 5 want 1000000": {Fetched: true, Quote: &v2.SimpleSwapQuote{ChainId: 42161, IsExactIn: true, IntentBased: true, InAmountRaw: "5"}},
	}
	for want, entry := range cases {
		got := swapRejectReason(entry, q, want == "intent_excluded")
		if !strings.HasPrefix(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
		if len(got) > 230 {
			t.Errorf("reason not truncated: %d chars", len(got))
		}
	}
}

func TestRouteRejectionRetryable(t *testing.T) {
	filtered := (&routeRejection{reasons: map[string]string{"zeroex": "amount_mismatch: got 1 want 2", "icecreamswap": "not_answered"}, answered: true, attempts: 2}).error("")
	if !filtered.Retryable || !strings.Contains(filtered.Message, "after 2 attempts") || !strings.Contains(filtered.Message, "icecreamswap: not_answered; zeroex: amount_mismatch") {
		t.Fatalf("transient rejection: %+v", filtered)
	}
	if routes, _ := filtered.Details["routes"].(map[string]string); routes["zeroex"] == "" {
		t.Fatalf("routes detail missing: %v", filtered.Details)
	}
	forced := (&routeRejection{reasons: map[string]string{"cowswap": "backend_error: below minimum"}, attempts: 1}).error("CowSwap")
	if forced.Retryable || forced.Message != "no eligible route: CowSwap: below minimum" {
		t.Fatalf("forced backend rejection: %+v", forced)
	}
}
