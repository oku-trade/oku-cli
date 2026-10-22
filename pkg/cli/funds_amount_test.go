package cli

import (
	"math/big"
	"testing"
)

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
