// Package pnl computes average-cost positions and profit and loss from the
// swaps an agent made, in exact rational arithmetic. It does no I/O.
package pnl

import (
	"math/big"
	"sort"
	"strings"
)

// Native identifies the chain's native asset in Swap and Position.
const Native = "native"

// Swap is one executed swap. Amounts are raw units of their token (wei for
// native).
type Swap struct {
	ID       string
	InToken  string
	OutToken string
	AmountIn *big.Int
	// AmountOut is what the recipient received. Approximate marks a value
	// that is the slippage floor rather than the measured fill.
	AmountOut   *big.Int
	Approximate bool
	// Gas is the native wei paid for the swap's transactions, nil if unknown.
	Gas *big.Int
	// InValue is the native wei value of the input of a token -> token swap
	// at the time of the swap, nil if unknown. It is ignored otherwise.
	InValue *big.Rat
}

// Position is the running state of one token. Quantities are raw units and
// values native wei.
type Position struct {
	Token string
	// Held is the tracked quantity: what the agent's own swaps bought and
	// have not sold.
	Held *big.Rat
	// Cost is the remaining cost basis of Held.
	Cost *big.Rat
	// Realized is proceeds minus average cost of every tracked sale, net of
	// the swap gas attributed to this token.
	Realized *big.Rat
	// Gas is the native wei paid by the swaps attributed to this token.
	Gas *big.Int
	// GasUnknown counts attributed swaps whose gas is not recorded.
	GasUnknown int
	Bought     *big.Rat
	Sold       *big.Rat
	// UntrackedSold is sold quantity the agent's swaps never bought (it came
	// from outside the agent). Its proceeds are not in Realized.
	UntrackedSold *big.Rat
	// UntrackedBought is received quantity whose cost is unknown, because a
	// token -> token input had no price and no tracked basis. It is not in Held.
	UntrackedBought *big.Rat
	// Approximate is set when any swap of this token used the slippage floor
	// or an estimated input value.
	Approximate bool
	Swaps       int
}

func newPosition(token string) *Position {
	return &Position{Token: token, Held: new(big.Rat), Cost: new(big.Rat), Realized: new(big.Rat), Gas: new(big.Int), Bought: new(big.Rat), Sold: new(big.Rat), UntrackedSold: new(big.Rat), UntrackedBought: new(big.Rat)}
}

// AvgCost returns native wei per raw unit of Held, or nil when nothing is held.
func (p *Position) AvgCost() *big.Rat {
	if p == nil || p.Held.Sign() <= 0 {
		return nil
	}
	return new(big.Rat).Quo(p.Cost, p.Held)
}

// BasisOf returns the native wei cost of size raw units at the average cost,
// or nil when nothing is held.
func (p *Position) BasisOf(size *big.Int) *big.Rat {
	avg := p.AvgCost()
	if avg == nil {
		return nil
	}
	return avg.Mul(avg, new(big.Rat).SetInt(size))
}

// Unrealized returns Held valued at price (native wei per raw unit) minus
// Cost. It is nil when price is nil, except that nothing held is worth 0.
func (p *Position) Unrealized(price *big.Rat) *big.Rat {
	if p.Held.Sign() == 0 {
		return new(big.Rat)
	}
	if price == nil {
		return nil
	}
	v := new(big.Rat).Mul(p.Held, price)
	return v.Sub(v, p.Cost)
}

// Book holds every token's position.
type Book struct {
	positions map[string]*Position
}

// Key normalizes a token for lookups: lower-case, with native aliases mapped
// to Native.
func Key(token string, nativeAliases ...string) string {
	t := strings.ToLower(strings.TrimSpace(token))
	if t == Native {
		return Native
	}
	for _, a := range nativeAliases {
		if a != "" && strings.EqualFold(t, a) {
			return Native
		}
	}
	return t
}

func (b *Book) get(token string) *Position {
	p := b.positions[token]
	if p == nil {
		p = newPosition(token)
		b.positions[token] = p
	}
	return p
}

// Position returns token's position, or nil if no swap touched it.
func (b *Book) Position(token string) *Position { return b.positions[Key(token)] }

// Positions returns every non-native position sorted by token.
func (b *Book) Positions() []*Position {
	out := make([]*Position, 0, len(b.positions))
	for _, p := range b.positions {
		out = append(out, p)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Token < out[k].Token })
	return out
}

