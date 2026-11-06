package lending

import "testing"

func TestLoopLeverageAtHonorsCapBelowLiquidation(t *testing.T) {
	// Euler AMZNon/USDT: lltv 0.75 would allow 3.9x, the 0.65 borrow cap only 2.85x.
	if got := MaxLoopLeverage(0.75); got != 3.9 {
		t.Fatalf("liquidation-derived ceiling %v", got)
	}
	if got := LoopLeverageAt(0.65); got != 2.85 {
		t.Fatalf("capped ceiling %v, want 2.85", got)
	}
}
