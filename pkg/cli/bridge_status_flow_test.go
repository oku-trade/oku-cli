package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

func TestBridgeStatusReportsBackendAndAcrossWithoutChangingSource(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		if method == "Ping" {
			return map[string]any{"orderId": "order", "status": "confirmed", "stage": "done"}, true
		}
		return nil, false
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("depositTxHash") != "0xabc" {
			t.Errorf("wrong deposit hash: %s", r.URL)
		}
		fmt.Fprint(w, `{"status":"filled","fillTx":"0xdef"}`)
	}))
	defer api.Close()
	h.acrossAPI = api.URL
	p := &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL}
	j, err := openJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.CreateOnchain(journal.OnchainIntent{ID: "bridge-status", Kind: "bridge", ChainID: 42161, OutChain: 8453, Sender: h.fake.agentAddress, Recipient: h.fake.agentAddress, Token: "native", OutToken: "0x2222222222222222222222222222222222222222", Amount: "100", OrderID: "order", QuoteID: "quote", Route: "across", Target: "0x5555555555555555555555555555555555555555", MinOut: "1", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpiresAt: time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano), Templates: []journal.OnchainTemplate{{ChainID: 42161, To: "0x5555555555555555555555555555555555555555", Value: "0x0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = j.AdvanceOnchain("bridge-status", "prepared", "confirmed", "0xabc"); err != nil {
		t.Fatal(err)
	}
	j.Close()
	run := func(want string) {
		t.Helper()
		result := h.run("bridge", "status", "--intent", "bridge-status", "--json")
		if result.code != 0 {
			t.Fatalf("status: %s %s", result.stdout, result.stderr)
		}
		data := result.data()
		source := data["source"].(map[string]any)
		backend := data["backend"].(map[string]any)
		fill := data["destination_fill"].(map[string]any)
		if source["state"] != "confirmed" || backend["order_id"] != "order" || fill["status"] != want || data["destination_settled"] != false {
			t.Fatalf("source changed or destination incorrect: %v", data)
		}
	}
	run("filled")
	api.Close()
	run("unknown")
}
