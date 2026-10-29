package pnl

import (
	"math/big"
	"testing"
)

const tok, tok2 = "0xaaaa", "0xbbbb"

func n(v int64) *big.Int { return big.NewInt(v) }

func rat(a, b int64) *big.Rat { return big.NewRat(a, b) }

func eq(t *testing.T, what string, got, want *big.Rat) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Fatalf("%s: got %v want %v", what, got, want)
		}
		return
	}
	if got.Cmp(want) != 0 {
		t.Fatalf("%s: got %s want %s", what, got.RatString(), want.RatString())
	}
}

func buy(qtyOut, cost int64) Swap {
	return Swap{InToken: Native, OutToken: tok, AmountIn: n(cost), AmountOut: n(qtyOut), Gas: n(1)}
}

func sell(qtyIn, proceeds int64) Swap {
	return Swap{InToken: tok, OutToken: Native, AmountIn: n(qtyIn), AmountOut: n(proceeds), Gas: n(1)}
}

func TestBuysOnly(t *testing.T) {
	p := Compute([]Swap{buy(100, 1000)}).Position(tok)
	eq(t, "held", p.Held, rat(100, 1))
	eq(t, "cost", p.Cost, rat(1000, 1))
	eq(t, "avg", p.AvgCost(), rat(10, 1))
	eq(t, "realized", p.Realized, rat(-1, 1))
	eq(t, "unrealized", p.Unrealized(rat(12, 1)), rat(200, 1))
	if p.Unrealized(nil) != nil {
		t.Fatal("unrealized without price must be unknown")
	}
	if p.Gas.Int64() != 1 || p.Approximate {
		t.Fatalf("gas %v approx %v", p.Gas, p.Approximate)
	}
}

func TestBuyThenPartialSell(t *testing.T) {
	p := Compute([]Swap{buy(100, 1000), sell(40, 600)}).Position(tok)
	eq(t, "held", p.Held, rat(60, 1))
	eq(t, "cost", p.Cost, rat(600, 1))
	eq(t, "realized", p.Realized, rat(600-400-2, 1))
	eq(t, "unrealized", p.Unrealized(rat(10, 1)), new(big.Rat))
}

func TestFullExit(t *testing.T) {
	p := Compute([]Swap{buy(100, 1000), sell(100, 900)}).Position(tok)
	eq(t, "held", p.Held, new(big.Rat))
	eq(t, "cost", p.Cost, new(big.Rat))
	eq(t, "realized", p.Realized, rat(-102, 1))
	if p.AvgCost() != nil {
		t.Fatal("avg cost of nothing held")
	}
	eq(t, "unrealized without price", p.Unrealized(nil), new(big.Rat))
}

func TestAverageCostOfSeveralBuys(t *testing.T) {
	p := Compute([]Swap{buy(100, 1000), buy(100, 3000), sell(50, 2500)}).Position(tok)
	eq(t, "avg", p.AvgCost(), rat(20, 1))
	eq(t, "held", p.Held, rat(150, 1))
	eq(t, "cost", p.Cost, rat(3000, 1))
	eq(t, "realized", p.Realized, rat(2500-1000-3, 1))
	eq(t, "basis of 15", p.BasisOf(n(15)), rat(300, 1))
}

func TestTokenToToken(t *testing.T) {
	b := Compute([]Swap{
		buy(100, 1000),
		{InToken: tok, OutToken: tok2, AmountIn: n(50), AmountOut: n(10), InValue: rat(800, 1), Gas: n(3)},
	})
	src, dst := b.Position(tok), b.Position(tok2)
	eq(t, "src held", src.Held, rat(50, 1))
	eq(t, "src realized", src.Realized, rat(800-500-1, 1))
	eq(t, "dst held", dst.Held, rat(10, 1))
	eq(t, "dst cost", dst.Cost, rat(800, 1))
	eq(t, "dst realized", dst.Realized, rat(-3, 1))
	if src.Approximate || dst.Approximate {
		t.Fatal("priced token swap is exact")
	}
}

func TestTokenToTokenWithoutPriceCarriesBasis(t *testing.T) {
	b := Compute([]Swap{
		buy(100, 1000),
		{InToken: tok, OutToken: tok2, AmountIn: n(150), AmountOut: n(30)},
	})
	src, dst := b.Position(tok), b.Position(tok2)
	eq(t, "src held", src.Held, new(big.Rat))
	eq(t, "src untracked", src.UntrackedSold, rat(50, 1))
	eq(t, "src realized", src.Realized, rat(-1, 1))
	eq(t, "dst held", dst.Held, rat(20, 1))
	eq(t, "dst cost", dst.Cost, rat(1000, 1))
	eq(t, "dst untracked", dst.UntrackedBought, rat(10, 1))
	if !src.Approximate || !dst.Approximate || dst.GasUnknown != 1 {
		t.Fatalf("approx %v %v gas unknown %d", src.Approximate, dst.Approximate, dst.GasUnknown)
	}
}

func TestLegacyMinOutIsApproximate(t *testing.T) {
	s := buy(100, 1000)
	s.Approximate, s.Gas = true, nil
	p := Compute([]Swap{s}).Position(tok)
	if !p.Approximate || p.GasUnknown != 1 || p.Gas.Sign() != 0 {
		t.Fatalf("legacy: %+v", p)
	}
	eq(t, "avg", p.AvgCost(), rat(10, 1))
}

func TestSellOfUntrackedTokensDoesNotRealize(t *testing.T) {
	p := Compute([]Swap{buy(10, 100), sell(40, 800)}).Position(tok)
	eq(t, "untracked", p.UntrackedSold, rat(30, 1))
	eq(t, "realized", p.Realized, rat(200-100-2, 1))
	eq(t, "held", p.Held, new(big.Rat))
}

func TestInvalidSwapsSkipped(t *testing.T) {
	b := Compute([]Swap{{InToken: Native, OutToken: tok, AmountIn: n(0), AmountOut: n(5)}, {InToken: tok, OutToken: tok, AmountIn: n(1), AmountOut: n(1)}})
	if len(b.Positions()) != 0 {
		t.Fatalf("positions: %+v", b.Positions())
	}
}

func TestKeyAndFormatting(t *testing.T) {
	if Key(" 0xABC ") != "0xabc" || Key("0x471EcE3750Da237f93B8E339c536989b8978a438", "0x471ece3750da237f93b8e339c536989b8978a438") != Native || Key("NATIVE") != Native {
		t.Fatal("key")
	}
	if Decimal(rat(-1, 3), 4) != "-0.3333" || Decimal(rat(-1, 100000), 2) != "0" || Decimal(rat(5, 2), 6) != "2.5" || Decimal(nil, 2) != "" {
		t.Fatal("decimal")
	}
	eq(t, "scale", Scale(rat(1500000, 1), 6), rat(3, 2))
}
