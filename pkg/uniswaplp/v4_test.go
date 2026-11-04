package uniswaplp

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Vectors from @uniswap/v4-sdk 2.4.4 for the same 18/6 decimals 0.3% pool
// (tick spacing 60, no hooks) at tick -200010, range [-201000, -199020].

func TestV4PoolIDMatchesSDK(t *testing.T) {
	for name, c := range map[string]struct {
		c0   string
		want string
	}{
		"erc20":  {"0x0000000000000000000000000000000000000001", "f6a117501d7c06f988e5cb96441dff2b3bc20bc7c52bc943e66da6e63b93c97c"},
		"native": {"0x0000000000000000000000000000000000000000", "a1dbc7676a437024e0e6f3488928881dfc2e7a44f133e8cbeec82b61386e94e2"},
	} {
		k := PoolKey{Currency0: common.HexToAddress(c.c0), Currency1: common.HexToAddress("0x02"), Fee: big.NewInt(3000), TickSpacing: big.NewInt(60)}
		if got := hex.EncodeToString(k.PoolID().Bytes()); got != c.want {
			t.Errorf("%s pool id %s; want %s", name, got, c.want)
		}
	}
}

func TestV4MintMatchesSDK(t *testing.T) {
	sqrt, err := SqrtRatioAtTick(-200010)
	if err != nil {
		t.Fatal(err)
	}
	s := PoolState{SqrtPriceX96: sqrt, Tick: -200010}
	r, err := NewRange(-201000, -199020, 60)
	if err != nil {
		t.Fatal(err)
	}
	liq := n("940103370731410")
	max, err := V4MintMaximums(s, r, liq, 50)
	if err != nil {
		t.Fatal(err)
	}
	if max.Amount0.String() != "1051962829284203057" || max.Amount1.String() != "2167722111" {
		t.Fatalf("maximums %s/%s; want 1051962829284203057/2167722111", max.Amount0, max.Amount1)
	}
	// The MINT_POSITION params are the first action of the SDK plan; the
	// encoded plan must carry exactly those bytes.
	const mintParams = "000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000bb8000000000000000000000000000000000000000000000000000000000000003c0000000000000000000000000000000000000000000000000000000000000000fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffceed8fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffcf69400000000000000000000000000000000000000000000000000035704df76ef920000000000000000000000000000000000000000000000000e99529ea4665a31000000000000000000000000000000000000000000000000000000008134d07f00000000000000000000000000000000000000000000000000000000000000aa00000000000000000000000000000000000000000000000000000000000001800000000000000000000000000000000000000000000000000000000000000000"
	k := PoolKey{Currency0: common.HexToAddress("0x01"), Currency1: common.HexToAddress("0x02"), Fee: big.NewInt(3000), TickSpacing: big.NewInt(60)}
	got := hex.EncodeToString(V4Mint(k, r, liq, max, common.HexToAddress("0xaa"), 1700000000))
	if !strings.Contains(got, mintParams) {
		t.Fatal("v4 mint calldata does not contain the SDK MINT_POSITION params")
	}
}

func TestV4BurnMinimumsMatchSDK(t *testing.T) {
	sqrt, err := SqrtRatioAtTick(-200010)
	if err != nil {
		t.Fatal(err)
	}
	got := BurnMinimums(sqrt, Range{-201000, -199020}, n("940103370731410"), 50)
	if got.Amount0.String() != "948425436948174185" || got.Amount1.String() != "1954318310" {
		t.Fatalf("burn minimums %s/%s", got.Amount0, got.Amount1)
	}
}
