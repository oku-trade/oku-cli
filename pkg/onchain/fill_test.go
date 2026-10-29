package onchain

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"testing"
)

const fixtureRouter = "0xb1f3a7B816B0681188F54dFa400991B93ADf00ed"

func loadRouterReceipts(t *testing.T) map[string]*TxReceipt {
	t.Helper()
	raw, err := os.ReadFile("testdata/okurouter_arbitrum_receipts.json")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*TxReceipt{}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMeasureSwapFillArbitrumReceipts(t *testing.T) {
	r := loadRouterReceipts(t)
	usdc := "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"
	for _, c := range []struct {
		name, out, recipient string
		native               bool
		received, gas        string
		source               string
	}{
		{"native_to_token", usdc, "0x1f84833dcb4e7421fe691cb64369580c1e48b287", false, "210021894", "10633920000000", "transfer_logs"},
		{"token_to_native", "native", "0xa83305e565e4d0afb339500c9010c5d16c66c54f", true, "50568168676903", "4329138352000", "router_event"},
		{"token_to_token", usdc, "0x346a5de94601dccb94ad060b9b4a59a22f782513", false, "1000856571", "22140960000000", "transfer_logs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := MeasureSwapFill([]*TxReceipt{r[c.name]}, fixtureRouter, c.out, c.recipient, c.native)
			if err != nil {
				t.Fatal(err)
			}
			if f.Received.String() != c.received || f.Gas.String() != c.gas || f.Source != c.source {
				t.Fatalf("fill %s gas %s source %s", f.Received, f.Gas, f.Source)
			}
		})
	}
}

func TestMeasureSwapFillRejects(t *testing.T) {
	r := loadRouterReceipts(t)
	agent := "0x1f84833dcb4e7421fe691cb64369580c1e48b287"
	usdc := "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"
	if _, err := MeasureSwapFill([]*TxReceipt{r["native_to_token"]}, fixtureRouter, usdc, "0x2222222222222222222222222222222222222222", false); !errors.Is(err, ErrNoFill) {
		t.Fatalf("other recipient: %v", err)
	}
	failed := *r["native_to_token"]
	failed.Status = "0x0"
	if _, err := MeasureSwapFill([]*TxReceipt{&failed}, fixtureRouter, usdc, agent, false); err == nil {
		t.Fatal("failed receipt accepted")
	}
	if _, err := MeasureSwapFill([]*TxReceipt{r["native_to_token"]}, "0x3333333333333333333333333333333333333333", "native", agent, true); !errors.Is(err, ErrNoFill) {
		t.Fatalf("other router: %v", err)
	}
	withL1 := *r["native_to_token"]
	withL1.L1Fee = "0x10"
	if g, err := withL1.Gas(); err != nil || g.Cmp(big.NewInt(10633920000016)) != 0 {
		t.Fatalf("l1 fee: %v %v", g, err)
	}
}
