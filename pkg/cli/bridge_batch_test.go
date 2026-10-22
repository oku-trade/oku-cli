package cli

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/onchain/onchaintest"
)

func TestBridgeSubmitBatchesApprovalAndDeposit(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	agent := h.fake.agentAddress
	token := common.HexToAddress("0xaf88d065e77c8cc2239327c5edb3a432268e5831")
	output := common.HexToAddress("0x833589fcd6edb6e08f4c7c32d4f71b54bda02913")
	target := "0x5555555555555555555555555555555555555555"
	amount := big.NewInt(500000)
	depositData := "0xdeadbeef"
	approveData := "0x095ea7b3" + strings.Repeat("0", 24) + strings.ToLower(target[2:]) + fmt.Sprintf("%064x", amount)
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		switch method {
		case "UpdateQuoteSelect":
			return map[string]any{"bridge": "relay", "quoteId": "quote"}, true
		case "ConfirmQuoteSelect", "Finish":
			return map[string]any{}, true
		case "GetQuotes":
			if body["generation"] != float64(7) {
				t.Errorf("missing prepared generation: %v", body)
			}
			approval := map[string]any{"to": token.Hex(), "functionName": "approve", "values": map[string]any{"spender": target, "amount": "500000"}}
			quote := map[string]any{"inChainId": 42161, "outChainId": 8453, "minAmountOut": "0.495817", "outToken": map[string]any{"address": output.Hex(), "decimals": 6}, "approvals": []any{approval}}
			entry := map[string]any{"bridge": "relay", "quoteId": "quote", "quote": quote}
			return map[string]any{"quotes": map[string]any{"relay": entry}}, true
		case "GetExecutionInfo":
			return map[string]any{"info": map[string]any{"trade": map[string]any{"chainId": "42161", "to": target, "value": "0", "data": depositData}}}, true
		case "GetQuote":
			t.Error("bridge submit used unreliable GetQuote")
		}
		return nil, false
	}
	batchHash := "0x" + strings.Repeat("bb", 32)
	sent := func() bool { return len(h.privy.batches) > 0 }
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_getCode":
			// Only the 7702 capability gate may read code: the agent (EOA) and
			// the Privy Kernel implementation. Bridge targets are not checked.
			addr, _ := req.Params[0].(string)
			switch {
			case strings.EqualFold(addr, agent):
				fmt.Fprint(w, `{"result":"0x"}`)
			case strings.EqualFold(addr, onchain.PrivyKernelImplementation):
				fmt.Fprint(w, `{"result":"0x6080"}`)
			default:
				t.Errorf("bridge submit must not check target contract code (%s)", addr)
				fmt.Fprint(w, `{"result":"0x"}`)
			}
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
		case "eth_blockNumber":
			if sent() {
				fmt.Fprint(w, `{"result":"0x60"}`)
			} else {
				fmt.Fprint(w, `{"result":"0x40"}`)
			}
		case "eth_getTransactionCount":
			n := "0x5"
			tag, _ := req.Params[1].(string)
			block, ok := new(big.Int).SetString(strings.TrimPrefix(tag, "0x"), 16)
			if sent() && (tag == "latest" || tag == "pending" || ok && block.Int64() >= 0x45) {
				n = "0x6"
			}
			fmt.Fprintf(w, `{"result":%q}`, n)
		case "eth_getBlockByNumber":
			if full, _ := req.Params[1].(bool); full {
				input := onchaintest.KernelBatch([]onchaintest.Call{{To: token.Hex(), Data: approveData}, {To: target, Data: depositData}})
				fmt.Fprintf(w, `{"result":{"transactions":[{"hash":%q,"from":%q,"to":%q,"input":%q,"nonce":"0x5"}]}}`, batchHash, agent, agent, input)
			} else {
				fmt.Fprintf(w, `{"result":{"hash":%q}}`, batchHash)
			}
		case "eth_getTransactionReceipt":
			fmt.Fprintf(w, `{"result":{"status":"0x1","transactionHash":%q,"blockHash":%q,"blockNumber":"0x45"}}`, batchHash, batchHash)
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
	rec, err := j.CreateOnchain(journal.OnchainIntent{ID: "bridge-batch", Kind: "bridge", ChainID: 42161, OutChain: 8453, Sender: agent, Recipient: agent, Token: token.Hex(), OutToken: output.Hex(), Amount: amount.String(), OrderID: "order", QuoteID: "quote", Generation: 7, Route: "relay", Target: target, Spender: target, MinOut: "495817", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpiresAt: time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano), Templates: []journal.OnchainTemplate{{ChainID: 42161, To: target, Value: "0x0"}}})
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	result := h.run("bridge", "submit", "--intent", rec.ID, "--digest", rec.Digest, "--json")
	if result.code != 0 || result.data()["state"] != "confirmed" {
		t.Fatalf("bridge submit: %s %s", result.stdout, result.stderr)
	}
	if len(h.privy.batches) != 1 || h.privy.rpcCalls != 1 {
		t.Fatalf("expected one wallet_sendCalls and no other sends: batches=%d rpc=%d", len(h.privy.batches), h.privy.rpcCalls)
	}
	calls := h.privy.batches[0]
	if len(calls) != 2 || !strings.EqualFold(calls[0]["to"], token.Hex()) || calls[0]["data"] != approveData || !strings.EqualFold(calls[1]["to"], target) || calls[1]["data"] != depositData {
		t.Fatalf("batch must be [exact approve, verified deposit]: %v", calls)
	}
	if hashes, _ := result.data()["tx_hashes"].([]any); len(hashes) != 1 || hashes[0] != batchHash {
		t.Fatalf("nonce-recovered batch hash missing: %v", result.data())
	}
}
