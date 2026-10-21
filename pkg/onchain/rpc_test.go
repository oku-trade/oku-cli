package onchain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

func TestHTTPRPCRejectsUnverifiedReceiptsAndTransactions(t *testing.T) {
	var receipt string
	chain := "0xa4b1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch req.Method {
		case "eth_chainId":
			fmt.Fprintf(w, `{"result":%q}`, chain)
		case "eth_getTransactionReceipt":
			fmt.Fprintf(w, `{"result":%s}`, receipt)
		case "eth_blockNumber":
			fmt.Fprint(w, `{"result":"0x30"}`)
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"result":{"hash":%q}}`, hash)
		case "eth_getTransactionByHash":
			fmt.Fprintf(w, `{"result":{"hash":%q,"from":%q,"to":%q,"value":"0x1","input":"0x"}}`, hash, agent, master)
		default:
			t.Errorf("unexpected RPC method %s", req.Method)
		}
	}))
	defer server.Close()
	rpc := HTTPRPC{Client: server.Client(), Getenv: func(key string) string {
		if key == "OKU_RPC_URL_42161" {
			return server.URL
		}
		return ""
	}}
	receipt = `{"status":"0x1"}`
	if _, err := rpc.Receipt(context.Background(), 42161, hash); err == nil {
		t.Fatal("status-only receipt accepted")
	}
	receipt = fmt.Sprintf(`{"status":"0x1","transactionHash":%q,"blockHash":%q,"blockNumber":"0x10"}`, strings.Replace(hash, "aa", "bb", 1), hash)
	if _, err := rpc.Receipt(context.Background(), 42161, hash); err == nil {
		t.Fatal("wrong transaction hash accepted")
	}
	receipt = fmt.Sprintf(`{"status":"0x1","transactionHash":%q,"blockHash":%q,"blockNumber":"0x10"}`, hash, hash)
	chain = "0x1"
	if _, err := rpc.Receipt(context.Background(), 42161, hash); err == nil {
		t.Fatal("wrong RPC chain accepted")
	}
	chain = "0xa4b1"
	ok, err := rpc.Receipt(context.Background(), 42161, hash)
	if err != nil || ok == nil || !*ok {
		t.Fatalf("verified receipt: %v %v", ok, err)
	}
	if err := rpc.VerifyTransaction(context.Background(), 42161, hash, agent, journal.OnchainTemplate{ChainID: 42161, To: master, Value: "0x1"}); err != nil {
		t.Fatalf("valid recovered tx: %v", err)
	}
	if err := rpc.VerifyTransaction(context.Background(), 42161, hash, agent, journal.OnchainTemplate{ChainID: 42161, To: master, Value: "0x2"}); err == nil {
		t.Fatal("mismatched recovered value accepted")
	}
}

// An approve-then-swap intent cannot simulate the swap until the approval is
// mined (live failure: "ERC20: transfer amount exceeds allowance"). The reserve
// must cover the swap with a conservative limit, but a reverting first step
// is still a real error.
func TestEstimateReservesDependentStepsThatCannotSimulateYet(t *testing.T) {
	firstFails := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string           `json:"method"`
			Params []map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_gasPrice":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_feeHistory":
			fmt.Fprint(w, `{"error":{"message":"unsupported"}}`)
		case "eth_estimateGas":
			if req.Params[0]["to"] == "0xrouter" || firstFails {
				fmt.Fprint(w, `{"error":{"message":"execution reverted: ERC20: transfer amount exceeds allowance"}}`)
				return
			}
			fmt.Fprint(w, `{"result":"0x64"}`)
		default:
			t.Errorf("unexpected RPC method %s", req.Method)
		}
	}))
	defer server.Close()
	rpc := HTTPRPC{Client: server.Client(), Getenv: func(string) string { return server.URL }}
	steps := []journal.OnchainTemplate{{ChainID: 42161, To: "0xtoken", Value: "0x0", Data: "0x095ea7b3"}, {ChainID: 42161, To: "0xrouter", Value: "0x0", Data: "0x48430c16"}}
	reserve, err := rpc.Estimate(context.Background(), 42161, agent, steps)
	if err != nil {
		t.Fatalf("dependent swap step must not fail the reserve: %v", err)
	}
	// 130% of 100 for the approval plus 130% of the fallback limit, at 1 wei/gas.
	if want := int64(130 + dependentStepGas*13/10); reserve.Int64() != want {
		t.Fatalf("reserve = %s, want %d", reserve, want)
	}
	firstFails = true
	if _, err := rpc.Estimate(context.Background(), 42161, agent, steps); err == nil {
		t.Fatal("a reverting first step must fail preflight")
	}
}

// Any EVM chain resolves: Oku's eRPC gateway by chain-config internal name for
// known chains, an override for anything else (or to replace the default), and
// a clear error naming the variable when neither exists (zkSync is deprecated
// in chain-config, so it needs an override).
func TestRPCURLResolvesAnyChain(t *testing.T) {
	env := map[string]string{"OKU_RPC_URL_324": "https://zk.example", "OKU_RPC_URL_10": "https://op.example"}
	getenv := func(k string) string { return env[k] }
	for chain, want := range map[uint64]string{137: "https://rpc.invalid/polygon", 42161: "https://rpc.invalid/arbitrum", 10: "https://op.example", 324: "https://zk.example"} {
		if got, err := RPCURL(getenv, chain); err != nil || got != want {
			t.Fatalf("chain %d: %q %v", chain, got, err)
		}
	}
	if _, err := RPCURL(getenv, 1101); !errors.Is(err, ErrNoRPC) || !strings.Contains(err.Error(), "OKU_RPC_URL_1101") {
		t.Fatalf("missing RPC must name the override: %v", err)
	}
}

// Submit's receipt wait must outlast the confirmation depth: Polygon's 64
// blocks at ~2.5s exceed the old fixed 2 minutes, which returned
// OUTCOME_UNKNOWN for successful transactions.
func TestReceiptWaitCoversConfirmationDepth(t *testing.T) {
	none := func(string) string { return "" }
	for _, chain := range []uint64{1, 137, 56, 42161} {
		need := time.Duration(float64(confirmationDepth(chain))*blockSeconds(chain)) * time.Second
		if got := ReceiptWait(chain, none); got <= need || got < 2*time.Minute {
			t.Fatalf("chain %d: wait %s does not cover %s", chain, got, need)
		}
	}
	deep := func(k string) string {
		if k == "OKU_RPC_CONFIRMATIONS_42161" {
			return "300"
		}
		return ""
	}
	if ReceiptWait(42161, deep) <= ReceiptWait(42161, none) {
		t.Fatal("a confirmation override must lengthen the wait")
	}
}
