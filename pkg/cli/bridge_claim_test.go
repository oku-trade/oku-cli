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

func TestBridgeClaimFlow(t *testing.T) {
	for _, variant := range []string{"confirmed", "pending", "already"} {
		t.Run(variant, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			h.rpcChain = "8453"
			target := "0x5555555555555555555555555555555555555555"
			calls := 0
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				switch method {
				case "History":
					status := "ORDER_STATUS_AWAITING_CLAIM"
					if variant == "pending" {
						status = "ORDER_STATUS_PENDING"
					}
					return map[string]any{"history": []any{map[string]any{"orderId": "order", "status": status, "dstChain": "eip155:8453", "metadata": map[string]string{"claimTo": target, "claimCalldata": "0xdeadbeef"}}}}, true
				case "FinishClaim":
					if body["orderId"] != "order" || body["txHash"] != "0x"+strings.Repeat("a", 64) {
						t.Errorf("wrong finish claim %v", body)
					}
					return map[string]any{}, true
				}
				return nil, false
			}
			rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				switch req.Method {
				case "eth_chainId":
					fmt.Fprint(w, `{"result":"0x2105"}`)
				case "eth_getBalance":
					fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
				case "eth_gasPrice", "eth_maxPriorityFeePerGas":
					fmt.Fprint(w, `{"result":"0x1"}`)
				case "eth_feeHistory":
					fmt.Fprint(w, `{"result":{"baseFeePerGas":["0x1","0x1"]}}`)
				case "eth_estimateGas":
					if variant == "already" {
						fmt.Fprint(w, `{"error":{"code":-32000,"message":"execution reverted: Nonce already used"}}`)
					} else {
						fmt.Fprint(w, `{"result":"0x5208"}`)
					}
				case "eth_getTransactionReceipt":
					fmt.Fprint(w, `{"result":{"status":"0x1","transactionHash":"0x`+strings.Repeat("a", 64)+`","blockHash":"0x`+strings.Repeat("a", 64)+`","blockNumber":"0x10"}}`)
				case "eth_blockNumber":
					fmt.Fprint(w, `{"result":"0x30"}`)
				case "eth_getBlockByNumber":
					fmt.Fprint(w, `{"result":{"hash":"0x`+strings.Repeat("a", 64)+`"}}`)
				default:
					t.Errorf("unexpected rpc %s", req.Method)
				}
			}))
			defer rpc.Close()
			h.rpcURL = rpc.URL
			p := &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL}
			j, err := openJournal(p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = j.CreateOnchain(journal.OnchainIntent{ID: "claim-source", Kind: "bridge", ChainID: 42161, OutChain: 8453, Sender: h.fake.agentAddress, Recipient: h.fake.agentAddress, Token: "native", Amount: "100", OrderID: "order", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Templates: []journal.OnchainTemplate{{ChainID: 42161, To: target, Value: "0x0"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = j.AdvanceOnchain("claim-source", "prepared", "confirmed", "0xabc"); err != nil {
				t.Fatal(err)
			}
			j.Close()
			first := h.run("bridge", "claim", "--intent", "claim-source", "--json")
			if first.code != 0 {
				t.Fatalf("claim failed %s %s", first.stdout, first.stderr)
			}
			calls = h.privy.rpcCalls
			if variant == "confirmed" && calls != 1 {
				t.Fatalf("expected one transaction, got %d", calls)
			}
			if variant == "confirmed" {
				wire := h.privy.transactions[0]
				params := wire["params"].(map[string]any)
				tx := params["transaction"].(map[string]any)
				if wire["caip2"] != "eip155:8453" || tx["to"] != target || tx["data"] != "0xdeadbeef" || tx["value"] != "0x0" || tx["chain_id"] != float64(8453) {
					t.Fatalf("unexpected claim payload: %v", wire)
				}
			}
			if variant != "confirmed" && calls != 0 {
				t.Fatalf("unexpected transaction count %d", calls)
			}
			if variant == "already" && !strings.Contains(first.stdout, "already claimed") {
				t.Fatalf("unexpected response %s", first.stdout)
			}
			second := h.run("bridge", "claim", "--intent", "claim-source", "--json")
			if second.code != 0 || h.privy.rpcCalls != calls {
				t.Fatalf("idempotency failed: %s %s", second.stdout, second.stderr)
			}
		})
	}
}
