package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var nativeInputs = []string{"native", "NATIVE", zeroAddress, eip7528Address, strings.ToLower(eip7528Address)}

func TestNormalizeTokenNativeAliases(t *testing.T) {
	for _, in := range nativeInputs {
		if !isNativeToken(in) || normalizeToken(in) != "native" {
			t.Errorf("%q not treated as native", in)
		}
	}
	other := "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"
	if isNativeToken(other) || normalizeToken(other) != other {
		t.Fatal("ERC-20 treated as native")
	}
	if quoteTokenAddress("swap", normalizeToken(eip7528Address)) != zeroAddress {
		t.Fatal("native must go on the wire as the zero address")
	}
}

func TestTokenManifestNativeRows(t *testing.T) {
	rows, err := parseTokenManifest(strings.NewReader("chain_id,token_address,amount\n137," + zeroAddress + ",1\n137," + eip7528Address + ",1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[137]) != 1 || rows[137][0].Address != "native" || rows[137][0].Amount != "1" {
		t.Fatalf("native CSV rows must normalize and dedupe: %#v", rows)
	}
}

// nativeRPC answers chainId, balance (1 native), gas price and decimals; it
// fails the test on any balanceOf/decimals call against a native alias.
func nativeRPC(t *testing.T, chainHex string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_chainId":
			fmt.Fprintf(w, `{"result":%q}`, chainHex)
		case "eth_getBalance":
			fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
		case "eth_gasPrice":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_call":
			call, _ := req.Params[0].(map[string]any)
			if to, _ := call["to"].(string); isNativeToken(to) {
				t.Errorf("eth_call against native alias %s", to)
			}
			fmt.Fprint(w, `{"result":"0x0000000000000000000000000000000000000000000000000000000000000006"}`)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestWalletBalancesCSVNativeRowDefaultsToCSVChains(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.rpcURL = nativeRPC(t, "0x89")
	h.rpcChain = "137"
	csv := filepath.Join(t.TempDir(), "t.csv")
	if err := os.WriteFile(csv, []byte("137,"+zeroAddress+",1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run("wallet", "balances", "--tokens-file", csv, "--json")
	if r.code != 0 {
		t.Fatalf("balances: %s %s", r.stdout, r.stderr)
	}
	native, _ := r.data()["native"].(map[string]any)
	row, _ := native["eip155:137"].(map[string]any)
	if row == nil || row["csv_amount"] != "1" || row["mismatch"] != false || row["balance"] != "1" {
		t.Fatalf("CSV chain 137 native row missing or wrong: %s", r.stdout)
	}
	if _, has := native["eip155:42161"]; has {
		t.Fatalf("without --chains the CSV chains must be read, not 42161: %s", r.stdout)
	}
}

func TestSwapQuoteAcceptsNativeAddressAliases(t *testing.T) {
	for _, alias := range []string{zeroAddress, eip7528Address} {
		t.Run(alias, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			output := "0x2222222222222222222222222222222222222222"
			var sent any
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				switch method {
				case "GetSupportedMarkets":
					return map[string]any{"markets": []any{map[string]any{"name": "zeroex", "active": true, "supportedChainIds": []int{42161}}}}, true
				case "Create":
					sent = body["params"].(map[string]any)["inTokenAddress"]
					return map[string]any{"orderId": "01a15585-427e-70f2-a7e4-7457069b9c0c", "timeline": 1}, true
				case "GetFlowstreamQuotes":
					return map[string]any{"timeline": 1, "quotes": map[string]any{"zeroex": map[string]any{"router": "zeroex", "fetched": true, "quoteId": "q", "quote": map[string]any{
						"chainId": 42161, "isExactIn": true, "inAmountRaw": "1000000000000000000", "minAmountOutRaw": "90", "outAmountRaw": "100", "netUsdValue": 1, "inUsdValue": 1, "outUsdValue": 1,
						"inToken": map[string]any{"address": zeroAddress}, "outToken": map[string]any{"address": output},
						"simulation": map[string]any{"amountOutRaw": "100"},
					}}}}, true
				case "Cancel":
					return map[string]any{}, true
				}
				return nil, false
			}
			h.rpcURL = nativeRPC(t, "0xa4b1")
			r := h.run("swap", "quote", "--chain", "42161", "--from", alias, "--to", output, "--amount", "1", "--json")
			if r.code != 0 || r.data()["in_token"] != "native" || sent != zeroAddress {
				t.Fatalf("swap quote from %s: sent %v: %s %s", alias, sent, r.stdout, r.stderr)
			}
			if _, ok := r.data()["loss_bps"]; !ok {
				t.Fatalf("quote must report loss_bps: %s", r.stdout)
			}
		})
	}
}

func TestBridgeQuoteAcceptsNativeAddressInputs(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	var params map[string]any
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		switch method {
		case "GetSupportedBridges":
			return map[string]any{"bridges": []any{map[string]any{"name": "lifi", "active": true, "supportedChains": []string{"eip155:8453", "eip155:42161"}}}}, true
		case "Create":
			params = body["params"].(map[string]any)
			return map[string]any{"orderId": "order"}, true
		case "UpdateQuoteParams":
			return map[string]any{"orderId": "order", "generation": 1}, true
		case "GetNewQuotes":
			return map[string]any{"quotes": map[string]any{"lifi": map[string]any{"bridge": "lifi", "fetched": true, "quoteId": "quote", "quote": map[string]any{
				"inChainId": 8453, "outChainId": 42161, "inToken": map[string]any{"address": zeroAddress, "decimals": 18}, "outToken": map[string]any{"address": eip7528Address, "decimals": 18},
				"inAmount": "0.01", "minAmountOut": "0.009", "outAmount": "0.0095", "outUsdValue": 20, "feesUsdValue": 1, "gasUsdValue": 1,
			}}}}, true
		case "Cancel":
			return map[string]any{}, true
		}
		return nil, false
	}
	r := h.run("bridge", "quote", "--chain", "8453", "--destination-chain", "42161", "--from", zeroAddress, "--to", eip7528Address, "--amount", "0.01", "--json")
	if r.code != 0 || r.data()["route"] != "lifi" || params["inTokenAddress"] != zeroAddress || params["outTokenAddress"] != zeroAddress {
		t.Fatalf("bridge quote with address aliases: %v: %s %s", params, r.stdout, r.stderr)
	}
}

func TestSendPrepareAcceptsZeroAddressAsNative(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.rpcURL = nativeRPC(t, "0xa4b1")
	r := h.run("send", "prepare", "--chain", "42161", "--token", zeroAddress, "--amount", "0.01", "--json")
	if r.code != 0 || r.data()["token"] != "native" {
		t.Fatalf("send prepare with zero address: %s %s", r.stdout, r.stderr)
	}
}

// One chain without an RPC endpoint is reported under errors and skipped;
// the other chains are still read. Blast (81457) is deprecated in chain-config,
// so it has no default endpoint.
func TestWalletBalancesSkipsUnreadableChain(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.rpcURL = nativeRPC(t, "0xa4b1")
	r := h.run("wallet", "balances", "--chains", "42161,81457", "--json")
	if r.code != 0 {
		t.Fatalf("balances: %s %s", r.stdout, r.stderr)
	}
	native, _ := r.data()["native"].(map[string]any)
	if _, ok := native["eip155:42161"]; !ok {
		t.Fatalf("readable chain missing: %s", r.stdout)
	}
	errs, _ := r.data()["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["chain"] != "eip155:81457" || errs[0].(map[string]any)["code"] != "NO_RPC" {
		t.Fatalf("unreadable chain not reported: %s", r.stdout)
	}
	if r := h.run("wallet", "balances", "--chain", "81457", "--json"); r.code == 0 {
		t.Fatalf("all chains failing must be an error: %s", r.stdout)
	}
}
