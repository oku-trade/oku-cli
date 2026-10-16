package cli

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type testWarrant struct {
	Nonce           *big.Int
	ValidBefore     *big.Int
	ValidAfter      *big.Int
	VerifyingSigner common.Address
	Signature       []byte
}

func TestRouteExecutionAuthorization(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(routerABI))
	if err != nil {
		t.Fatal(err)
	}
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	output := common.HexToAddress("0x2222222222222222222222222222222222222222")
	recipient := common.HexToAddress("0x4444444444444444444444444444444444444444")
	target := common.HexToAddress("0x5555555555555555555555555555555555555555")
	warrant := testWarrant{big.NewInt(0), big.NewInt(0), big.NewInt(0), common.Address{}, []byte{}}
	amount := big.NewInt(10000000)
	pack := func(method string, args ...any) string {
		t.Helper()
		data, e := parsed.Pack(method, args...)
		if e != nil {
			t.Fatal(e)
		}
		return fmt.Sprintf("0x%x", data)
	}
	data := pack("fillQuoteTokenToToken", token, output, target, target, []byte{1, 2}, amount, big.NewInt(0), recipient, warrant)
	in := journal.OnchainIntent{Kind: "swap", ChainID: 42161, OutChain: 42161, Token: token.Hex(), OutToken: output.Hex(), Recipient: recipient.Hex(), Target: okuRouter, Spender: okuRouter, Amount: amount.String(), MinOut: "100"}
	approval := func(spender string) *v2.ApprovalPayloadV2 {
		v, e := structpb.NewStruct(map[string]any{"spender": spender, "amount": amount.String()})
		if e != nil {
			t.Fatal(e)
		}
		return &v2.ApprovalPayloadV2{To: token.Hex(), FunctionName: "approve", Values: v}
	}
	tx := &v2.TransactionPayload{ChainId: 42161, To: okuRouter, Value: "0x0", Data: data}
	info := &v2.ExecutionInformation{Trade: tx}
	if templates, e := verifyExecution(in, "100", info, []*v2.ApprovalPayloadV2{approval(okuRouter)}); e != nil || len(templates) != 2 || !strings.EqualFold(templates[0].To, token.Hex()) || templates[0].Data[:10] != "0x095ea7b3" {
		t.Fatalf("verified token swap: %v %+v", e, templates)
	}
	tests := []struct {
		name   string
		mutate func(*journal.OnchainIntent, *v2.TransactionPayload, **v2.ApprovalPayloadV2)
	}{
		{"wrong router", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			tx.To = target.Hex()
		}},
		{"wrong recipient", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			tx.Data = pack("fillQuoteTokenToToken", token, output, target, target, []byte{1}, amount, big.NewInt(0), target, warrant)
		}},
		{"wrong amount", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			tx.Data = pack("fillQuoteTokenToToken", token, output, target, target, []byte{1}, big.NewInt(10000001), big.NewInt(0), recipient, warrant)
		}},
		{"aggregator approval", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			*a = approval(target.Hex())
		}},
		{"permit selector", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) {
			tx.Data = "0xdeadbeef" + data[10:]
		}},
		{"chain mismatch", func(i *journal.OnchainIntent, tx *v2.TransactionPayload, a **v2.ApprovalPayloadV2) { tx.ChainId = 8453 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			copyIntent := in
			copyTx := proto.Clone(tx).(*v2.TransactionPayload)
			ap := approval(okuRouter)
			tc.mutate(&copyIntent, copyTx, &ap)
			if _, err := verifyExecution(copyIntent, "100", &v2.ExecutionInformation{Trade: copyTx}, []*v2.ApprovalPayloadV2{ap}); err == nil {
				t.Fatal("unsafe swap accepted")
			}
		})
	}
	eth := in
	eth.Token = "native"
	eth.Spender = ""
	eth.Amount = "10000000"
	ethTx := &v2.TransactionPayload{ChainId: 42161, To: okuRouter, Value: "0x989680", Data: pack("fillQuoteEthToToken", output, target, []byte{1}, big.NewInt(0), recipient, warrant)}
	if templates, err := verifyExecution(eth, "100", &v2.ExecutionInformation{Trade: ethTx}, nil); err != nil || len(templates) != 1 {
		t.Fatalf("ETH swap refused: %v %+v", err, templates)
	}
}
