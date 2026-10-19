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

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain/onchaintest"
)

// A swap that needs an approval must go out as one atomic wallet_sendCalls
// batch [approve, swap] (live failure: the approval confirmed but the swap was
// never sent). The batch is found on chain by the agent nonce recorded before
// submission and confirmed from its receipt; eth_sendTransaction is never used.
func TestSwapSubmitBatchesApprovalAndSwap(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	agent := h.fake.agentAddress
	input := common.HexToAddress("0x3333333333333333333333333333333333333333")
	output := common.HexToAddress("0x2222222222222222222222222222222222222222")
	target := common.HexToAddress("0x5555555555555555555555555555555555555555")
	parsed, err := abi.JSON(strings.NewReader(routerABI))
	if err != nil {
		t.Fatal(err)
	}
	amount := big.NewInt(1000000)
	packed, err := parsed.Pack("fillQuoteTokenToToken", input, output, target, target, []byte{1}, amount, big.NewInt(0), common.HexToAddress(agent), testWarrant{big.NewInt(0), big.NewInt(0), big.NewInt(0), common.Address{}, []byte{}})
	if err != nil {
		t.Fatal(err)
	}
	swapData := fmt.Sprintf("0x%x", packed)
	approveData := "0x095ea7b3" + strings.Repeat("0", 24) + strings.ToLower(okuRouter[2:]) + fmt.Sprintf("%064x", amount)
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		switch method {
		case "UpdateQuoteSelect":
			return map[string]any{"router": "route", "quoteId": "quote"}, true
		case "ConfirmQuoteSelect", "Finish":
			return map[string]any{}, true
		case "GetQuotes":
			return map[string]any{"quotes": map[string]any{"route": map[string]any{"router": "route", "quoteId": "quote", "quote": map[string]any{
				"minAmountOutRaw": "100", "simulation": map[string]any{"amountOutRaw": "100"},
				"approvalsV2": []any{map[string]any{"to": input.Hex(), "functionName": "approve", "values": map[string]any{"spender": okuRouter, "amount": "1000000"}}},
			}}}}, true
		case "GetExecutionInfo":
			return map[string]any{"info": map[string]any{"trade": map[string]any{"chainId": "42161", "to": okuRouter, "value": "0", "data": swapData}}}, true
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
			// Block 0x40 at submission; the batch mines in 0x45; head moves to 0x60.
			if sent() {
				fmt.Fprint(w, `{"result":"0x60"}`)
				return
			}
			fmt.Fprint(w, `{"result":"0x40"}`)
		case "eth_getTransactionCount":
			// Nonce 5 before the batch; 6 from block 0x45 once sent.
			n := "0x5"
			tag, _ := req.Params[1].(string)
			block, ok := new(big.Int).SetString(strings.TrimPrefix(tag, "0x"), 16)
			if sent() && (tag == "latest" || tag == "pending" || ok && block.Int64() >= 0x45) {
				n = "0x6"
			}
			fmt.Fprintf(w, `{"result":%q}`, n)
		case "eth_getBlockByNumber":
			if full, _ := req.Params[1].(bool); full {
				input := onchaintest.KernelBatch([]onchaintest.Call{{To: input.Hex(), Data: approveData}, {To: okuRouter, Data: swapData}})
				fmt.Fprintf(w, `{"result":{"transactions":[{"hash":%q,"from":%q,"to":%q,"input":%q,"nonce":"0x5"}]}}`, batchHash, agent, agent, input)
				return
			}
			fmt.Fprintf(w, `{"result":{"hash":%q}}`, batchHash)
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
	rec, err := j.CreateOnchain(journal.OnchainIntent{ID: "swap-batch", Kind: "swap", ChainID: 42161, OutChain: 42161, Sender: agent, Recipient: agent, Token: input.Hex(), OutToken: output.Hex(), Amount: amount.String(), OrderID: "order", QuoteID: "quote", Route: "route", Target: okuRouter, Spender: okuRouter, MinOut: "100", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpiresAt: time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano), Templates: []journal.OnchainTemplate{{ChainID: 42161, To: okuRouter, Value: "0x0"}}})
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	r := h.run("swap", "submit", "--intent", rec.ID, "--digest", rec.Digest, "--json")
	if r.code != 0 || r.data()["state"] != "confirmed" {
		t.Fatalf("swap submit: %s %s", r.stdout, r.stderr)
	}
	if len(h.privy.batches) != 1 || h.privy.rpcCalls != 1 {
		t.Fatalf("want exactly one wallet_sendCalls and no other sends: batches=%d rpc=%d", len(h.privy.batches), h.privy.rpcCalls)
	}
	calls := h.privy.batches[0]
	if len(calls) != 2 || !strings.EqualFold(calls[0]["to"], input.Hex()) || calls[0]["data"] != approveData || !strings.EqualFold(calls[1]["to"], okuRouter) || calls[1]["data"] != swapData {
		t.Fatalf("batch must be [approve exact amount to router, verified swap]: %v", calls)
	}
	if hashes, _ := r.data()["tx_hashes"].([]any); len(hashes) != 1 || hashes[0] != batchHash {
		t.Fatalf("batch hash not recorded: %v", r.data()["tx_hashes"])
	}
}