// Compute replays swaps in order with the average-cost method. Token keys
// must already be normalized with Key. Swaps with invalid amounts, or with
// native or the same token on both sides, are skipped.
func Compute(swaps []Swap) *Book {
	b := &Book{positions: map[string]*Position{}}
	for _, s := range swaps {
		if s.AmountIn == nil || s.AmountOut == nil || s.AmountIn.Sign() <= 0 || s.AmountOut.Sign() <= 0 || s.InToken == s.OutToken || s.InToken == "" || s.OutToken == "" {
			continue
		}
		in, out := new(big.Rat).SetInt(s.AmountIn), new(big.Rat).SetInt(s.AmountOut)
		switch {
		case s.InToken == Native:
			p := b.get(s.OutToken)
			p.buy(out, in)
			p.touch(s)
		case s.OutToken == Native:
			p := b.get(s.InToken)
			p.sell(in, out)
			p.touch(s)
		default:
			src, dst := b.get(s.InToken), b.get(s.OutToken)
			src.Swaps++
			if s.InValue != nil {
				src.sell(in, s.InValue)
				dst.buy(out, s.InValue)
			} else {
				// No price for the input: carry its tracked basis over to the
				// output without realizing. Output bought with untracked input
				// has unknown cost.
				tracked, carried := src.carry(in)
				got := ratProportion(out, in, tracked)
				if got.Sign() > 0 {
					dst.buy(got, carried)
				}
				dst.UntrackedBought.Add(dst.UntrackedBought, new(big.Rat).Sub(out, got))
				src.Approximate, dst.Approximate = true, true
			}
			dst.touch(s)
			if s.Approximate {
				src.Approximate = true
			}
		}
	}
	for _, p := range b.positions {
		p.Realized.Sub(p.Realized, new(big.Rat).SetInt(p.Gas))
	}
	return b
}

// ratProportion returns v * part / whole.
func ratProportion(v, whole, part *big.Rat) *big.Rat {
	if whole.Sign() == 0 {
		return new(big.Rat)
	}
	r := new(big.Rat).Mul(v, part)
	return r.Quo(r, whole)
}

func minRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) < 0 {
		return new(big.Rat).Set(a)
	}
	return new(big.Rat).Set(b)
}

func (p *Position) touch(s Swap) {
	p.Swaps++
	if s.Approximate {
		p.Approximate = true
	}
	if s.Gas == nil {
		p.GasUnknown++
	} else {
		p.Gas.Add(p.Gas, s.Gas)
	}
}

func (p *Position) buy(qty, cost *big.Rat) {
	p.Held.Add(p.Held, qty)
	p.Cost.Add(p.Cost, cost)
	p.Bought.Add(p.Bought, qty)
}

// carry removes qty at the average cost without realizing and returns the
// tracked part of qty and its cost.
func (p *Position) carry(qty *big.Rat) (*big.Rat, *big.Rat) {
	p.Sold.Add(p.Sold, qty)
	tracked := minRat(qty, p.Held)
	p.UntrackedSold.Add(p.UntrackedSold, new(big.Rat).Sub(qty, tracked))
	cost := new(big.Rat)
	if avg := p.AvgCost(); avg != nil {
		cost.Mul(avg, tracked)
	}
	p.Held.Sub(p.Held, tracked)
	p.Cost.Sub(p.Cost, cost)
	if p.Held.Sign() == 0 {
		p.Cost.SetInt64(0)
	}
	return tracked, cost
}

// sell removes qty at the average cost. Only the tracked part (up to Held)
// realizes, with its proportional share of proceeds.
func (p *Position) sell(qty, proceeds *big.Rat) {
	p.Sold.Add(p.Sold, qty)
	tracked := minRat(qty, p.Held)
	if untracked := new(big.Rat).Sub(qty, tracked); untracked.Sign() > 0 {
		p.UntrackedSold.Add(p.UntrackedSold, untracked)
	}
	if tracked.Sign() == 0 {
		return
	}
	cost := new(big.Rat).Mul(p.AvgCost(), tracked)
	p.Realized.Add(p.Realized, ratProportion(proceeds, qty, tracked))
	p.Realized.Sub(p.Realized, cost)
	p.Held.Sub(p.Held, tracked)
	p.Cost.Sub(p.Cost, cost)
	if p.Held.Sign() == 0 {
		p.Cost.SetInt64(0)
	}
}

// Decimal renders r with at most places fraction digits, rounded half away
// from zero, without trailing zeros. Nil renders as "".
func Decimal(r *big.Rat, places int) string {
	if r == nil {
		return ""
	}
	s := r.FloatString(places)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		return "0"
	}
	return s
}

// Scale returns r / 10^decimals.
func Scale(r *big.Rat, decimals int) *big.Rat {
	if r == nil {
		return nil
	}
	return new(big.Rat).Quo(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)))
}
