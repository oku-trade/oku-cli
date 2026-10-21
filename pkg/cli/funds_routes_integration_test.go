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
)

func TestSwapSubmitBroadcastsOnlyVerifiedRouterCall(t *testing.T) {
	for _, tc := range []struct {
		native, noCode, wrongTarget, unsafeInfo bool
	}{{false, false, false, false}, {true, false, false, false}, {true, true, false, false}, {true, false, true, false}, {true, false, false, true}} {
		native := tc.native
		name := "token"
		if native {
			name = "eth"
		}
		if tc.noCode {
			// A router address without code would accept native value as a
			// plain transfer: submit must refuse before any Privy call.
			name = "eth-router-without-code"
		}
		if tc.wrongTarget {
			name = "eth-wrong-execution-target"
		}
		if tc.unsafeInfo {
			name = "eth-unsafe-execution-shape"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			output := common.HexToAddress("0x2222222222222222222222222222222222222222")
			input := common.HexToAddress("0x3333333333333333333333333333333333333333")
			recipient := common.HexToAddress(h.fake.agentAddress)
			target := common.HexToAddress("0x5555555555555555555555555555555555555555")
			parsed, e := abi.JSON(strings.NewReader(routerABI))
			if e != nil {
				t.Fatal(e)
			}
			warrant := testWarrant{big.NewInt(0), big.NewInt(0), big.NewInt(0), common.Address{}, []byte{}}
			amount := big.NewInt(10000000)
			method := "fillQuoteTokenToToken"
			arguments := []any{input, output, target, target, []byte{1}, amount, big.NewInt(0), recipient, warrant}
			token := input.Hex()
			spender := okuRouter
			value := "0"
			if native {
				method = "fillQuoteEthToToken"
				arguments = []any{output, target, []byte{1}, big.NewInt(0), recipient, warrant}
				token = "native"
				spender = ""
				value = "10000000"
			}
			packed, e := parsed.Pack(method, arguments...)
			if e != nil {
				t.Fatal(e)
			}
			calldata := fmt.Sprintf("0x%x", packed)
			trade := map[string]any{"chainId": "42161", "to": okuRouter, "value": value, "data": calldata}
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				switch method {
				case "UpdateQuoteSelect":
					return map[string]any{"router": "route", "quoteId": "quote"}, true
				case "ConfirmQuoteSelect":
					return map[string]any{}, true
				case "GetQuotes":
					return map[string]any{"quotes": map[string]any{"route": map[string]any{"router": "route", "quoteId": "quote", "quote": map[string]any{"minAmountOutRaw": "100", "simulation": map[string]any{"amountOutRaw": "100"}}}}}, true
				case "GetExecutionInfo":
					if tc.wrongTarget {
						trade["to"] = target.Hex()
					}
					info := map[string]any{"trade": trade}
					if tc.unsafeInfo {
						info["depositAddress"] = target.Hex()
					}
					return map[string]any{"info": info}, true
				case "Finish":
					return map[string]any{}, true
				}
				return nil, false
			}
			rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				switch req.Method {
				case "eth_chainId":
					fmt.Fprint(w, `{"result":"0xa4b1"}`)
				case "eth_getCode":
					if tc.noCode {
						fmt.Fprint(w, `{"result":"0x"}`)
						return
					}
					fmt.Fprint(w, `{"result":"0x6080"}`)
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
				case "eth_getTransactionReceipt":
					fmt.Fprint(w, `{"result":{"status":"0x1","transactionHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","blockHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","blockNumber":"0x10"}}`)
				case "eth_blockNumber":
					fmt.Fprint(w, `{"result":"0x30"}`)
				case "eth_getBlockByNumber":
					fmt.Fprint(w, `{"result":{"hash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
				default:
					t.Errorf("unexpected RPC %s", req.Method)
				}
			}))
			defer rpc.Close()
			h.rpcURL = rpc.URL
			p := &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL}
			j, e := openJournal(p)
			if e != nil {
				t.Fatal(e)
			}
			record, e := j.CreateOnchain(journal.OnchainIntent{ID: "swap-test", Kind: "swap", ChainID: 42161, OutChain: 42161, Sender: h.fake.agentAddress, Recipient: recipient.Hex(), Token: token, OutToken: output.Hex(), Amount: amount.String(), OrderID: "order", QuoteID: "quote", Route: "route", Target: okuRouter, Spender: spender, MinOut: "100", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpiresAt: time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano), Templates: []journal.OnchainTemplate{{ChainID: 42161, To: okuRouter, Value: "0x0"}}})
			if e != nil {
				t.Fatal(e)
			}
			j.Close()
			result := h.run("swap", "submit", "--intent", record.ID, "--digest", record.Digest, "--json")
			if tc.wrongTarget || tc.unsafeInfo {
				if result.errCode() != "PAYLOAD_MISMATCH" || h.privy.rpcCalls != 0 {
					t.Fatalf("unsafe authenticated swap execution must be refused before signing: %s; privy sends=%d", result.stdout, h.privy.rpcCalls)
				}
				return
			}
			if tc.noCode {
				if result.errCode() != "PAYLOAD_MISMATCH" || h.privy.rpcCalls != 0 || !strings.Contains(result.stdout, "no OkuRouter contract") {
					t.Fatalf("router without code must be refused before signing: %s; privy sends=%d", result.stdout, h.privy.rpcCalls)
				}
				return
			}
			if result.code != 0 || result.data()["state"] != "confirmed" || h.privy.rpcCalls != 1 {
				t.Fatalf("swap submit: %s; privy sends=%d", result.stdout, h.privy.rpcCalls)
			}
		})
	}
}
