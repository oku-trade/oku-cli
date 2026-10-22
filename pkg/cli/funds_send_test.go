package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDepositPrepareRejectsOtherRecipient(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	r := h.run("deposit", "prepare", "--amount", "5", "--to", "0x3333333333333333333333333333333333333333", "--json")
	if r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("expected master-only refusal: %s", r.stdout)
	}
}
func TestSendPrepareRejectsHyperliquidBridge(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	r := h.run("send", "prepare", "--chain", "42161", "--token", "native", "--amount", "0.01", "--to", "0x2Df1c51E09aECF9cacB7bc98cB1742757f163dF7", "--json")
	if r.errCode() != "INVALID_ARGUMENT" || h.privy.rpcCalls != 0 {
		t.Fatalf("bridge recipient accepted: %s", r.stdout)
	}
}
func TestSendSubmitRejectsWrongDigestBeforeSigning(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	prepared := h.run("send", "prepare", "--chain", "42161", "--token", "native", "--amount", "0.01", "--json")
	if prepared.code != 0 {
		t.Fatalf("prepare: %s", prepared.stdout)
	}
	id := prepared.data()["intent_id"].(string)
	refused := h.run("send", "submit", "--intent", id, "--digest", "wrong", "--json")
	if refused.errCode() != "PAYLOAD_MISMATCH" || h.privy.rpcCalls != 0 {
		t.Fatalf("digest mismatch was not refused before Privy RPC: %s", refused.stdout)
	}
}
func TestSendSubmitPersistsBeforeReceiptAndReconciles(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	prepared := h.run("send", "prepare", "--chain", "42161", "--token", "native", "--amount", "0.01", "--json")
	if prepared.code != 0 {
		t.Fatalf("prepare: %s", prepared.stdout)
	}
	id, digest := prepared.data()["intent_id"].(string), prepared.data()["digest"].(string)
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		switch request.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_estimateGas":
			fmt.Fprint(w, `{"result":"0x5208"}`)
		case "eth_feeHistory":
			fmt.Fprint(w, `{"result":{"baseFeePerGas":["0x1","0x1"]}}`)
		case "eth_maxPriorityFeePerGas":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_blockNumber":
			fmt.Fprint(w, `{"result":"0x30"}`)
		case "eth_getBlockByNumber":
			fmt.Fprint(w, `{"result":{"hash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
		case "eth_getBalance":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0xde0b6b3a7640000"}`)
		case "eth_gasPrice":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
		case "eth_getTransactionReceipt":
			p := &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL}
			j, err := openJournal(p)
			if err != nil {
				t.Error(err)
				return
			}
			rec, err := j.GetOnchain(id)
			j.Close()
			if err != nil || rec.State != "sent" || len(rec.Hashes) != 1 {
				t.Errorf("receipt observed before durable hash: %+v %v", rec, err)
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"status":"0x1","transactionHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","blockHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","blockNumber":"0x10"}}`)
		default:
			t.Errorf("unexpected RPC %s", request.Method)
		}
	}))
	defer rpc.Close()
	h.rpcURL = rpc.URL
	submitted := h.run("send", "submit", "--intent", id, "--digest", digest, "--json")
	if submitted.code != 0 || submitted.data()["state"] != "confirmed" || h.privy.rpcCalls != 1 {
		t.Fatalf("send result=%s calls=%d", submitted.stdout, h.privy.rpcCalls)
	}
	status := h.run("send", "status", "--intent", id, "--json")
	if status.code != 0 || status.data()["state"] != "confirmed" {
		t.Fatalf("status: %s", status.stdout)
	}
}
func TestSendUnknownBlocksNextSubmission(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.privy.failSign = true
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_estimateGas":
			fmt.Fprint(w, `{"result":"0x5208"}`)
		case "eth_feeHistory":
			fmt.Fprint(w, `{"result":{"baseFeePerGas":["0x1","0x1"]}}`)
		case "eth_maxPriorityFeePerGas":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_getBalance":
			fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
		case "eth_gasPrice":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_getBlockByNumber":
			fmt.Fprint(w, `{"result":{"baseFeePerGas":"0x1"}}`)
		default:
			t.Errorf("unexpected RPC %s", request.Method)
		}
	}))
	defer rpc.Close()
	h.rpcURL = rpc.URL
	makeIntent := func() (string, string) {
		r := h.run("send", "prepare", "--chain", "42161", "--token", "native", "--amount", "0.01", "--json")
		if r.code != 0 {
			t.Fatalf("prepare: %s", r.stdout)
		}
		return r.data()["intent_id"].(string), r.data()["digest"].(string)
	}
	first, digest := makeIntent()
	result := h.run("send", "submit", "--intent", first, "--digest", digest, "--json")
	if result.errCode() != "OUTCOME_UNKNOWN" {
		t.Fatalf("expected unknown: %s", result.stdout)
	}
	second, digest := makeIntent()
	result = h.run("send", "submit", "--intent", second, "--digest", digest, "--json")
	if result.errCode() != "OUTCOME_UNKNOWN" || h.privy.rpcCalls != 1 {
		t.Fatalf("unknown did not block broadcast: %s calls=%d", result.stdout, h.privy.rpcCalls)
	}
	if !strings.Contains(result.stdout, "blocking") {
		t.Fatalf("missing unresolved reason: %s", result.stdout)
	}
}

func TestSendInsufficientGasBeforeBroadcast(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_estimateGas":
			fmt.Fprint(w, `{"result":"0x5208"}`)
		case "eth_feeHistory":
			fmt.Fprint(w, `{"result":{"baseFeePerGas":["0x1","0x1"]}}`)
		case "eth_maxPriorityFeePerGas":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_getBalance":
			fmt.Fprint(w, `{"result":"0x0"}`)
		case "eth_gasPrice":
			fmt.Fprint(w, `{"result":"0x1"}`)
		default:
			t.Errorf("unexpected RPC %s", request.Method)
		}
	}))
	defer rpc.Close()
	h.rpcURL = rpc.URL
	prepared := h.run("send", "prepare", "--chain", "42161", "--token", "native", "--amount", "0.01", "--json")
	if prepared.code != 0 {
		t.Fatalf("prepare: %s", prepared.stdout)
	}
	result := h.run("send", "submit", "--intent", prepared.data()["intent_id"].(string), "--digest", prepared.data()["digest"].(string), "--json")
	if result.errCode() != "INSUFFICIENT_GAS" || h.privy.rpcCalls != 0 {
		t.Fatalf("gas refusal: %s calls=%d", result.stdout, h.privy.rpcCalls)
	}
}
