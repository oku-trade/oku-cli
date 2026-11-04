package uniswaplp

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func testPool(t *testing.T, v Version, token0 string) (Deployment, Pool) {
	t.Helper()
	d, err := Lookup(1, v)
	if err != nil {
		t.Fatal(err)
	}
	sqrt, err := SqrtRatioAtTick(-200010)
	if err != nil {
		t.Fatal(err)
	}
	return d, Pool{
		Version: v, ChainID: 1, Fee: 3000, TickSpacing: 60, SqrtPriceX96: sqrt, Tick: -200010, Liquidity: n("1000000000000000000"),
		Token0: Token{Address: token0, Symbol: "A", Decimals: 18},
		Token1: Token{Address: "0x0000000000000000000000000000000000000002", Symbol: "B", Decimals: 6},
	}
}

func templates(t *testing.T, d Deployment, p Plan, legs []Allowances) []Template {
	t.Helper()
	action, err := p.Calldata()
	if err != nil {
		t.Fatal(err)
	}
	var ts []Template
	for _, c := range Approvals(d, 1700000000, legs) {
		ts = append(ts, Template{To: c.To, Data: c.Data, Value: c.Value})
	}
	return append(ts, Template{To: action.To, Data: action.Data, Value: action.Value})
}

func TestOpenPlanVerifiesAndRefusesTampering(t *testing.T) {
	owner := common.HexToAddress("0xaa")
	for _, v := range []Version{V3, V4} {
		d, pool := testPool(t, v, "0x0000000000000000000000000000000000000001")
		p, err := PlanOpen(d, owner, pool, Range{-201000, -199020}, DepositInput{Side: Side0, Amount: n("1000000000000000000")}, 50, 1700001200)
		if err != nil {
			t.Fatal(err)
		}
		spend, err := p.Spend()
		if err != nil {
			t.Fatal(err)
		}
		zero := new(big.Int)
		legs := []Allowances{
			{Token: pool.Token0.Address, Required: spend.Amount0, ERC20: zero, Permit2Amount: zero},
			{Token: pool.Token1.Address, Required: spend.Amount1, ERC20: zero, Permit2Amount: zero},
		}
		ts := templates(t, d, p, legs)
		if err := VerifyTemplates(p, d, ts); err != nil {
			t.Fatalf("%s: honest templates refused: %v", v, err)
		}
		// A different spender in the first approval must be refused.
		bad := append([]Template(nil), ts...)
		bad[0].Data = strings.Replace(bad[0].Data, strings.ToLower(d.Manager.Hex()[2:]), strings.Repeat("1", 40), 1)
		bad[0].Data = strings.Replace(bad[0].Data, strings.ToLower(d.Permit2.Hex()[2:]), strings.Repeat("1", 40), 1)
		if VerifyTemplates(p, d, bad) == nil {
			t.Errorf("%s: approval to a foreign spender accepted", v)
		}
		// Edited action bytes must be refused.
		bad = append([]Template(nil), ts...)
		last := &bad[len(bad)-1]
		last.Data = last.Data[:len(last.Data)-2] + "ff"
		if VerifyTemplates(p, d, bad) == nil {
			t.Errorf("%s: edited action accepted", v)
		}
		// An extra call smuggled before the action must be refused.
		bad = append([]Template{{To: "0x0000000000000000000000000000000000000003", Data: "0x095ea7b3", Value: zero}}, ts...)
		if VerifyTemplates(p, d, bad) == nil {
			t.Errorf("%s: extra call accepted", v)
		}
	}
}

func TestV4NativeOpenSendsValueWithoutApproval(t *testing.T) {
	d, pool := testPool(t, V4, "0x0000000000000000000000000000000000000000")
	p, err := PlanOpen(d, common.HexToAddress("0xaa"), pool, Range{-201000, -199020}, DepositInput{Side: Side0, Amount: n("1000000000000000000")}, 50, 1700001200)
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.Calldata()
	if err != nil {
		t.Fatal(err)
	}
	if c.Value.String() != p.Limit0 {
		t.Fatalf("native value %s; want the token0 maximum %s", c.Value, p.Limit0)
	}
}

func TestDecreasePlanRejectsBadPercent(t *testing.T) {
	d, pool := testPool(t, V3, "0x0000000000000000000000000000000000000001")
	pos := Position{TokenID: "7", Pool: pool, Range: Range{-201000, -199020}, Liquidity: n("1000"), Fees: Amounts{new(big.Int), new(big.Int)}}
	for _, bps := range []int{0, -1, 10001} {
		if _, err := PlanDecrease(d, common.HexToAddress("0xaa"), pos, bps, false, 50, 1700001200); err == nil {
			t.Errorf("bps %d accepted", bps)
		}
	}
	if _, err := PlanOpen(d, common.HexToAddress("0xaa"), pool, Range{-201000, -199020}, DepositInput{Side: Side0, Amount: n("1")}, MaxSlippageBps+1, 1700001200); err == nil {
		t.Error("slippage above the cap accepted")
	}
}
