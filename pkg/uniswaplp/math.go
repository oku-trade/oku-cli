// Package uniswaplp builds and verifies Uniswap v3 and v4 liquidity position
// actions for the agent wallet. The math is a direct port of the Uniswap
// v3-sdk (TickMath, SqrtPriceMath, maxLiquidityForAmounts, Position) and of the
// Oku web app's v4 slippage and burn-minimum helpers, so a plan built here
// matches what the web app would submit for the same inputs.
package uniswaplp

import (
	"errors"
	"fmt"
	"math/big"
)

const (
	MinTick = -887272
	MaxTick = 887272
)

var (
	MinSqrtRatio = big.NewInt(4295128739)
	MaxSqrtRatio = mustInt("1461446703485210103287273052203988822378723970342")
	q32          = new(big.Int).Lsh(big.NewInt(1), 32)
	q96          = new(big.Int).Lsh(big.NewInt(1), 96)
	q128         = new(big.Int).Lsh(big.NewInt(1), 128)
	two256       = new(big.Int).Lsh(big.NewInt(1), 256)
	maxUint256   = new(big.Int).Sub(two256, big.NewInt(1))
	MaxUint128   = new(big.Int).Sub(q128, big.NewInt(1))
	maxUint160   = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 160), big.NewInt(1))
)

func mustInt(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 0)
	if !ok {
		panic("invalid integer constant " + s)
	}
	return v
}

var tickMagic = []string{
	"0xfff97272373d413259a46990580e213a", "0xfff2e50f5f656932ef12357cf3c7fdcc", "0xffe5caca7e10e4e61c3624eaa0941cd0",
	"0xffcb9843d60f6159c9db58835c926644", "0xff973b41fa98c081472e6896dfb254c0", "0xff2ea16466c96a3843ec78b326b52861",
	"0xfe5dee046a99a2a811c461f1969c3053", "0xfcbe86c7900a88aedcffc83b479aa3a4", "0xf987a7253ac413176f2b074cf7815e54",
	"0xf3392b0822b70005940c7a398e4b70f3", "0xe7159475a2c29b7443b29c7fa6e889d9", "0xd097f3bdfd2022b8845ad8f792aa5825",
	"0xa9f746462d870fdf8a65dc1f90e061e5", "0x70d869a156d2a1b890bb3df62baf32f7", "0x31be135f97d08fd981231505542fcfa6",
	"0x9aa508b5b7a84e1c677de54f3e99bc9", "0x5d6af8dedb81196699c329225ee604", "0x2216e584f5fa1ea926041bedfe98",
	"0x48a170391f7dc42444e8fa2",
}

