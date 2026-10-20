package cli

import (
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

// Destination delivery for any bridge comes from Oku's order history, the same
// source as the web app: a completed order with a destination hash is filled,
// awaiting_claim surfaces the claim hint, and the source journal state never
// changes. History unavailable leaves destination unknown.
func TestBridgeStatusDestinationFromHistory(t *testing.T) {
	for _, tc := range []struct {
		name, status, dstHash, want string
		claim                       bool
		historyDown                 bool
	}{
		{"filled", "ORDER_STATUS_COMPLETED", "0xdef", "filled", false, false},
		{"awaiting claim", "ORDER_STATUS_AWAITING_CLAIM", "", "awaiting_claim", true, false},
		{"pending", "ORDER_STATUS_PENDING", "", "pending", false, false},
		{"refunded", "ORDER_STATUS_REFUNDED", "", "refunded", false, false},
		{"history down", "", "", "unknown", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
				switch method {
				case "History":
					if tc.historyDown {
						return nil, false
					}
					return map[string]any{"history": []any{map[string]any{"orderId": "order", "status": tc.status, "dstTxHash": tc.dstHash}}}, true
				case "Ping":
					return map[string]any{"orderId": "order", "status": "running", "stage": "executing"}, true
				}
				return nil, false
			}
			p := &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL}
			j, err := openJournal(p)
			if err != nil {
				t.Fatal(err)
			}
			// A Circle route: destination must not depend on any provider API.
			_, err = j.CreateOnchain(journal.OnchainIntent{ID: "bridge-status", Kind: "bridge", ChainID: 42161, OutChain: 8453, Sender: h.fake.agentAddress, Recipient: h.fake.agentAddress, Token: "native", OutToken: "0x2222222222222222222222222222222222222222", Amount: "100", OrderID: "order", QuoteID: "quote", Route: "circleStandard", Target: "0x5555555555555555555555555555555555555555", MinOut: "1", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpiresAt: time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano), Templates: []journal.OnchainTemplate{{ChainID: 42161, To: "0x5555555555555555555555555555555555555555", Value: "0x0"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = j.AdvanceOnchain("bridge-status", "prepared", "confirmed", "0xabc"); err != nil {
				t.Fatal(err)
			}
			j.Close()
			result := h.run("bridge", "status", "--intent", "bridge-status", "--json")
			if result.code != 0 {
				t.Fatalf("status: %s %s", result.stdout, result.stderr)
			}
			data := result.data()
			fill := data["destination_fill"].(map[string]any)
			if data["source"].(map[string]any)["state"] != "confirmed" || fill["status"] != tc.want || data["claim_required"] != tc.claim || data["destination_settled"] != (tc.want == "filled") {
				t.Fatalf("destination: %v", data)
			}
			if tc.want == "filled" && fill["fill_tx"] != tc.dstHash {
				t.Fatalf("fill tx: %v", fill)
			}
		})
	}
}
