package cli

import (
	"github.com/oku-trade/oku-cli/pkg/journal"
	"math/big"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"testing"
)

func TestResolvedAmountBoundInDigest(t *testing.T) {
	in := journal.OnchainIntent{ID: "resolved", Kind: "send", Amount: "1.25", Token: "native", Templates: []journal.OnchainTemplate{{ChainID: 8453, Value: "0x1158e460913d0000"}}}
	digest := in.Digest()
	in.Amount = "1.26"
	if in.Digest() == digest {
		t.Fatal("resolved amount absent from intent digest")
	}
	in.Amount = "1.25"
	in.Templates[0].Value = "0x1"
	if in.Digest() == digest {
		t.Fatal("submitted transfer value absent from intent digest")
	}
}

func TestMaxAmountFormatting(t *testing.T) {
	cases := []struct {
		raw      string
		decimals int
		want     string
	}{
		{"1000000", 6, "1"}, {"1000001", 6, "1.000001"}, {"1", 18, "0.000000000000000001"}, {"123", 0, "123"},
	}
	for _, tc := range cases {
		n, _ := new(big.Int).SetString(tc.raw, 10)
		got := formatUnits(n, tc.decimals)
		if got != tc.want {
			t.Errorf("formatUnits(%s,%d)=%s, want %s", tc.raw, tc.decimals, got, tc.want)
		}
		parsed, err := parseUnits(got, tc.decimals)
		if err != nil || parsed.Cmp(n) != 0 {
			t.Errorf("round trip %s: %v %v", tc.raw, parsed, err)
		}
	}
	if !isMaxAmount("MAX") || !isMaxAmount("all") || isMaxAmount("1") {
		t.Fatal("max aliases")
	}
	if n, err := parseNonnegativeUnits("0", 18); err != nil || n.Sign() != 0 {
		t.Fatal("zero reserve", n, err)
	}
}

func TestSwapFeeSeparatesFlatFeeFromMarketImpact(t *testing.T) {
	info := &v2.FeeInfo{InputTokenFeeInfo: &v2.TokenFeeInfo{AmountRaw: "104400000000000", TokenAddress: "0x0000000000000000000000000000000000000000"}}
	raw, _, usd, impact := swapFee(info, "1000000000000000", 2.3947, 2.1385)
	if raw != "104400000000000" || usd < 0.249 || usd > 0.251 || impact < -0.0035 || impact > 0 {
		t.Fatalf("raw %s usd %v impact %v", raw, usd, impact)
	}
}
