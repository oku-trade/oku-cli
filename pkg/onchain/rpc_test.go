package onchain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
