package lending

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Loop bundles. Morpho mirrors the backend loop open (and the morpho-sdk
// flash pattern): Bundler3 calls GeneralAdapter1.morphoFlashLoan, whose
// callback runs the inner bundle, and Morpho pulls the flash amount back
// from GeneralAdapter1 when the callback returns. Anything the callback does
// not leave on the adapter makes the whole transaction revert, which is
// what bounds the opaque router swap.

// Swap is one Oku Router fill and its bound: the minimum the bundle accepts.
type Swap struct {
	Router common.Address
	Trade  RouterTrade
	MinOut *big.Int
}

func (s Swap) valid(sell, buy, recipient common.Address) error {
	t := s.Trade
	if s.Router == (common.Address{}) || t.Sell != sell || t.Buy != buy || t.Recipient != recipient || s.MinOut == nil || s.MinOut.Sign() <= 0 {
		return errors.New("swap does not sell the expected token to the bundle")
	}
	return nil
}

func callbackHash(inner []bundleCall) ([]byte, [32]byte) {
	enc, err := abi.Arguments{morphoABI.Methods["multicall"].Inputs[0]}.Pack(inner)
	if err != nil {
		panic(err)
	}
	return enc, crypto.Keccak256Hash(enc)
}

func (d MorphoDeployment) flash(token common.Address, amount *big.Int, inner []bundleCall) bundleCall {
	enc, hash := callbackHash(inner)
	c := d.adapter(pack("morphoFlashLoan", token, amount, enc))
	c.CallbackHash = hash
	return c
}

func (d MorphoDeployment) direct(to common.Address, data []byte) bundleCall {
	return bundleCall{To: to, Data: data, Value: new(big.Int)}
}

// LoopIncrease raises leverage on an existing (or empty) position: flash
// borrow `borrow` loan token, swap it to collateral through the router
// (delivered to GeneralAdapter1), supply at least the swap minimum plus any
// surplus, then borrow `borrow` to repay the flash. The owner sends nothing.
func (d MorphoDeployment) LoopIncrease(mp MarketParams, borrow, minBorrowSharePrice *big.Int, s Swap, owner common.Address) (Call, error) {
	if borrow == nil || borrow.Sign() <= 0 || s.Trade.Amount.Cmp(borrow) != 0 {
		return Call{}, errors.New("swap must sell exactly the borrowed amount")
	}
	if err := s.valid(mp.LoanToken, mp.CollateralToken, d.GeneralAdapter1); err != nil {
		return Call{}, err
	}
	surplus := d.adapter(pack("morphoSupplyCollateral", mp, maxUint256, owner, []byte{}))
	surplus.SkipRevert = true
	inner := []bundleCall{
		d.adapter(pack("erc20Transfer", mp.LoanToken, d.Bundler3, borrow)),
		d.direct(mp.LoanToken, pack("approve", s.Router, borrow)),
		d.direct(s.Router, s.Trade.Data),
		d.adapter(pack("morphoSupplyCollateral", mp, s.MinOut, owner, []byte{})),
		surplus,
		d.adapter(pack("morphoBorrow", mp, borrow, new(big.Int), minBorrowSharePrice, d.GeneralAdapter1)),
	}
	return d.bundle(
		d.flash(mp.LoanToken, borrow, inner),
		d.adapter(pack("erc20Transfer", mp.LoanToken, owner, maxUint256)),
		d.adapter(pack("erc20Transfer", mp.CollateralToken, owner, maxUint256)),
	), nil
}

// LoopDecrease lowers leverage or closes: flash borrow r.Transfer of the
// loan token, repay (by assets, or by shares for a full close), withdraw
// `withdraw` collateral to GeneralAdapter1, sell s.Trade.Amount of it
// through the router back to the adapter, and let Morpho pull the flash.
// Unsold collateral and the unused loan token are swept to the owner, so
// the owner never needs to hold the loan token.
func (d MorphoDeployment) LoopDecrease(mp MarketParams, r Repay, withdraw *big.Int, s Swap, owner common.Address) (Call, error) {
	if r.Transfer == nil || r.Transfer.Sign() <= 0 || r.MaxSharePrice == nil || withdraw == nil || withdraw.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	if err := s.valid(mp.CollateralToken, mp.LoanToken, d.GeneralAdapter1); err != nil {
		return Call{}, err
	}
	if s.Trade.Amount.Cmp(withdraw) > 0 {
		return Call{}, errors.New("swap sells more collateral than is withdrawn")
	}
	// The flash is repaid from the swap: it must cover the debt repaid.
	if s.MinOut.Cmp(r.Transfer) < 0 {
		return Call{}, errors.New("swap minimum does not cover the flash loan")
	}
	assets, shares := r.Transfer, new(big.Int)
	if r.Shares != nil && r.Shares.Sign() > 0 {
		assets, shares = new(big.Int), r.Shares
	}
	inner := []bundleCall{
		d.adapter(pack("morphoRepay", mp, assets, shares, r.MaxSharePrice, owner, []byte{})),
		d.adapter(pack("morphoWithdrawCollateral", mp, withdraw, d.GeneralAdapter1)),
		d.adapter(pack("erc20Transfer", mp.CollateralToken, d.Bundler3, s.Trade.Amount)),
		d.direct(mp.CollateralToken, pack("approve", s.Router, s.Trade.Amount)),
		d.direct(s.Router, s.Trade.Data),
	}
	return d.bundle(
		d.flash(mp.LoanToken, r.Transfer, inner),
		d.adapter(pack("erc20Transfer", mp.LoanToken, owner, maxUint256)),
		d.adapter(pack("erc20Transfer", mp.CollateralToken, owner, maxUint256)),
	), nil
}

// EulerLoop is one Euler sub-account position and the swap periphery.
type EulerLoop struct {
	Swap                         EulerSwap
	Owner, Sub                   common.Address
	CollateralVault, BorrowVault common.Address
	CollateralAsset, BorrowAsset common.Address
	Deadline                     uint64
}

// Euler Swapper constants (evk-periphery ISwapper / GenericHandler).
var (
	genericHandler = [32]byte{'G', 'e', 'n', 'e', 'r', 'i', 'c'}
	swapDustMin    = big.NewInt(5)
)

type swapParams struct {
	Handler   [32]byte       `abi:"handler"`
	Mode      *big.Int       `abi:"mode"`
	Account   common.Address `abi:"account"`
	TokenIn   common.Address `abi:"tokenIn"`
	TokenOut  common.Address `abi:"tokenOut"`
	VaultIn   common.Address `abi:"vaultIn"`
	AccountIn common.Address `abi:"accountIn"`
	Receiver  common.Address `abi:"receiver"`
	AmountOut *big.Int       `abi:"amountOut"`
	Data      []byte         `abi:"data"`
}

var genericArgs = abi.Arguments{{Type: mustType("address", nil)}, {Type: mustType("bytes", nil)}}

type evcItem struct {
	TargetContract    common.Address `abi:"targetContract"`
	OnBehalfOfAccount common.Address `abi:"onBehalfOfAccount"`
	Value             *big.Int       `abi:"value"`
	Data              []byte         `abi:"data"`
}

func (l EulerLoop) valid() error {
	z := common.Address{}
	if l.Swap.EVC == z || l.Swap.Swapper == z || l.Swap.SwapVerifier == z || l.Deadline == 0 || l.Sub == z {
		return errors.New("incomplete Euler loop")
	}
	return nil
}

// swapExactIn is the Swapper call that sells s.Trade through the router.
func (l EulerLoop) swapExactIn(in, out, vaultIn, receiver common.Address, s Swap) ([]byte, error) {
	if err := s.valid(in, out, l.Swap.Swapper); err != nil {
		return nil, err
	}
	data, err := genericArgs.Pack(s.Router, s.Trade.Data)
	if err != nil {
		return nil, err
	}
	return packLoop("swap", swapParams{Handler: genericHandler, Mode: new(big.Int), Account: l.Sub, TokenIn: in, TokenOut: out, VaultIn: vaultIn, AccountIn: l.Sub, Receiver: receiver, AmountOut: new(big.Int), Data: data}), nil
}

func (l EulerLoop) batch(items []evcItem) Call {
	return Call{To: l.Swap.EVC, Data: pack("batch", items), Value: new(big.Int)}
}

func item(to, onBehalf common.Address, data []byte) evcItem {
	return evcItem{TargetContract: to, OnBehalfOfAccount: onBehalf, Value: new(big.Int), Data: data}
}

// Increase mirrors the backend's Transaction-scheme loop (MultiplyWithSwap)
// without a fresh deposit: borrow to the Swapper, swap to collateral,
// deposit it for the sub-account and skim-verify the minimum. Account and
// vault status checks are deferred to the end of the EVC batch.
func (l EulerLoop) Increase(borrow *big.Int, s Swap) (Call, error) {
	if err := l.valid(); err != nil {
		return Call{}, err
	}
	if borrow == nil || borrow.Sign() <= 0 || s.Trade.Amount.Cmp(borrow) != 0 {
		return Call{}, errors.New("swap must sell exactly the borrowed amount")
	}
	swap, err := l.swapExactIn(l.BorrowAsset, l.CollateralAsset, l.BorrowVault, l.CollateralVault, s)
	if err != nil {
		return Call{}, err
	}
	multi := packLoop("multicall", [][]byte{
		swap,
		packLoop("sweep", l.CollateralAsset, new(big.Int), l.CollateralVault),
		packLoop("deposit", l.BorrowAsset, l.BorrowVault, swapDustMin, l.Sub),
		packLoop("deposit", l.CollateralAsset, l.CollateralVault, swapDustMin, l.Owner),
	})
	return l.batch([]evcItem{
		item(l.Swap.EVC, common.Address{}, pack("enableCollateral", l.Sub, l.CollateralVault)),
		item(l.Swap.EVC, common.Address{}, pack("enableController", l.Sub, l.BorrowVault)),
		item(l.BorrowVault, l.Sub, pack("borrow", borrow, l.Swap.Swapper)),
		item(l.Swap.Swapper, l.Sub, multi),
		item(l.Swap.SwapVerifier, l.Sub, packLoop("verifyAmountMinAndSkim", l.CollateralVault, l.Sub, s.MinOut, new(big.Int).SetUint64(l.Deadline))),
	}), nil
}

// Decrease sells collateral to repay debt in one EVC batch with deferred
// checks: withdraw collateral to the Swapper, swap it to the loan asset,
// repay, verify the remaining debt, and send any extra loan asset to the
// owner. A full close (debtMax 0) repays everything, releases the
// controller and redeems the remaining collateral to the owner.
//
// withdraw is the collateral assets sent to the Swapper, or nil to redeem
// every share to it (close into the loan asset).
func (l EulerLoop) Decrease(withdraw *big.Int, s Swap, debtMax *big.Int) (Call, error) {
	if err := l.valid(); err != nil {
		return Call{}, err
	}
	if debtMax == nil || debtMax.Sign() < 0 || withdraw != nil && (withdraw.Sign() <= 0 || s.Trade.Amount.Cmp(withdraw) > 0) {
		return Call{}, errors.New("invalid decrease amounts")
	}
	full := debtMax.Sign() == 0
	swap, err := l.swapExactIn(l.CollateralAsset, l.BorrowAsset, l.CollateralVault, l.BorrowVault, s)
	if err != nil {
		return Call{}, err
	}
	repay := s.MinOut
	leftover := l.Sub
	if full {
		repay, leftover = maxUint256, l.Owner
	}
	multi := packLoop("multicall", [][]byte{
		swap,
		packLoop("repay", l.BorrowAsset, l.BorrowVault, repay, l.Sub),
		packLoop("deposit", l.CollateralAsset, l.CollateralVault, swapDustMin, leftover),
		packLoop("sweep", l.BorrowAsset, new(big.Int), l.Owner),
	})
	var items []evcItem
	if withdraw == nil {
		items = append(items, item(l.CollateralVault, l.Sub, pack("redeem", maxUint256, l.Swap.Swapper, l.Sub)))
	} else {
		items = append(items, item(l.CollateralVault, l.Sub, pack("withdraw", withdraw, l.Swap.Swapper, l.Sub)))
	}
	items = append(items,
		item(l.Swap.Swapper, l.Sub, multi),
		item(l.Swap.SwapVerifier, l.Sub, packLoop("verifyDebtMax", l.BorrowVault, l.Sub, debtMax, new(big.Int).SetUint64(l.Deadline))),
	)
	if full {
		items = append(items, item(l.BorrowVault, l.Sub, pack("disableController")))
		if withdraw != nil {
			items = append(items, item(l.CollateralVault, l.Sub, pack("redeem", maxUint256, l.Owner, l.Sub)))
		}
	}
	return l.batch(items), nil
}

// LoopBorrow sizes the extra debt that moves a position to target leverage
// when each unit of loan asset swaps into k loan-units of collateral value
// (k = 1 - slippage - fees). Values are in loan units: collateral value cv,
// debt d. Returns <= 0 when the position is already at or above target.
func LoopBorrow(cv, d, target, k float64) float64 {
	return (target*(cv-d) - cv) / (target*(1-k) + k)
}

// LoopSell sizes the collateral (in loan units of value) to sell so the
// position reaches target LTV t when each unit sold repays k units of debt.
// Returns <= 0 when the position is already at or below target.
func LoopSell(cv, d, t, k float64) float64 {
	return (d - t*cv) / (k - t)
}
