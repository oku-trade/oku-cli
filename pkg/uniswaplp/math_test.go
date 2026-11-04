package uniswaplp

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Golden vectors produced by @uniswap/v3-sdk (TickMath, nearestUsableTick,
// Position.fromAmounts with full precision, mintAmountsWithSlippage,
// burnAmountsWithSlippage and NonfungiblePositionManager.addCallParameters)
// for a 18/6 decimals 0.3% pool with 50 bps slippage.

func n(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return v
}

func TestTickMathMatchesSDK(t *testing.T) {
	for tick, want := range map[int]string{
		-887272: "4295128739", -276324: "79228267247129223624114", -1: "79224201403219477170569942574", 0: "79228162514264337593543950336",
		1: "79232123823359799118286999568", 200: "80024378775772204256025656563", 46054: "792281450588003167884250659085",
		887272: "1461446703485210103287273052203988822378723970342",
	} {
		got, err := SqrtRatioAtTick(tick)
		if err != nil || got.String() != want {
			t.Errorf("SqrtRatioAtTick(%d) = %v, %v; want %s", tick, got, err, want)
		}
	}
	for sqrt, want := range map[string]int{
		"4295128739": -887272, "79228162514264337593543950336": 0,
		"1461446703485210103287273052203988822378723970341": 887271, "3543191142285914205922034323214": 76012,
	} {
		if got, err := TickAtSqrtRatio(n(sqrt)); err != nil || got != want {
			t.Errorf("TickAtSqrtRatio(%s) = %d, %v; want %d", sqrt, got, err, want)
		}
	}
	if _, err := SqrtRatioAtTick(MaxTick + 1); err == nil {
		t.Error("tick above MaxTick accepted")
	}
	for _, c := range [][3]int{{-887272, 60, -887220}, {887272, 200, 887200}, {-5, 10, 0}, {5, 10, 10}, {15, 10, 20}, {-15, 10, -10}} {
		if got := NearestUsableTick(c[0], c[1]); got != c[2] {
			t.Errorf("NearestUsableTick(%d, %d) = %d; want %d", c[0], c[1], got, c[2])
		}
	}
}

func TestV3MintAndBurnMatchSDK(t *testing.T) {
	cases := []struct {
		tick, lower, upper                  int
		liquidity, mint0, mint1, min0, min1 string
		burn0, burn1, pos0, pos1            string
	}{
		{-200010, -201000, -199020, "940103370731410", "999999999999999522", "2061153589", "948425436948174186", "1954318311", "948425436948174185", "1954318310", "999999999999999521", "2061153588"},
		{-200010, -199020, -198000, "959467153800650", "999999999999999351", "0", "999999999999999351", "0", "999999999999999350", "0", "999999999999999350", "0"},
		{-200010, -202020, -201000, "1163750192301316", "0", "2500000000", "0", "2500000000", "0", "2499999999", "0", "2499999999"},
		{10, -60, 60, "715214478338", "1784821251", "2500000000", "4352787", "708827837", "4352786", "708827836", "1784821250", "2499999999"},
	}
	for _, c := range cases {
		sqrt, err := SqrtRatioAtTick(c.tick)
		if err != nil {
			t.Fatal(err)
		}
		s := PoolState{SqrtPriceX96: sqrt, Tick: c.tick}
		r, err := NewRange(c.lower, c.upper, 60)
		if err != nil {
			t.Fatal(err)
		}
		a, b := r.sqrtBounds()
		liq := MaxLiquidityForAmounts(sqrt, a, b, n("1000000000000000000"), n("2500000000"), true)
		if liq.String() != c.liquidity {
			t.Fatalf("tick %d: liquidity %s; want %s", c.tick, liq, c.liquidity)
		}
		mint := MintAmounts(s, r, liq)
		mins, err := V3MintMinimums(s, r, mint, 50)
		if err != nil {
			t.Fatal(err)
		}
		burn := BurnMinimums(sqrt, r, liq, 50)
		pos := PositionAmounts(s, r, liq)
		for name, got := range map[string][2]string{
			"mint": {mint.Amount0.String() + "/" + mint.Amount1.String(), c.mint0 + "/" + c.mint1},
			"min":  {mins.Amount0.String() + "/" + mins.Amount1.String(), c.min0 + "/" + c.min1},
			"burn": {burn.Amount0.String() + "/" + burn.Amount1.String(), c.burn0 + "/" + c.burn1},
			"pos":  {pos.Amount0.String() + "/" + pos.Amount1.String(), c.pos0 + "/" + c.pos1},
		} {
			if got[0] != got[1] {
				t.Errorf("tick %d range [%d,%d] %s = %s; want %s", c.tick, c.lower, c.upper, name, got[0], got[1])
			}
		}
	}
}

func TestV3MintCalldataMatchesSDK(t *testing.T) {
	const want = "88316456000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000bb8fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffceed8fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffcf6940000000000000000000000000000000000000000000000000de0b6b3a763fe22000000000000000000000000000000000000000000000000000000007adab5350000000000000000000000000000000000000000000000000d297be8f13d956a00000000000000000000000000000000000000000000000000000000747c87e700000000000000000000000000000000000000000000000000000000000000aa000000000000000000000000000000000000000000000000000000006553f100"
	got := V3Mint(V3MintParams{
		Token0: common.HexToAddress("0x01"), Token1: common.HexToAddress("0x02"), Fee: big.NewInt(3000),
		TickLower: big.NewInt(-201000), TickUpper: big.NewInt(-199020),
		Amount0Desired: n("999999999999999522"), Amount1Desired: n("2061153589"),
		Amount0Min: n("948425436948174186"), Amount1Min: n("1954318311"),
		Recipient: common.HexToAddress("0xaa"), Deadline: big.NewInt(1700000000),
	})
	if hex.EncodeToString(got) != want {
		t.Fatalf("calldata\n got %x\nwant %s", got, want)
	}
}

func TestUncollectedFeesWrap(t *testing.T) {
	// feeGrowthInside below the last checkpoint wraps modulo 2^256, as the
	// position manager's unchecked subtraction does.
	last := new(big.Int).Sub(two256, q128) // checkpoint at -1 * 2^128
	got := UncollectedFees(big.NewInt(5), q128, big.NewInt(0), last, big.NewInt(0))
	if got.Amount0.Int64() != 10 || got.Amount1.Sign() != 0 {
		t.Fatalf("fees = %s/%s; want 10/0", got.Amount0, got.Amount1)
	}
}
