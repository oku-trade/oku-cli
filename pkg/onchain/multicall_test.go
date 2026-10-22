package onchain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

func TestTokenBalancesMulticall(t *testing.T) {
	contract, err := abi.JSON(strings.NewReader(aggregateABI))
	if err != nil {
		t.Fatal(err)
	}
	results, err := contract.Methods["aggregate3"].Outputs.Pack([]multicallOutput{{Success: true, ReturnData: big.NewInt(123456).FillBytes(make([]byte, 32))}, {Success: true, ReturnData: append(big.NewInt(32).FillBytes(make([]byte, 32)), append(big.NewInt(3).FillBytes(make([]byte, 32)), append([]byte("TOK"), make([]byte, 29)...)...)...)}, {Success: true, ReturnData: big.NewInt(6).FillBytes(make([]byte, 32))}})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		calls++
		result := "0x2105"
		if request.Method == "eth_call" {
			result = "0x" + hex.EncodeToString(results)
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer server.Close()
	rpc := HTTPRPC{Getenv: func(string) string { return server.URL }}
	balances, err := rpc.TokenBalances(context.Background(), 8453, "0x0000000000000000000000000000000000000001", []string{"0x0000000000000000000000000000000000000002"})
	if err != nil {
		t.Fatal(err)
	}
	if len(balances) != 1 || balances[0].Int64() != 123456 || calls != 2 {
		t.Fatalf("balances=%v calls=%d", balances, calls)
	}
}
