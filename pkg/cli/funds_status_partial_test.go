package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

// Live failure: submit timed out after the approval confirmed but before the
// swap broadcast, leaving the intent "unknown" and blocking every later
// onchain intent. Status must close it once the agent has no later mined or
// pending transaction, and must keep it unresolved while one exists.
func TestSwapStatusClosesPartialIntentOnlyWhenNothingLaterIsInFlight(t *testing.T) {
	approval := "0x" + strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name    string
		pending string
		want    string
	}{
		{"quiescent", "0x3", "failed"},
		{"later tx pending", "0x4", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			agent := h.fake.agentAddress
			rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
					Params []any  `json:"params"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				switch req.Method {
				case "eth_chainId":
					fmt.Fprint(w, `{"result":"0xa4b1"}`)
				case "eth_getTransactionReceipt":
					fmt.Fprintf(w, `{"result":{"status":"0x1","transactionHash":%q,"blockHash":%q,"blockNumber":"0x10"}}`, approval, approval)
				case "eth_blockNumber":
					fmt.Fprint(w, `{"result":"0x30"}`)
				case "eth_getBlockByNumber":
					fmt.Fprintf(w, `{"result":{"hash":%q}}`, approval)
				case "eth_getTransactionByHash":
					fmt.Fprintf(w, `{"result":{"from":%q,"nonce":"0x2"}}`, agent)
				case "eth_getTransactionCount":
					if req.Params[1] == "pending" {
						fmt.Fprintf(w, `{"result":%q}`, tc.pending)
						return
					}
					fmt.Fprint(w, `{"result":"0x3"}`)
				default:
					t.Errorf("unexpected RPC %s", req.Method)
				}
			}))
			defer rpc.Close()
			h.rpcURL = rpc.URL
			h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
				if method == "Ping" {
					return map[string]any{"orderId": "order"}, true
				}
				return nil, false
			}
			p := &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL}
			j, err := openJournal(p)
			if err != nil {
				t.Fatal(err)
			}
			steps := []journal.OnchainTemplate{{ChainID: 42161, To: arbitrumUSDC, Value: "0x0"}, {ChainID: 42161, To: okuRouter, Value: "0x0"}}
			rec, err := j.CreateOnchain(journal.OnchainIntent{ID: "swap-partial", Kind: "swap", ChainID: 42161, Sender: agent, Recipient: agent, Token: arbitrumUSDC, Amount: "1000000", OrderID: "order", Templates: steps, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range []struct{ from, to, hash string }{{"sending", "sent", approval}, {"sent", "sending", ""}, {"sending", "unknown", ""}} {
				if step.from == "sending" && step.to == "sent" {
					if err = j.BeginOnchain(rec.ID, rec.Digest, agent); err != nil {
						t.Fatal(err)
					}
				}
				if err = j.AdvanceOnchain(rec.ID, step.from, step.to, step.hash); err != nil {
					t.Fatal(err)
				}
			}
			j.Close()
			r := h.run("swap", "status", "--intent", rec.ID, "--json")
			if r.code != 0 {
				t.Fatalf("status: %s %s", r.stdout, r.stderr)
			}
			source := r.data()["source"].(map[string]any)
			if source["state"] != tc.want {
				t.Fatalf("state = %v, want %s", source["state"], tc.want)
			}
		})
	}
}
