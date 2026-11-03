package lending

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	loopOwner = common.HexToAddress("0x1111111111111111111111111111111111111111")
	loanTok   = common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	collTok   = common.HexToAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
)

type warrant struct {
	Nonce           *big.Int
	ValidBefore     *big.Int
	ValidAfter      *big.Int
	VerifyingSigner common.Address
	Signature       []byte
}

func fill(t *testing.T, sell, buy, recipient common.Address, amount *big.Int) RouterTrade {
	t.Helper()
	data, err := loopABI.Pack("fillQuoteTokenToToken", sell, buy, common.Address{9}, common.Address{9}, []byte{1, 2}, amount, new(big.Int).Div(amount, big.NewInt(1000)), recipient, warrant{big.NewInt(1), big.NewInt(2_000_000_000), new(big.Int), common.Address{7}, []byte{3}})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := DecodeRouterTrade(data)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func morphoLoop(t *testing.T) (MorphoDeployment, MarketParams, common.Address, Plan) {
	t.Helper()
	d, err := Morpho(1)
	if err != nil {
		t.Fatal(err)
	}
	router, err := OkuRouter(1)
	if err != nil {
		t.Fatal(err)
	}
	mp := MarketParams{LoanToken: loanTok, CollateralToken: collTok, Oracle: common.Address{3}, Irm: common.Address{4}, Lltv: big.NewInt(860_000_000_000_000_000)}
	id := mp.ID()
	plan := Plan{Protocol: "morpho", Chain: 1, Owner: loopOwner, MarketID: &id, Tokens: []common.Address{loanTok, collTok}, Router: &router}
	return d, mp, router, plan
}

// bundle decodes a Bundler3 multicall into (to, selector-name) and the
// flash callback, so the test pins the structure the builders emit.
func outline(t *testing.T, c Call) []string {
	t.Helper()
	var out []string
	var walk func(data []byte)
	walk = func(data []byte) {
		var entries []bundleEntry
		d, err := Decode(c.To, data)
		if err != nil {
			t.Fatal(err)
		}
		abi.ConvertType(d.Args[0], &entries)
		for _, e := range entries {
			m, err := Decode(e.To, e.Data)
			if err != nil {
				t.Fatal(err)
			}
			name := m.Method
			if e.SkipRevert {
				name += "?"
			}
			out = append(out, name)
			if m.Method == "morphoFlashLoan" {
				cb := m.Args[2].([]byte)
				if crypto.Keccak256Hash(cb) != common.Hash(e.CallbackHash) {
					t.Fatal("callback hash does not bind the callback")
				}
				out = append(out, "{")
				walk(append(append([]byte{}, morphoABI.Methods["multicall"].ID...), cb...))
				out = append(out, "}")
			}
		}
	}
	walk(c.Data)
	return out
}

func TestMorphoLoopIncrease(t *testing.T) {
	d, mp, router, plan := morphoLoop(t)
	borrow := big.NewInt(1_000_000_000)
	s := Swap{Router: router, Trade: fill(t, loanTok, collTok, d.GeneralAdapter1, borrow), MinOut: big.NewInt(300_000_000_000_000_000)}
	c, err := d.LoopIncrease(mp, nil, borrow, big.NewInt(1), s, loopOwner)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(outline(t, c), " ")
	want := "morphoFlashLoan { erc20Transfer approve loop:fillQuoteTokenToToken morphoSupplyCollateral morphoSupplyCollateral? morphoBorrow } erc20Transfer erc20Transfer"
	if got != want {
		t.Fatalf("bundle\n got %s\nwant %s", got, want)
	}
	id := mp.ID()
	w := Want{}.Borrow(id, borrow).Supply(id, s.MinOut).SupplyRest(id).Sell(loanTok, collTok, borrow).Flash()
	if err := plan.Check([]Call{c}, w); err != nil {
		t.Fatal(err)
	}
	// A fill paying anyone but the adapter is refused at build and verify.
	bad := s
	bad.Trade = fill(t, loanTok, collTok, common.Address{0xba, 0xd}, borrow)
	if _, err := d.LoopIncrease(mp, nil, borrow, big.NewInt(1), bad, loopOwner); err == nil {
		t.Fatal("built a loop paying a stranger")
	}
	// Open: the deposit is pulled first and supplied with the swap minimum.
	dep := big.NewInt(1_000_000_000_000_000_000)
	o, err := d.LoopIncrease(mp, dep, borrow, big.NewInt(1), s, loopOwner)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(outline(t, o), " "); got != "erc20TransferFrom "+want {
		t.Fatalf("open bundle %s", got)
	}
	ow := Want{}.Pull(collTok, dep).Borrow(id, borrow).Supply(id, new(big.Int).Add(dep, s.MinOut)).SupplyRest(id).Sell(loanTok, collTok, borrow).Flash()
	if err := plan.Check([]Call{o}, ow); err != nil {
		t.Fatal(err)
	}
	// A plan without the router cannot carry the swap.
	plan.Router = nil
	if plan.Check([]Call{c}, w) == nil {
		t.Fatal("swap verified without a router in scope")
	}
}

func TestMorphoLoopDecreaseAndUnwind(t *testing.T) {
	d, mp, router, plan := morphoLoop(t)
	id := mp.ID()
	flash := big.NewInt(500_000_000)
	withdraw := big.NewInt(400_000_000_000_000_000)
	sell := big.NewInt(200_000_000_000_000_000)
	s := Swap{Router: router, Trade: fill(t, collTok, loanTok, d.GeneralAdapter1, sell), MinOut: big.NewInt(501_000_000)}
	// Partial: repay by assets.
	c, err := d.LoopDecrease(mp, Repay{Transfer: flash, MaxSharePrice: big.NewInt(1)}, withdraw, s, loopOwner)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(outline(t, c), " ")
	want := "morphoFlashLoan { morphoRepay morphoWithdrawCollateral erc20Transfer approve loop:fillQuoteTokenToToken } erc20Transfer erc20Transfer"
	if got != want {
		t.Fatalf("bundle\n got %s\nwant %s", got, want)
	}
	w := Want{}.Repaid(id).Withdraw(id, withdraw).Sell(collTok, loanTok, sell).Flash()
	if err := plan.Check([]Call{c}, w); err != nil {
		t.Fatal(err)
	}
	// Unwind: repay by shares (no dust debt), withdraw everything.
	u, err := d.LoopDecrease(mp, Repay{Shares: big.NewInt(123), Transfer: flash, MaxSharePrice: big.NewInt(1)}, withdraw, s, loopOwner)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := Decode(d.Bundler3, u.Data)
	var entries []bundleEntry
	abi.ConvertType(r.Args[0], &entries)
	fl, _ := Decode(d.GeneralAdapter1, entries[0].Data)
	cb, _ := Decode(d.GeneralAdapter1, append(append([]byte{}, morphoABI.Methods["multicall"].ID...), fl.Args[2].([]byte)...))
	var inner []bundleEntry
	abi.ConvertType(cb.Args[0], &inner)
	rep, _ := Decode(d.GeneralAdapter1, inner[0].Data)
	if rep.Args[1].(*big.Int).Sign() != 0 || rep.Args[2].(*big.Int).Cmp(big.NewInt(123)) != 0 {
		t.Fatalf("unwind repays assets %v shares %v, want shares only", rep.Args[1], rep.Args[2])
	}
	// The swap minimum must cover the flash loan.
	short := s
	short.MinOut = big.NewInt(499_999_999)
	if _, err := d.LoopDecrease(mp, Repay{Transfer: flash, MaxSharePrice: big.NewInt(1)}, withdraw, short, loopOwner); err == nil {
		t.Fatal("built an unwind whose swap cannot repay the flash")
	}
}

func eulerLoop(t *testing.T) (EulerLoop, Plan, common.Address) {
	t.Helper()
	sw, err := EulerSwapFor(56)
	if err != nil {
		t.Fatal(err)
	}
	router, err := OkuRouter(56)
	if err != nil {
		t.Fatal(err)
	}
	cv, bv := common.HexToAddress("0xc0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0"), common.HexToAddress("0xb0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0")
	sub := SubAccount(loopOwner, 2)
	l := EulerLoop{Swap: sw, Owner: loopOwner, Sub: sub, CollateralVault: cv, BorrowVault: bv, CollateralAsset: collTok, BorrowAsset: loanTok, Deadline: 1_900_000_000}
	plan := Plan{Protocol: "euler", Chain: 56, Owner: loopOwner, Vault: &bv, CollateralVault: &cv, SubAccount: 2, Tokens: []common.Address{collTok, loanTok}, Router: &router, SwapPeriphery: true}
	return l, plan, router
}

func evcOutline(t *testing.T, c Call, evc common.Address) []string {
	t.Helper()
	items, err := DecodeEVCBatch(c.Data, evc)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		out = append(out, it.Call.Method)
		if it.Call.Method == "loop:multicall" {
			out = append(out, "{")
			for _, inner := range it.Call.Args[0].([][]byte) {
				m, err := Decode(it.Call.To, inner)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, m.Method)
			}
			out = append(out, "}")
		}
	}
	return out
}