// SqrtRatioAtTick returns sqrt(1.0001^tick) as a Q64.96 (TickMath.getSqrtRatioAtTick).
func SqrtRatioAtTick(tick int) (*big.Int, error) {
	if tick < MinTick || tick > MaxTick {
		return nil, fmt.Errorf("tick %d out of range", tick)
	}
	abs := tick
	if abs < 0 {
		abs = -abs
	}
	ratio := mustInt("0x100000000000000000000000000000000")
	if abs&1 != 0 {
		ratio = mustInt("0xfffcb933bd6fad37aa2d162d1a594001")
	}
	for i, magic := range tickMagic {
		if abs&(2<<i) != 0 {
			ratio.Mul(ratio, mustInt(magic))
			ratio.Rsh(ratio, 128)
		}
	}
	if tick > 0 {
		ratio = new(big.Int).Div(maxUint256, ratio)
	}
	q, r := new(big.Int).QuoRem(ratio, q32, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q, nil
}

// TickAtSqrtRatio returns the greatest tick whose sqrt ratio is at most
// sqrtRatioX96 (TickMath.getTickAtSqrtRatio), found by binary search over the
// exact forward function.
func TickAtSqrtRatio(sqrtRatioX96 *big.Int) (int, error) {
	if sqrtRatioX96.Cmp(MinSqrtRatio) < 0 || sqrtRatioX96.Cmp(MaxSqrtRatio) >= 0 {
		return 0, errors.New("sqrt ratio out of range")
	}
	lo, hi := MinTick, MaxTick
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		r, _ := SqrtRatioAtTick(mid)
		if r.Cmp(sqrtRatioX96) <= 0 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, nil
}

func mulDivRoundingUp(a, b, d *big.Int) *big.Int {
	q, r := new(big.Int).QuoRem(new(big.Int).Mul(a, b), d, new(big.Int))
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

func ordered(a, b *big.Int) (*big.Int, *big.Int) {
	if a.Cmp(b) > 0 {
		return b, a
	}
	return a, b
}

// Amount0Delta is SqrtPriceMath.getAmount0Delta.
func Amount0Delta(a, b, liquidity *big.Int, roundUp bool) *big.Int {
	a, b = ordered(a, b)
	n1 := new(big.Int).Lsh(liquidity, 96)
	n2 := new(big.Int).Sub(b, a)
	if roundUp {
		return mulDivRoundingUp(mulDivRoundingUp(n1, n2, b), big.NewInt(1), a)
	}
	v := new(big.Int).Mul(n1, n2)
	v.Div(v, b)
	return v.Div(v, a)
}

// Amount1Delta is SqrtPriceMath.getAmount1Delta.
func Amount1Delta(a, b, liquidity *big.Int, roundUp bool) *big.Int {
	a, b = ordered(a, b)
	d := new(big.Int).Sub(b, a)
	if roundUp {
		return mulDivRoundingUp(liquidity, d, q96)
	}
	v := new(big.Int).Mul(liquidity, d)
	return v.Div(v, q96)
}

func liquidityForAmount0(a, b, amount0 *big.Int, precise bool) *big.Int {
	a, b = ordered(a, b)
	if precise {
		n := new(big.Int).Mul(new(big.Int).Mul(amount0, a), b)
		return n.Div(n, new(big.Int).Mul(q96, new(big.Int).Sub(b, a)))
	}
	inter := new(big.Int).Mul(a, b)
	inter.Div(inter, q96)
	n := new(big.Int).Mul(amount0, inter)
	return n.Div(n, new(big.Int).Sub(b, a))
}

func liquidityForAmount1(a, b, amount1 *big.Int) *big.Int {
	a, b = ordered(a, b)
	n := new(big.Int).Mul(amount1, q96)
	return n.Div(n, new(big.Int).Sub(b, a))
}

// MaxLiquidityForAmounts is the v3-sdk maxLiquidityForAmounts.
func MaxLiquidityForAmounts(current, a, b, amount0, amount1 *big.Int, precise bool) *big.Int {
	a, b = ordered(a, b)
	switch {
	case current.Cmp(a) <= 0:
		return liquidityForAmount0(a, b, amount0, precise)
	case current.Cmp(b) < 0:
		l0 := liquidityForAmount0(current, b, amount0, precise)
		l1 := liquidityForAmount1(a, current, amount1)
		if l0.Cmp(l1) < 0 {
			return l0
		}
		return l1
	default:
		return liquidityForAmount1(a, b, amount1)
	}
}

// PoolState is the price of a pool at a point in time.
type PoolState struct {
	SqrtPriceX96 *big.Int
	Tick         int
}

// Range is a validated position tick range.
type Range struct{ Lower, Upper int }

// NewRange validates tick bounds against the pool's tick spacing.
func NewRange(lower, upper, spacing int) (Range, error) {
	switch {
	case spacing <= 0:
		return Range{}, errors.New("tick spacing must be positive")
	case lower >= upper:
		return Range{}, fmt.Errorf("tick lower %d must be below tick upper %d", lower, upper)
	case lower < MinTick || upper > MaxTick:
		return Range{}, errors.New("ticks out of range")
	case lower%spacing != 0 || upper%spacing != 0:
		return Range{}, fmt.Errorf("ticks must be multiples of the tick spacing %d", spacing)
	}
	return Range{lower, upper}, nil
}

// sqrtBounds requires a range built by NewRange (every loader and planner
// path validates ticks there), so a conversion failure is a broken invariant.
func (r Range) sqrtBounds() (*big.Int, *big.Int) {
	a, err := SqrtRatioAtTick(r.Lower)
	if err != nil {
		panic(fmt.Sprintf("unvalidated range lower tick: %v", err))
	}
	b, err := SqrtRatioAtTick(r.Upper)
	if err != nil {
		panic(fmt.Sprintf("unvalidated range upper tick: %v", err))
	}
	return a, b
}

// InRange reports whether the pool's current tick earns fees in r.
func (r Range) InRange(tick int) bool { return tick >= r.Lower && tick < r.Upper }

// Amounts is a pair of raw token amounts.
type Amounts struct{ Amount0, Amount1 *big.Int }

// MintAmounts is Position.mintAmounts: the amounts (rounded up) needed to mint
// liquidity at the given pool state.
func MintAmounts(s PoolState, r Range, liquidity *big.Int) Amounts {
	a, b := r.sqrtBounds()
	switch {
	case s.Tick < r.Lower:
		return Amounts{Amount0Delta(a, b, liquidity, true), new(big.Int)}
	case s.Tick < r.Upper:
		return Amounts{Amount0Delta(s.SqrtPriceX96, b, liquidity, true), Amount1Delta(a, s.SqrtPriceX96, liquidity, true)}
	default:
		return Amounts{new(big.Int), Amount1Delta(a, b, liquidity, true)}
	}
}

// PositionAmounts is Position.amount0/amount1: the principal (rounded down)
// that removing liquidity at the given pool state returns.
func PositionAmounts(s PoolState, r Range, liquidity *big.Int) Amounts {
	a, b := r.sqrtBounds()
	switch {
	case s.Tick < r.Lower:
		return Amounts{Amount0Delta(a, b, liquidity, false), new(big.Int)}
	case s.Tick < r.Upper:
		return Amounts{Amount0Delta(s.SqrtPriceX96, b, liquidity, false), Amount1Delta(a, s.SqrtPriceX96, liquidity, false)}
	default:
		return Amounts{new(big.Int), Amount1Delta(a, b, liquidity, false)}
	}
}

// Side names which pool token an input amount is denominated in.
type Side int

const (
	Side0 Side = iota
	Side1
)

// LiquidityForAmount returns the liquidity a single-sided input amount buys
// in r. It fails when the range does not use that token at the current price.
func LiquidityForAmount(s PoolState, r Range, side Side, amount *big.Int, precise bool) (*big.Int, error) {
	a, b := r.sqrtBounds()
	var l *big.Int
	switch side {
	case Side0:
		if s.Tick >= r.Upper {
			return nil, errors.New("range is below the current price; it takes only token1")
		}
		lower := a
		if s.Tick >= r.Lower {
			lower = s.SqrtPriceX96
		}
		l = liquidityForAmount0(lower, b, amount, precise)
	case Side1:
		if s.Tick < r.Lower {
			return nil, errors.New("range is above the current price; it takes only token0")
		}
		upper := b
		if s.Tick < r.Upper {
			upper = s.SqrtPriceX96
		}
		l = liquidityForAmount1(a, upper, amount)
	default:
		return nil, errors.New("invalid side")
	}
	if l.Sign() <= 0 {
		return nil, errors.New("amount is too small to mint any liquidity")
	}
	return l, nil
}

// encodeSqrtRatioX96 is sqrt((amount1 << 192) / amount0).
func encodeSqrtRatioX96(num, den *big.Int) *big.Int {
	r := new(big.Int).Lsh(num, 192)
	r.Div(r, den)
	return r.Sqrt(r)
}

// V3MintMinimums is Position.mintAmountsWithSlippage from the v3-sdk, which
// the web app uses for v3 mint and increase minimums.
func V3MintMinimums(s PoolState, r Range, desired Amounts, slippageBps int) (Amounts, error) {
	a, b := r.sqrtBounds()
	// The position the router will actually create (imprecise amount0 math).
	created := MaxLiquidityForAmounts(s.SqrtPriceX96, a, b, desired.Amount0, desired.Amount1, false)
	// token0 price as a fraction: sqrtP^2 / 2^192.
	p2 := new(big.Int).Mul(s.SqrtPriceX96, s.SqrtPriceX96)
	q192 := new(big.Int).Lsh(big.NewInt(1), 192)
	bps := big.NewInt(int64(slippageBps))
	lowerNum := new(big.Int).Mul(p2, new(big.Int).Sub(big.NewInt(10000), bps))
	upperNum := new(big.Int).Mul(p2, new(big.Int).Add(big.NewInt(10000), bps))
	den := new(big.Int).Mul(q192, big.NewInt(10000))
	sqrtLower := encodeSqrtRatioX96(lowerNum, den)
	if sqrtLower.Cmp(MinSqrtRatio) <= 0 {
		sqrtLower = new(big.Int).Add(MinSqrtRatio, big.NewInt(1))
	}
	sqrtUpper := encodeSqrtRatioX96(upperNum, den)
	if sqrtUpper.Cmp(MaxSqrtRatio) >= 0 {
		sqrtUpper = new(big.Int).Sub(MaxSqrtRatio, big.NewInt(1))
	}
	tl, err := TickAtSqrtRatio(sqrtLower)
	if err != nil {
		return Amounts{}, err
	}
	tu, err := TickAtSqrtRatio(sqrtUpper)
	if err != nil {
		return Amounts{}, err
	}
	return Amounts{
		Amount0: MintAmounts(PoolState{sqrtUpper, tu}, r, created).Amount0,
		Amount1: MintAmounts(PoolState{sqrtLower, tl}, r, created).Amount1,
	}, nil
}

func clampSqrt(v *big.Int) *big.Int {
	if v.Cmp(MinSqrtRatio) < 0 {
		return new(big.Int).Set(MinSqrtRatio)
	}
	if v.Cmp(MaxSqrtRatio) >= 0 {
		return new(big.Int).Sub(MaxSqrtRatio, big.NewInt(1))
	}
	return v
}

// shiftedSqrt returns sqrt(sqrtP^2 * (10000 +/- bps) / 10000), clamped.
func shiftedSqrt(sqrtP *big.Int, bps int, up bool) *big.Int {
	f := int64(10000 - bps)
	if up {
		f = int64(10000 + bps)
	}
	v := new(big.Int).Mul(sqrtP, sqrtP)
	v.Mul(v, big.NewInt(f))
	v.Div(v, big.NewInt(10000))
	return clampSqrt(v.Sqrt(v))
}

func maxInt(vs ...*big.Int) *big.Int {
	m := vs[0]
	for _, v := range vs[1:] {
		if v.Cmp(m) > 0 {
			m = v
		}
	}
	return m
}

// V4MintMaximums is the web app's getMintAmountsWithSlippage: the most each
// token can cost to mint liquidity anywhere in price * (1 +/- slippage).
func V4MintMaximums(s PoolState, r Range, liquidity *big.Int, slippageBps int) (Amounts, error) {
	raw := MintAmounts(s, r, liquidity)
	upSqrt, downSqrt := shiftedSqrt(s.SqrtPriceX96, slippageBps, true), shiftedSqrt(s.SqrtPriceX96, slippageBps, false)
	upTick, err := TickAtSqrtRatio(upSqrt)
	if err != nil {
		return Amounts{}, err
	}
	downTick, err := TickAtSqrtRatio(downSqrt)
	if err != nil {
		return Amounts{}, err
	}
	up := MintAmounts(PoolState{upSqrt, upTick}, r, liquidity)
	down := MintAmounts(PoolState{downSqrt, downTick}, r, liquidity)
	return Amounts{maxInt(raw.Amount0, up.Amount0, down.Amount0), maxInt(raw.Amount1, up.Amount1, down.Amount1)}, nil
}

func burnAmountsAt(sqrtP, a, b, liquidity *big.Int) Amounts {
	switch {
	case sqrtP.Cmp(a) <= 0:
		return Amounts{Amount0Delta(a, b, liquidity, false), new(big.Int)}
	case sqrtP.Cmp(b) >= 0:
		return Amounts{new(big.Int), Amount1Delta(a, b, liquidity, false)}
	default:
		return Amounts{Amount0Delta(sqrtP, b, liquidity, false), Amount1Delta(a, sqrtP, liquidity, false)}
	}
}

// BurnMinimums is the web app's getBurnMinimums: principal minimums for
// removing liquidity, worst case across price * (1 +/- slippage). Fees are
// not included.
func BurnMinimums(sqrtP *big.Int, r Range, liquidity *big.Int, slippageBps int) Amounts {
	if liquidity.Sign() <= 0 {
		return Amounts{new(big.Int), new(big.Int)}
	}
	a, b := r.sqrtBounds()
	return Amounts{
		Amount0: burnAmountsAt(shiftedSqrt(sqrtP, slippageBps, true), a, b, liquidity).Amount0,
		Amount1: burnAmountsAt(shiftedSqrt(sqrtP, slippageBps, false), a, b, liquidity).Amount1,
	}
}

// UncollectedFees is the web app's calculateUnclaimedFees: liquidity times
// the fee growth since the position's last checkpoint, modulo 2^256.
func UncollectedFees(liquidity, inside0, inside1, last0, last1 *big.Int) Amounts {
	delta := func(cur, last *big.Int) *big.Int {
		d := new(big.Int).Sub(cur, last)
		return d.Mod(d, two256)
	}
	f0 := new(big.Int).Mul(delta(inside0, last0), liquidity)
	f1 := new(big.Int).Mul(delta(inside1, last1), liquidity)
	return Amounts{f0.Div(f0, q128), f1.Div(f1, q128)}
}

// NearestUsableTick is the v3-sdk nearestUsableTick.
func NearestUsableTick(tick, spacing int) int {
	q := float64(tick) / float64(spacing)
	// Math.round: halves round toward +infinity.
	n := int(q)
	if frac := q - float64(n); frac >= 0.5 {
		n++
	} else if frac < -0.5 {
		n--
	}
	rounded := n * spacing
	if rounded < MinTick {
		return rounded + spacing
	}
	if rounded > MaxTick {
		return rounded - spacing
	}
	return rounded
}

// FullRange is the widest usable range for a tick spacing.
func FullRange(spacing int) Range {
	return Range{(MinTick / spacing) * spacing, (MaxTick / spacing) * spacing}
}

// SqrtPriceForRawPrice returns the Q64.96 sqrt price for a raw (base-unit)
// token1-per-token0 price.
func SqrtPriceForRawPrice(p *big.Rat) (*big.Int, error) {
	if p.Sign() <= 0 {
		return nil, errors.New("price must be positive")
	}
	v := clampSqrt(encodeSqrtRatioX96(p.Num(), p.Denom()))
	return v, nil
}

// RawPrice converts a human token1-per-token0 price to base units.
func RawPrice(human *big.Rat, decimals0, decimals1 int) *big.Rat {
	scale := new(big.Rat).SetFrac(pow10(decimals1), pow10(decimals0))
	return new(big.Rat).Mul(human, scale)
}

// HumanPrice converts a sqrt price to a human token1-per-token0 price.
func HumanPrice(sqrtP *big.Int, decimals0, decimals1 int) *big.Rat {
	raw := new(big.Rat).SetFrac(new(big.Int).Mul(sqrtP, sqrtP), new(big.Int).Lsh(big.NewInt(1), 192))
	return raw.Mul(raw, new(big.Rat).SetFrac(pow10(decimals0), pow10(decimals1)))
}

// TickForHumanPrice returns the tick at or below a human price.
func TickForHumanPrice(human *big.Rat, decimals0, decimals1 int) (int, error) {
	sqrtP, err := SqrtPriceForRawPrice(RawPrice(human, decimals0, decimals1))
	if err != nil {
		return 0, err
	}
	return TickAtSqrtRatio(sqrtP)
}

// PercentRange returns the usable range around the current price spanning
// price * (1 - bps/10000) to price * (1 + bps/10000), each bound rounded to
// the nearest usable tick like the web app's range inputs.
func PercentRange(sqrtP *big.Int, bps, spacing int) (Range, error) {
	if bps <= 0 || bps >= 10000 {
		return Range{}, errors.New("range percent must be between 0.01 and 99.99")
	}
	lowerTick, err := TickAtSqrtRatio(shiftedSqrt(sqrtP, bps, false))
	if err != nil {
		return Range{}, err
	}
	upperTick, err := TickAtSqrtRatio(shiftedSqrt(sqrtP, bps, true))
	if err != nil {
		return Range{}, err
	}
	return UsableRange(lowerTick, upperTick, spacing)
}

// UsableRange snaps raw ticks to the spacing, widening a collapsed range by
// one spacing so it stays non-empty.
func UsableRange(lowerTick, upperTick, spacing int) (Range, error) {
	lower, upper := NearestUsableTick(lowerTick, spacing), NearestUsableTick(upperTick, spacing)
	if lower >= upper {
		upper = lower + spacing
		if upper > MaxTick {
			lower, upper = lower-spacing, lower
		}
	}
	return NewRange(lower, upper, spacing)
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }
