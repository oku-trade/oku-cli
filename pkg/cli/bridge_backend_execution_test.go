package cli

import (
	"math/big"
	"strings"
	"testing"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
)

func TestBridgeBackendExecutionKeepsApprovalAndExtraCallOrder(t *testing.T) {
	token := "0x3333333333333333333333333333333333333333"
	first := "0x5555555555555555555555555555555555555555"
	extra := "0x6666666666666666666666666666666666666666"
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)).String()
	info := &v2.ExecutionInformation{
		Approvals:    []*v2.ApprovalPayload{{ChainId: 42161, Address: token, Approvee: first, Amount: max}},
		Trade:        &v2.TransactionPayload{ChainId: 42161, To: first, Value: "0", Data: "0xdeadbeef"},
		Transactions: []*v2.TransactionPayload{{ChainId: 42161, To: extra, Value: "1", Data: "0x1234"}},
	}
	calls, err := backendBridgeExecution(42161, info, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0].To != token || calls[1].To != first || calls[1].Data != "0xdeadbeef" || calls[2].To != extra || calls[2].Data != "0x1234" || calls[2].Value != "0x1" || !strings.HasSuffix(calls[0].Data, strings.Repeat("f", 64)) {
		t.Fatalf("backend approval/trade/extra calls changed: %+v", calls)
	}
}
