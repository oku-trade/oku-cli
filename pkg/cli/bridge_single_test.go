package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

func TestBridgeSingleBackendCallUsesExecute(t *testing.T) {
	for _, tc := range []struct {
		name, executionTarget string
		unsafeInfo            bool
	}{{name: "valid", executionTarget: "0x5555555555555555555555555555555555555555"}, {name: "missing-target"}, {name: "unsupported-shape", executionTarget: "0x5555555555555555555555555555555555555555", unsafeInfo: true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			target := "0x5555555555555555555555555555555555555555"
			data := "0xdeadbeef"
			h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
				switch method {
				case "UpdateQuoteSelect":
					return map[string]any{"bridge": "relay", "quoteId": "quote"}, true
				case "ConfirmQuoteSelect", "Finish":
					return map[string]any{}, true
				case "GetQuotes":
					return map[string]any{"quotes": map[string]any{"relay": map[string]any{"bridge": "relay", "quoteId": "quote", "quote": map[string]any{}}}}, true
				case "GetExecutionInfo":
					info := map[string]any{"trade": map[string]any{"chainId": "42161", "to": tc.executionTarget, "value": "0", "data": data}}
					if tc.unsafeInfo {
						info["depositAddress"] = target
					}
					return map[string]any{"info": info}, true
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
					fmt.Fprint(w, `{"result":"0xa4b1"}`)
				case "eth_getCode":
					t.Error("bridge target code must not be checked")
					fmt.Fprint(w, `{"result":"0x"}`)
				case "eth_getBalance":
					fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
				case "eth_gasPrice", "eth_maxPriorityFeePerGas":
					fmt.Fprint(w, `{"result":"0x1"}`)
				case "eth_feeHistory":
					fmt.Fprint(w, `{"result":{"baseFeePerGas":["0x1","0x1"]}}`)
				case "eth_estimateGas":
					fmt.Fprint(w, `{"result":"0x5208"}`)
				case "eth_call":
					fmt.Fprint(w, `{"result":"0xffffffffffffffff"}`)
				case "eth_getTransactionReceipt":
					fmt.Fprint(w, `{"result":{"status":"0x1","transactionHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","blockHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","blockNumber":"0x10"}}`)
				case "eth_blockNumber":
					fmt.Fprint(w, `{"result":"0x30"}`)
				case "eth_getBlockByNumber":
					fmt.Fprint(w, `{"result":{"hash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
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
			record, err := j.CreateOnchain(journal.OnchainIntent{ID: "bridge-single", Kind: "bridge", ChainID: 42161, OutChain: 8453, Sender: h.fake.agentAddress, Recipient: h.fake.agentAddress, Token: "native", OutToken: "0x2222222222222222222222222222222222222222", Amount: "100", OrderID: "order", QuoteID: "quote", Generation: 7, Route: "relay", MinOut: "1", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpiresAt: time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano), Templates: []journal.OnchainTemplate{{ChainID: 42161, Value: "0x0"}}})
			if err != nil {
				t.Fatal(err)
			}
			j.Close()
			result := h.run("bridge", "submit", "--intent", record.ID, "--digest", record.Digest, "--json")
			if tc.name != "valid" {
				if result.errCode() != "PAYLOAD_MISMATCH" || h.privy.rpcCalls != 0 {
					t.Fatalf("unsafe authenticated bridge execution must be refused before signing: %s; sends=%d", result.stdout, h.privy.rpcCalls)
				}
				return
			}
			if result.code != 0 || result.data()["state"] != "confirmed" || h.privy.rpcCalls != 1 || len(h.privy.batches) != 0 {
				t.Fatalf("single-call bridge should use Execute: %s %s; sends=%d batches=%d", result.stdout, result.stderr, h.privy.rpcCalls, len(h.privy.batches))
			}
		})
	}
}
