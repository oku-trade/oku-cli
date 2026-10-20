package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestWalletFundPaymentURI(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	token := "0x3333333333333333333333333333333333333333"
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&call)
		if call.Method == "eth_chainId" {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0xa4b1"}`)
		} else {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x6"}`)
		}
	}))
	defer rpc.Close()
	h.rpcURL = rpc.URL
	for _, tc := range []struct {
		name, token, amount, uri string
	}{
		{"native", "native", "", ""},
		{"native amount", "native", "0.01", "?value=10000000000000000"},
		{"erc20", token, "", ""},
		{"erc20 amount", token, "1.25", "&uint256=1250000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"wallet", "fund", "--token", tc.token, "--no-open", "--json"}
			if tc.amount != "" {
				args = append(args, "--amount", tc.amount)
			}
			r := h.run(args...)
			if r.code != 0 {
				t.Fatalf("fund: %s %s", r.stdout, r.stderr)
			}
			want := fmt.Sprintf("ethereum:%s@42161", strings.ToLower(h.fake.agentAddress))
			if tc.token != "native" {
				want = fmt.Sprintf("ethereum:%s@42161/transfer?address=%s", tc.token, strings.ToLower(h.fake.agentAddress))
			}
			if got := r.data()["payment_uri"]; got != want+tc.uri {
				t.Errorf("payment URI: got %q want %q", got, want+tc.uri)
			}
			web := r.data()["web_url"].(string)
			u, err := url.Parse(web)
			if err != nil || u.Scheme != "https" || u.Host != "oku.trade" || u.Path != "/agent/fund" || u.Query().Get("chain") != "42161" || u.Query().Get("token") != tc.token || u.Query().Get("amount") != tc.amount {
				t.Errorf("web URL: %s (%v)", web, err)
			}
		})
	}
}

func TestFundingHintsOnPreflightFailures(t *testing.T) {
	for _, tc := range []struct {
		cause error
		token string
	}{
		{onchain.ErrGas, "native"},
		{onchain.ErrBalance, "0x3333333333333333333333333333333333333333"},
	} {
		err := executionError(errors.Join(onchain.ErrNotBroadcast, tc.cause), "swap", 42161, tc.token)
		if !strings.Contains(err.Error(), "oku wallet fund --chain 42161 --token "+tc.token) {
			t.Errorf("missing funding hint: %v", err)
		}
	}
}

func TestCoWTypedDataIntegerJSON(t *testing.T) {
	domain, err := structpb.NewStruct(map[string]any{"name": "Gnosis Protocol", "version": "v2", "chainId": 42161, "verifyingContract": "0x9008D19f58AAbD9eD0D60971565AA8510560ab41"})
	if err != nil {
		t.Fatal(err)
	}
	message, err := structpb.NewStruct(map[string]any{"sellToken": "0x3333333333333333333333333333333333333333", "buyAmount": "100", "sellAmount": "1000", "feeAmount": "0", "validTo": 1792522648, "kind": "sell", "partiallyFillable": false, "sellTokenBalance": "erc20", "buyTokenBalance": "erc20"})
	if err != nil {
		t.Fatal(err)
	}
	types, err := structpb.NewStruct(map[string]any{"Order": []any{map[string]any{"name": "validTo", "type": "uint32"}}})
	if err != nil {
		t.Fatal(err)
	}
	converted, err := intentTypedData(&v2.TypedData{Domain: domain, Types: types, Message: message, PrimaryType: "Order"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"chainId":42161`) || !strings.Contains(string(raw), `"validTo":1792522648`) {
		t.Fatalf("CoW numeric fields changed in Privy JSON: %s", raw)
	}
}
