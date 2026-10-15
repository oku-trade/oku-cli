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
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"status":"0x1"}}`)
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
		case "eth_getBalance":
			fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
		case "eth_gasPrice":
			fmt.Fprint(w, `{"result":"0x1"}`)
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
