package privy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendTransactionOAuthWire(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/oauth/v2/wallets/wallet/rpc" || r.Header.Get("privy-authorization-signature") == "" {
			t.Errorf("unexpected RPC request: %s %v", r.URL.Path, r.Header)
		}
		var body struct {
			Method string `json:"method"`
			CAIP2  string `json:"caip2"`
			Params struct {
				Transaction map[string]any `json:"transaction"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		tx := body.Params.Transaction
		if body.Method != "eth_sendTransaction" || body.CAIP2 != "eip155:8453" || tx["chain_id"] != float64(8453) || tx["to"] != "0x1111111111111111111111111111111111111111" || tx["value"] != "0x0" || tx["data"] != "0x1234" {
			t.Errorf("unexpected transaction: %+v", body)
		}
		fmt.Fprintf(w, `{"method":"eth_sendTransaction","data":{"hash":"0x%s"}}`, strings.Repeat("a", 64))
	}))
	defer server.Close()
	client := &Client{AuthURL: server.URL, AppID: "app"}
	session := &Session{authKey: key, expiresAt: time.Now().Add(time.Hour), accessToken: "access", client: client}
	hash, err := session.SendTransaction(context.Background(), "wallet", Transaction{To: "0x1111111111111111111111111111111111111111", Value: "0x0", Data: "0x1234", ChainID: 8453})
	if err != nil || hash != "0x"+strings.Repeat("a", 64) || calls != 1 {
		t.Fatalf("hash=%s error=%v calls=%d", hash, err, calls)
	}
}
