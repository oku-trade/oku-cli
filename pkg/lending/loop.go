package lending

import "math"

// Loop sizing constants mirror the web app (loopEstimates.ts) and the
// backend: leverage moves in 0.05 steps and is capped at 10x.
const (
	LoopMinLeverage  = 1.0
	loopLeverageStep = 0.05
	loopMaxLeverage  = 10.0
)

// MaxLoopLeverage mirrors maxLoopLeverage: 1/(1-maxBorrowLTV), floored to
// the 0.05 step and capped at 10x.
func MaxLoopLeverage(lltv float64) float64 {
	maxLTV := MaxBorrowLTV(lltv)
	if !(maxLTV > 0) || maxLTV >= 1 {
		return LoopMinLeverage
	}
	raw := 1 / (1 - maxLTV)
	stepped := math.Floor(raw/loopLeverageStep+1e-9) * loopLeverageStep
	return math.Min(loopMaxLeverage, math.Max(LoopMinLeverage, math.Round(stepped*100)/100))
}

// LoopNetAPY is the leveraged position's yield on the deposit: the
// collateral earns its supply yield on lev times the deposit while
// (lev-1) times the deposit is borrowed. Borrow cost should already be net
// of borrow rewards.
func LoopNetAPY(leverage, collateralAPY, borrowAPY float64) float64 {
	return leverage*collateralAPY - (leverage-1)*borrowAPY
}