func TestEulerLoopIncrease(t *testing.T) {
	l, plan, router := eulerLoop(t)
	borrow := big.NewInt(1_000_000)
	s := Swap{Router: router, Trade: fill(t, loanTok, collTok, l.Swap.Swapper, borrow), MinOut: big.NewInt(42)}
	c, err := l.Increase(nil, borrow, s)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(evcOutline(t, c, l.Swap.EVC), " ")
	want := "enableCollateral enableController borrow loop:multicall { loop:swap loop:sweep loop:deposit loop:deposit } loop:verifyAmountMinAndSkim"
	if got != want {
		t.Fatalf("batch\n got %s\nwant %s", got, want)
	}
	w := Want{}.Vault(l.BorrowVault, "borrow", borrow).Sell(loanTok, collTok, borrow).Bound("skim_min", l.CollateralVault, s.MinOut)
	if err := plan.Check([]Call{c}, w); err != nil {
		t.Fatal(err)
	}
	// Without the periphery in scope the batch is refused.
	plan.SwapPeriphery = false
	if plan.Check([]Call{c}, w) == nil {
		t.Fatal("swapper verified without the periphery in scope")
	}
	plan.SwapPeriphery = true
	// Open with a deposit from the owner into the sub-account.
	o, err := l.Increase(big.NewInt(7), borrow, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(evcOutline(t, o, l.Swap.EVC), " "); got != "enableCollateral deposit enableController borrow loop:multicall { loop:swap loop:sweep loop:deposit loop:deposit } loop:verifyAmountMinAndSkim" {
		t.Fatalf("open batch %s", got)
	}
	if err := plan.Check([]Call{o}, w.Vault(l.CollateralVault, "deposit", big.NewInt(7))); err != nil {
		t.Fatal(err)
	}
}

func TestEulerLoopDecreaseAndUnwind(t *testing.T) {
	l, plan, router := eulerLoop(t)
	sell := big.NewInt(5_000)
	s := Swap{Router: router, Trade: fill(t, collTok, loanTok, l.Swap.Swapper, sell), MinOut: big.NewInt(900)}
	debtMax := big.NewInt(10_000)
	c, err := l.Decrease(sell, s, debtMax)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(evcOutline(t, c, l.Swap.EVC), " ")
	want := "withdraw loop:multicall { loop:swap loop:repay loop:deposit loop:sweep } loop:verifyDebtMax"
	if got != want {
		t.Fatalf("batch\n got %s\nwant %s", got, want)
	}
	w := Want{}.Vault(l.CollateralVault, "withdraw", sell).Vault(l.BorrowVault, "swap_repay", s.MinOut).Sell(collTok, loanTok, sell).Bound("debt_max", l.BorrowVault, debtMax)
	if err := plan.Check([]Call{c}, w); err != nil {
		t.Fatal(err)
	}
	// Unwind into collateral: sell part, repay all, release the
	// controller and redeem the rest to the owner.
	u, err := l.Decrease(sell, s, new(big.Int))
	if err != nil {
		t.Fatal(err)
	}
	got = strings.Join(evcOutline(t, u, l.Swap.EVC), " ")
	want = "withdraw loop:multicall { loop:swap loop:repay loop:deposit loop:sweep } loop:verifyDebtMax disableController redeem"
	if got != want {
		t.Fatalf("unwind\n got %s\nwant %s", got, want)
	}
	uw := Want{}.Vault(l.CollateralVault, "withdraw", sell).Vault(l.CollateralVault, "redeem", maxUint256).Vault(l.BorrowVault, "swap_repay", maxUint256).Sell(collTok, loanTok, sell).Bound("debt_max", l.BorrowVault, new(big.Int)).Flag("vault:" + strings.ToLower(l.BorrowVault.Hex()) + ":disableController")
	if err := plan.Check([]Call{u}, uw); err != nil {
		t.Fatal(err)
	}
	// Unwind into the loan asset: everything goes through the swap.
	all, err := l.Decrease(nil, s, new(big.Int))
	if err != nil {
		t.Fatal(err)
	}
	got = strings.Join(evcOutline(t, all, l.Swap.EVC), " ")
	if got != "redeem loop:multicall { loop:swap loop:repay loop:deposit loop:sweep } loop:verifyDebtMax disableController" {
		t.Fatalf("unwind to loan: %s", got)
	}
}

func TestLoopSizing(t *testing.T) {
	// 10 units of collateral value, 5 debt: 2x. To 3x at k=1: borrow 5
	// (15 collateral, 10 debt).
	if b := LoopBorrow(10, 5, 3, 1); b < 4.999 || b > 5.001 {
		t.Fatalf("borrow %v", b)
	}
	// A 1% swap loss shrinks equity, so 3x needs a smaller borrow:
	// (3*5-10)/(0.99+0.03) = 4.902.
	if b := LoopBorrow(10, 5, 3, 0.99); b < 4.901 || b > 4.903 {
		t.Fatalf("borrow with loss %v", b)
	}
	// From 50% LTV to 25%, k=1: sell 10/3 of value (6.67 coll, 1.67 debt).
	if s := LoopSell(10, 5, 0.25, 1); s < 3.333 || s > 3.334 {
		t.Fatalf("sell %v", s)
	}
	if LoopSell(10, 5, 0.6, 1) > 0 {
		t.Fatal("already below target must not sell")
	}
}
