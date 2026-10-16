package cli

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
)

// Real successful OkuRouter transactions captured from Arbitrum One. They prove
// the embedded ABI decodes production router calldata, that a prepared intent
// matching the on-chain swap is accepted, and that the permit variant and a
// redirected recipient are refused.
func TestVerifyExecutionAcceptsRealArbitrumRouterCalldata(t *testing.T) {
	raw, err := os.ReadFile("testdata/okurouter_arbitrum_txs.json")
	if err != nil {
		t.Fatal(err)
	}
	var txs []struct {
		Hash, From, To, Value, Input string
		ChainID                      uint64 `json:"chain_id"`
	}
	if err := json.Unmarshal(raw, &txs); err != nil {
		t.Fatal(err)
	}
	parsed, err := abi.JSON(strings.NewReader(routerABI))
	if err != nil {
		t.Fatal(err)
	}
	accepted, permit := 0, 0
	for _, tx := range txs {
		data, _ := hex.DecodeString(strings.TrimPrefix(tx.Input, "0x"))
		method, err := parsed.MethodById(data[:4])
		if err != nil {
			permit++
			info := &v2.ExecutionInformation{Trade: &v2.TransactionPayload{ChainId: int64(tx.ChainID), To: tx.To, Data: tx.Input, Value: "0"}}
			in := journal.OnchainIntent{Kind: "swap", ChainID: tx.ChainID, OutChain: tx.ChainID, Target: okuRouter, Token: "0x" + strings.Repeat("1", 40), OutToken: "0x" + strings.Repeat("2", 40), Amount: "1", MinOut: "1", Recipient: tx.From, Spender: okuRouter}
			if _, err := verifyExecution(in, "1", info, nil); err == nil {
				t.Errorf("%s: permit-variant calldata accepted", tx.Hash)
			}
			continue
		}
		args, err := method.Inputs.Unpack(data[4:])
		if err != nil {
			t.Fatalf("%s: real calldata does not decode: %v", tx.Hash, err)
		}
		a := func(i int) string { return args[i].(common.Address).Hex() }
		n := func(i int) *big.Int { return args[i].(*big.Int) }
		value := new(big.Int)
		value.SetString(strings.TrimPrefix(tx.Value, "0x"), 16)
		in := journal.OnchainIntent{Kind: "swap", ChainID: tx.ChainID, OutChain: tx.ChainID, Target: okuRouter, MinOut: "1"}
		switch method.Name {
		case "fillQuoteEthToToken":
			in.Token, in.OutToken, in.Amount, in.Recipient = "native", a(0), value.String(), a(4)
		case "fillQuoteTokenToToken":
			in.Token, in.OutToken, in.Amount, in.Recipient, in.Spender = a(0), a(1), n(5).String(), a(7), okuRouter
		case "fillQuoteTokenToEth":
			in.Token, in.OutToken, in.Amount, in.Recipient, in.Spender = a(0), "native", n(4).String(), a(6), okuRouter
		}
		info := &v2.ExecutionInformation{Trade: &v2.TransactionPayload{ChainId: int64(tx.ChainID), To: tx.To, Data: tx.Input, Value: value.String()}}
		templates, err := verifyExecution(in, "1", info, nil)
		if err != nil {
			t.Fatalf("%s (%s): real router calldata refused: %v", tx.Hash, method.Name, err)
		}
		if last := templates[len(templates)-1]; !strings.EqualFold(last.To, okuRouter) || last.Data != tx.Input {
			t.Fatalf("%s: template does not carry the verified calldata", tx.Hash)
		}
		redirected := in
		redirected.Recipient = "0x" + strings.Repeat("9", 40)
		if _, err := verifyExecution(redirected, "1", info, nil); err == nil {
			t.Fatalf("%s: calldata accepted for a different recipient", tx.Hash)
		}
		accepted++
	}
	if accepted < 3 || permit < 1 {
		t.Fatalf("fixture coverage: accepted=%d permit=%d", accepted, permit)
	}
}
