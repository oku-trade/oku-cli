package lending

import (
	"errors"
	"fmt"
	"math"
	"math/big"
)

// The web app's borrow ceiling is the liquidation LTV minus 0.5 percentage
// points (getMaxBorrowLtv); both borrow and loop gates refuse anything above
// it. Its risk tier turns "High" once LTV/LLTV exceeds 0.75, which is a
// health factor (LLTV/LTV) below 1/0.75. The CLI refuses above the ceiling and
// requires an explicit override below that health factor.
const (
	BorrowLTVMargin = 0.005
	HighRiskRatio   = 0.75
)

// MinHealthFactor is the health factor below which a write needs an explicit
// high-risk override (LTV/LLTV > 0.75).
var MinHealthFactor = 1 / HighRiskRatio

// MaxBorrowLTV mirrors getMaxBorrowLtv: 0 for anything outside 0 < lltv <= 1.
func MaxBorrowLTV(lltv float64) float64 {
	if math.IsNaN(lltv) || lltv <= 0 || lltv > 1 {
		return 0
	}
	return math.Max(0, lltv-BorrowLTVMargin)
}

// Position is a collateral/debt pair in human units. Price is loan-asset per
// collateral-asset (the oracle price the market liquidates on).
type Position struct {
	Collateral float64
	Debt       float64
	Price      float64
	LLTV       float64
}

// Risk is the derived state shown in previews and positions.
type Risk struct {
	LTV              float64  `json:"ltv"`
	LLTV             float64  `json:"lltv"`
	MaxLTV           float64  `json:"max_ltv"`
	HealthFactor     *float64 `json:"health_factor"`     // nil when there is no debt
	LiquidationPrice *float64 `json:"liquidation_price"` // loan per collateral, nil when no debt or collateral
}

// Assess computes LTV, health factor and liquidation price. Missing prices are
// an error, never a fake zero.
func Assess(p Position) (Risk, error) {
	if p.LLTV <= 0 || p.LLTV > 1 || math.IsNaN(p.LLTV) {
		return Risk{}, errors.New("market liquidation LTV unknown")
	}
	r := Risk{LLTV: p.LLTV, MaxLTV: MaxBorrowLTV(p.LLTV)}
	if p.Debt <= 0 {
		return r, nil
	}
	if !(p.Price > 0) || math.IsInf(p.Price, 0) {
		return Risk{}, errors.New("oracle price unknown")
	}
	if !(p.Collateral > 0) {
		r.LTV = math.Inf(1)
		zero := 0.0
		r.HealthFactor = &zero
		return r, nil
	}
	r.LTV = p.Debt / (p.Collateral * p.Price)
	hf := p.LLTV / r.LTV
	r.HealthFactor = &hf
	liq := p.Debt / (p.Collateral * p.LLTV)
	r.LiquidationPrice = &liq
	return r, nil
}

// Verdict is the guardrail outcome for a post-action position.
type Verdict int

const (
	Safe Verdict = iota
	HighRisk
	Refused
)

// ErrHighRisk asks for an explicit override; ErrUnsafeLTV is never overridable.
var (
	ErrHighRisk  = errors.New("post-action health factor is below the high-risk threshold")
	ErrUnsafeLTV = errors.New("post-action LTV exceeds the maximum borrow LTV")
)

// Check applies the guardrails to a post-action risk. Debt-free positions are
// always safe. acceptHighRisk converts HighRisk to nil but never Refused.
func Check(r Risk, acceptHighRisk bool) error {
	switch Classify(r) {
	case Refused:
		return fmt.Errorf("%w: %.2f%% > %.2f%% (LLTV %.2f%% minus 0.5pp)", ErrUnsafeLTV, r.LTV*100, r.MaxLTV*100, r.LLTV*100)
	case HighRisk:
		if acceptHighRisk {
			return nil
		}
		return fmt.Errorf("%w: %.3f < %.3f (LTV %.2f%% of LLTV %.2f%%)", ErrHighRisk, *r.HealthFactor, MinHealthFactor, r.LTV*100, r.LLTV*100)
	}
	return nil
}

// Classify buckets a risk the way the guardrails act on it.
func Classify(r Risk) Verdict {
	if r.HealthFactor == nil {
		return Safe
	}
	if r.LTV > r.MaxLTV {
		return Refused
	}
	if *r.HealthFactor < MinHealthFactor {
		return HighRisk
	}
	return Safe
}

// MorphoPrice converts a Morpho oracle price (raw loan per raw collateral,
// scaled 1e36) to human loan per human collateral.
func MorphoPrice(oraclePrice *big.Int, collateralDecimals, loanDecimals int) float64 {
	if oraclePrice == nil || oraclePrice.Sign() <= 0 {
		return 0
	}
	return Float(oraclePrice, 36+loanDecimals-collateralDecimals)
}

// Float converts raw units to a human float.
func Float(raw *big.Int, decimals int) float64 {
	if raw == nil {
		return 0
	}
	f, _ := new(big.Rat).SetFrac(raw, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)).Float64()
	return f
}
