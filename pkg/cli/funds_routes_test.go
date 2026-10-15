package cli

import (
	"fmt"
	"strings"
	"testing"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"google.golang.org/protobuf/types/known/structpb"
)

// The execution planner is the authorization boundary. The RPC/Privy sender
// cannot be reached unless it returns fully checked templates.
func TestRouteExecutionAuthorization(t *testing.T) {
	token := "0x1111111111111111111111111111111111111111"
	router := "0x2222222222222222222222222222222222222222"
	spender := "0x3333333333333333333333333333333333333333"
	recipient := "0x4444444444444444444444444444444444444444"
	in := journal.OnchainIntent{Kind: "swap", ChainID: 42161, OutChain: 42161, Token: token, Recipient: recipient, Target: router, Spender: spender, Amount: "10000000", MinOut: "100"}
	approval := func(amount string) *v2.ApprovalPayloadV2 {
		v, _ := structpb.NewStruct(map[string]any{"spender": spender, "amount": amount})
		return &v2.ApprovalPayloadV2{To: token, FunctionName: "approve", Values: v}
	}
	data := "0x12345678" + strings.Repeat("0", 24) + recipient[2:] + fmt.Sprintf("%064x", 100)
	for _, kind := range []string{"swap", "bridge"} {
		t.Run(kind, func(t *testing.T) {
			source := in
			source.Kind = kind
			if kind == "bridge" {
				source.OutChain = 8453
			}
			tx := &v2.TransactionPayload{ChainId: 42161, To: router, Value: "0x0", Data: data}
			info := &v2.ExecutionInformation{Trade: tx}
			check := func() error {
				_, err := verifyExecution(source, "100", info, []*v2.ApprovalPayloadV2{approval(source.Amount)})
				return err
			}
			templates, err := verifyExecution(source, "100", info, []*v2.ApprovalPayloadV2{approval(source.Amount)})
			if err != nil || len(templates) != 2 || templates[0].To != token || templates[1].To != router {
				t.Fatalf("valid route refused: %v %+v", err, templates)
			}
			cases := []struct {
				name    string
				mutate  func()
				restore func()
			}{
				{"unexpected router", func() { tx.To = token }, func() { tx.To = router }},
				{"chain mismatch", func() { tx.ChainId = 8453 }, func() { tx.ChainId = 42161 }},
				{"wrong destination", func() { tx.Data = "0x12345678" + strings.Repeat("0", 24) + token[2:] + fmt.Sprintf("%064x", 100) }, func() { tx.Data = data }},
				{"bridge deposit address", func() { info.DepositAddress = recipient }, func() { info.DepositAddress = "" }},
				{"psbt", func() { info.PsbtHex = "0011" }, func() { info.PsbtHex = "" }},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					tc.mutate()
					defer tc.restore()
					if err := check(); err == nil {
						t.Fatal("unsafe execution accepted")
					}
				})
			}
			if _, err = verifyExecution(source, "99", info, []*v2.ApprovalPayloadV2{approval(source.Amount)}); err == nil {
				t.Fatal("worse JIT quote accepted")
			}
			if _, err = verifyExecution(source, "100", info, []*v2.ApprovalPayloadV2{approval("10000001")}); err == nil {
				t.Fatal("approval greater than exact input accepted")
			}
		})
	}
}
