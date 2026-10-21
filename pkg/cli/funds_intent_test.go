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

// This exercises the actual CLI, Privy RPC, allowance RPC, journal and backend
// relay. A transaction intent would fail because it requires OkuRouter calldata.
func TestSwapBridgeIntentSubmitAndHistory(t *testing.T) {
	for _, tc := range []struct {
		kind                  string
		sufficient, ambiguous bool
	}{{"swap", false, false}, {"bridge", false, false}, {"swap", true, false}, {"bridge", true, false}, {"swap", true, true}} {
		kind := tc.kind
		name := kind
		if tc.sufficient {
			name += "-sufficient-allowance"
		}
		if tc.ambiguous {
			name += "-ambiguous-relay"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			token := "0x3333333333333333333333333333333333333333"
			out := "0x2222222222222222222222222222222222222222"
			relayer := "0x4444444444444444444444444444444444444444"
			typed := map[string]any{"domain": map[string]any{"name": "CoW Protocol", "chainId": 42161}, "types": map[string]any{"Order": []any{map[string]any{"name": "sellToken", "type": "address"}}}, "primaryType": "Order", "message": map[string]any{"sellToken": token}}
			var relayCalls int
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				switch method {
				case "UpdateQuoteSelect":
					return map[string]any{"quoteId": "quote", "router": "route", "bridge": "route"}, true
				case "ConfirmQuoteSelect":
					return map[string]any{}, true
				case "GetQuotes":
					q := map[string]any{"intentBased": true, "signingRequest": map[string]any{"typedData": []any{map[string]any{"payload": typed}}}, "inToken": map[string]any{"address": token}, "outToken": map[string]any{"address": out}, "approvalsV2": []any{map[string]any{"type": "APPROVAL_TYPE_ERC20", "to": token, "functionName": "approve", "args": []string{"spender", "amount"}, "values": map[string]any{"spender": relayer, "amount": "1000000"}}}}
					if kind == "swap" {
						q["chainId"] = 42161
						q["isExactIn"] = true
						q["inAmountRaw"] = "1000000"
						q["minAmountOutRaw"] = "100"
						return map[string]any{"quotes": map[string]any{"route": map[string]any{"router": "route", "quoteId": "quote", "quote": q}}}, true
					}
					q["inChainId"] = 42161
					q["outChainId"] = 8453
					q["approvals"] = q["approvalsV2"]
					return map[string]any{"quotes": map[string]any{"route": map[string]any{"bridge": "route", "quoteId": "quote", "quote": q}}}, true
				case "GetExecutionInfo":
					relayCalls++
					request, _ := body["request"].(map[string]any)
					signing, _ := request["signingRequest"].(map[string]any)
					entries, _ := signing["typedData"].([]any)
					if len(entries) != 1 || entries[0].(map[string]any)["signature"] == "" {
						t.Errorf("relay missing signature: %v", body)
					}
					if tc.ambiguous {
						return map[string]any{}, true
					}
					return map[string]any{"info": map[string]any{"orderId": "solver-order"}}, true
				case "Ping":
					return map[string]any{"orderId": "order"}, true
				case "History":
					return map[string]any{"history": []any{map[string]any{"orderId": "solver-order", "status": "ORDER_STATUS_COMPLETED", "txHash": "0xabc", "dstTxHash": "0xdef", "metadata": map[string]string{"explorerUrl": "https://example.test/order", "okuOrderId": "order"}}}}, true
				}
				return nil, false
			}
			hash := "0x" + strings.Repeat("a", 64)
			rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
					Params []any  `json:"params"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				switch req.Method {
				case "eth_chainId":
					fmt.Fprint(w, `{"result":"0xa4b1"}`)
				case "eth_call":
					if tc.sufficient {
						fmt.Fprint(w, `{"result":"0xf4240"}`)
					} else {
						fmt.Fprint(w, `{"result":"0x0"}`)
					}
				case "eth_getBalance":
					fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
				case "eth_gasPrice", "eth_maxPriorityFeePerGas":
					fmt.Fprint(w, `{"result":"0x1"}`)
				case "eth_feeHistory":
					fmt.Fprint(w, `{"result":{"baseFeePerGas":["0x1","0x1"]}}`)
				case "eth_estimateGas":
					fmt.Fprint(w, `{"result":"0x5208"}`)
				case "eth_blockNumber":
					fmt.Fprint(w, `{"result":"0x60"}`)
				case "eth_getBlockByNumber":
					fmt.Fprintf(w, `{"result":{"hash":%q}}`, hash)
				case "eth_getTransactionReceipt":
					fmt.Fprintf(w, `{"result":{"status":"0x1","transactionHash":%q,"blockHash":%q,"blockNumber":"0x45"}}`, hash, hash)
				default:
					t.Errorf("unexpected RPC %s", req.Method)
				}
			}))
			defer rpc.Close()
			h.rpcURL = rpc.URL
			p := &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL}
			j, err := openJournal(p)
			if err != nil {
				t.Fatal(err)
			}
			dst := uint64(42161)
			if kind == "bridge" {
				dst = 8453
			}
			rec, err := j.CreateOnchain(journal.OnchainIntent{ID: kind + "-intent", Kind: kind, Intent: true, ChainID: 42161, OutChain: dst, Sender: h.fake.agentAddress, Recipient: h.fake.agentAddress, Token: token, OutToken: out, Amount: "1000000", OrderID: "order", QuoteID: "quote", Route: "route", MinOut: "100", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
			if err != nil {
				t.Fatal(err)
			}
			j.Close()
			result := h.run(kind, "submit", "--intent", rec.ID, "--digest", rec.Digest, "--json")
			if !tc.ambiguous && (result.code != 0 || result.data()["state"] != "submitted") {
				t.Fatalf("submit: %s %s", result.stdout, result.stderr)
			}
			if tc.ambiguous {
				if result.code == 0 {
					t.Fatalf("empty relay reply accepted: %s", result.stdout)
				}
				j, err := openJournal(p)
				if err != nil {
					t.Fatal(err)
				}
				pending, err := j.GetOnchain(rec.ID)
				j.Close()
				if err != nil || pending.State != "unknown" {
					t.Fatalf("ambiguous relay not durable: %v %v", pending, err)
				}
			}
			wantTransactions := 1
			if tc.sufficient {
				wantTransactions = 0
			}
			if !tc.ambiguous && result.data()["backend_order_id"] != "solver-order" || relayCalls != 1 || len(h.privy.typedRequests) != 1 || len(h.privy.transactions) != wantTransactions || len(h.privy.batches) != 0 {
				t.Fatalf("unexpected intent execution: %v relay=%d typed=%d tx=%d batches=%d", result.data(), relayCalls, len(h.privy.typedRequests), len(h.privy.transactions), len(h.privy.batches))
			}
			retry := h.run(kind, "submit", "--intent", rec.ID, "--digest", rec.Digest, "--json")
			if retry.code == 0 || relayCalls != 1 {
				t.Fatalf("intent replay permitted: %s", retry.stdout)
			}
			status := h.run(kind, "status", "--intent", rec.ID, "--json")
			if status.code != 0 || status.data()["status"] != "filled" || status.data()["intent_order_id"] != "solver-order" || status.data()["explorer_url"] != "https://example.test/order" {
				t.Fatalf("intent history fill: %s %s", status.stdout, status.stderr)
			}
		})
	}
}
