// Package lending builds and checks Morpho Blue, MetaMorpho vault and Euler V2
// lending calls, and holds the shared risk and loop math. It mirrors the Oku
// web app (Morpho calldata built client side with the Morpho SDK, Euler
// calldata built by the accounts LendingService) and has no CLI dependencies.
package lending

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Call is one transaction to send: target, calldata and native value.
type Call struct {
	To    common.Address
	Data  []byte
	Value *big.Int
}

// Hex renders calldata as 0x-prefixed lowercase hex.
func (c Call) Hex() string { return "0x" + common.Bytes2Hex(c.Data) }

// MorphoDeployment is the per-chain Morpho Blue stack the Morpho SDK uses.
type MorphoDeployment struct {
	ChainID         uint64
	Morpho          common.Address
	Bundler3        common.Address
	GeneralAdapter1 common.Address
}

// Valid reports whether every contract the builders need is configured.
func (d MorphoDeployment) Valid() bool {
	zero := common.Address{}
	return d.ChainID > 0 && d.Morpho != zero && d.Bundler3 != zero && d.GeneralAdapter1 != zero
}

// MarketParams identifies a Morpho Blue market.
type MarketParams struct {
	LoanToken       common.Address `abi:"loanToken"`
	CollateralToken common.Address `abi:"collateralToken"`
	Oracle          common.Address `abi:"oracle"`
	Irm             common.Address `abi:"irm"`
	Lltv            *big.Int       `abi:"lltv"`
}

// ID is keccak256(abi.encode(marketParams)), the Morpho Blue market id.
func (m MarketParams) ID() common.Hash {
	enc, err := abi.Arguments{{Type: marketParamsType}}.Pack(m)
	if err != nil {
		return common.Hash{}
	}
	return crypto.Keccak256Hash(enc)
}

// MarketState is Morpho Blue's market(id) storage.
type MarketState struct {
	TotalSupplyAssets, TotalSupplyShares *big.Int
	TotalBorrowAssets, TotalBorrowShares *big.Int
	LastUpdate, Fee                      *big.Int
}

// PositionState is Morpho Blue's position(id, user) storage.
type PositionState struct {
	SupplyShares, BorrowShares, Collateral *big.Int
}

// Slippage defaults match the Morpho SDK's DEFAULT_SLIPPAGE_TOLERANCE (3 bps, WAD).
var (
	WAD                     = big.NewInt(1_000_000_000_000_000_000)
	RAY                     = new(big.Int).Exp(big.NewInt(10), big.NewInt(27), nil)
	DefaultSlippage         = big.NewInt(300_000_000_000_000)
	maxAbsoluteSharePrice   = new(big.Int).Mul(big.NewInt(100), RAY)
	virtualShares           = big.NewInt(1_000_000)
	virtualAssets           = big.NewInt(1)
	maxUint256              = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	errZeroShares           = errors.New("share conversion rounds to zero")
	errNonPositive          = errors.New("amount must be positive")
	marketParamsType        = mustType("tuple", []abi.ArgumentMarshaling{{Name: "loanToken", Type: "address"}, {Name: "collateralToken", Type: "address"}, {Name: "oracle", Type: "address"}, {Name: "irm", Type: "address"}, {Name: "lltv", Type: "uint256"}})
	morphoAccrualHorizonSec = int64(2 * 60 * 60)
)

func mustType(t string, components []abi.ArgumentMarshaling) abi.Type {
	typ, err := abi.NewType(t, "", components)
	if err != nil {
		panic(err)
	}
	return typ
}

const marketParamsJSON = `{"name":"marketParams","type":"tuple","components":[{"name":"loanToken","type":"address"},{"name":"collateralToken","type":"address"},{"name":"oracle","type":"address"},{"name":"irm","type":"address"},{"name":"lltv","type":"uint256"}]}`
const marketJSON = `{"name":"market","type":"tuple","components":[{"name":"totalSupplyAssets","type":"uint128"},{"name":"totalSupplyShares","type":"uint128"},{"name":"totalBorrowAssets","type":"uint128"},{"name":"totalBorrowShares","type":"uint128"},{"name":"lastUpdate","type":"uint128"},{"name":"fee","type":"uint128"}]}`

// morphoABI covers Bundler3, GeneralAdapter1, Morpho Blue, ERC-4626 vaults,
// ERC-20 and the IRM view the builders and readers use.
var morphoABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(`[
{"type":"function","name":"multicall","inputs":[{"name":"bundle","type":"tuple[]","components":[{"name":"to","type":"address"},{"name":"data","type":"bytes"},{"name":"value","type":"uint256"},{"name":"skipRevert","type":"bool"},{"name":"callbackHash","type":"bytes32"}]}],"outputs":[]},
{"type":"function","name":"erc20TransferFrom","inputs":[{"name":"token","type":"address"},{"name":"receiver","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[]},
{"type":"function","name":"erc20Transfer","inputs":[{"name":"token","type":"address"},{"name":"receiver","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[]},
{"type":"function","name":"erc4626Deposit","inputs":[{"name":"vault","type":"address"},{"name":"assets","type":"uint256"},{"name":"maxSharePriceE27","type":"uint256"},{"name":"receiver","type":"address"}],"outputs":[]},
{"type":"function","name":"morphoSupplyCollateral","inputs":[` + marketParamsJSON + `,{"name":"assets","type":"uint256"},{"name":"onBehalf","type":"address"},{"name":"data","type":"bytes"}],"outputs":[]},
{"type":"function","name":"morphoBorrow","inputs":[` + marketParamsJSON + `,{"name":"assets","type":"uint256"},{"name":"shares","type":"uint256"},{"name":"minSharePriceE27","type":"uint256"},{"name":"receiver","type":"address"}],"outputs":[]},
{"type":"function","name":"morphoRepay","inputs":[` + marketParamsJSON + `,{"name":"assets","type":"uint256"},{"name":"shares","type":"uint256"},{"name":"maxSharePriceE27","type":"uint256"},{"name":"onBehalf","type":"address"},{"name":"data","type":"bytes"}],"outputs":[]},
{"type":"function","name":"morphoWithdrawCollateral","inputs":[` + marketParamsJSON + `,{"name":"assets","type":"uint256"},{"name":"receiver","type":"address"}],"outputs":[]},
{"type":"function","name":"withdrawCollateral","inputs":[` + marketParamsJSON + `,{"name":"assets","type":"uint256"},{"name":"onBehalf","type":"address"},{"name":"receiver","type":"address"}],"outputs":[]},
{"type":"function","name":"setAuthorization","inputs":[{"name":"authorized","type":"address"},{"name":"newIsAuthorized","type":"bool"}],"outputs":[]},
{"type":"function","name":"isAuthorized","inputs":[{"type":"address"},{"type":"address"}],"outputs":[{"type":"bool"}]},
{"type":"function","name":"idToMarketParams","inputs":[{"type":"bytes32"}],"outputs":[{"type":"address"},{"type":"address"},{"type":"address"},{"type":"address"},{"type":"uint256"}]},
{"type":"function","name":"market","inputs":[{"type":"bytes32"}],"outputs":[{"type":"uint128"},{"type":"uint128"},{"type":"uint128"},{"type":"uint128"},{"type":"uint128"},{"type":"uint128"}]},
{"type":"function","name":"position","inputs":[{"type":"bytes32"},{"type":"address"}],"outputs":[{"type":"uint256"},{"type":"uint128"},{"type":"uint128"}]},
{"type":"function","name":"price","inputs":[],"outputs":[{"type":"uint256"}]},
{"type":"function","name":"borrowRateView","inputs":[` + marketParamsJSON + `,` + marketJSON + `],"outputs":[{"type":"uint256"}]},
{"type":"function","name":"withdraw","inputs":[{"name":"assets","type":"uint256"},{"name":"receiver","type":"address"},{"name":"owner","type":"address"}],"outputs":[{"type":"uint256"}]},
{"type":"function","name":"redeem","inputs":[{"name":"shares","type":"uint256"},{"name":"receiver","type":"address"},{"name":"owner","type":"address"}],"outputs":[{"type":"uint256"}]},
{"type":"function","name":"convertToShares","inputs":[{"type":"uint256"}],"outputs":[{"type":"uint256"}]},
{"type":"function","name":"convertToAssets","inputs":[{"type":"uint256"}],"outputs":[{"type":"uint256"}]},
{"type":"function","name":"asset","inputs":[],"outputs":[{"type":"address"}]},
{"type":"function","name":"balanceOf","inputs":[{"type":"address"}],"outputs":[{"type":"uint256"}]},
{"type":"function","name":"allowance","inputs":[{"type":"address"},{"type":"address"}],"outputs":[{"type":"uint256"}]},
{"type":"function","name":"approve","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"type":"bool"}]}
]`))
	if err != nil {
		panic(err)
	}
	return a
}()

func pack(method string, args ...any) []byte {
	data, err := morphoABI.Pack(method, args...)
	if err != nil {
		panic(fmt.Sprintf("lending: pack %s: %v", method, err))
	}
	return data
}

// bundleCall is one Bundler3 multicall entry.
type bundleCall struct {
	To           common.Address `abi:"to"`
	Data         []byte         `abi:"data"`
	Value        *big.Int       `abi:"value"`
	SkipRevert   bool           `abi:"skipRevert"`
	CallbackHash [32]byte       `abi:"callbackHash"`
}

func (d MorphoDeployment) adapter(data []byte) bundleCall {
	return bundleCall{To: d.GeneralAdapter1, Data: data, Value: new(big.Int)}
}

func (d MorphoDeployment) bundle(calls ...bundleCall) Call {
	return Call{To: d.Bundler3, Data: pack("multicall", calls), Value: new(big.Int)}
}

// Approve is the ERC-20 approval the Morpho SDK requires before a bundle
// pulls `amount` through GeneralAdapter1 (or to the Morpho contract for a
// direct call). A nonzero, insufficient allowance is reset to zero first so
// tokens such as USDT accept the new approval.
func Approve(token, spender common.Address, allowance, amount *big.Int) []Call {
	if allowance.Cmp(amount) >= 0 {
		return nil
	}
	var out []Call
	if allowance.Sign() > 0 {
		out = append(out, Call{To: token, Data: pack("approve", spender, new(big.Int)), Value: new(big.Int)})
	}
	return append(out, Call{To: token, Data: pack("approve", spender, amount), Value: new(big.Int)})
}

// AuthorizeAdapter is Morpho.setAuthorization(GeneralAdapter1, true), the
// one-time authorization the SDK requires before the adapter can borrow or
// withdraw collateral on the user's behalf.
func (d MorphoDeployment) AuthorizeAdapter() Call {
	return Call{To: d.Morpho, Data: pack("setAuthorization", d.GeneralAdapter1, true), Value: new(big.Int)}
}

// VaultDeposit mirrors morpho-sdk vaultV1Deposit/vaultV2Deposit: pull the
// asset into GeneralAdapter1 and deposit with an on-chain max share price.
func (d MorphoDeployment) VaultDeposit(vault, asset common.Address, assets, maxSharePrice *big.Int, receiver common.Address) (Call, error) {
	if assets.Sign() <= 0 || maxSharePrice.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	return d.bundle(
		d.adapter(pack("erc20TransferFrom", asset, d.GeneralAdapter1, assets)),
		d.adapter(pack("erc4626Deposit", vault, assets, maxSharePrice, receiver)),
	), nil
}

// VaultWithdraw is the direct vault.withdraw(assets, receiver, owner) call.
func VaultWithdraw(vault common.Address, assets *big.Int, owner common.Address) (Call, error) {
	if assets.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	return Call{To: vault, Data: pack("withdraw", assets, owner, owner), Value: new(big.Int)}, nil
}

// VaultRedeem is the direct vault.redeem(shares, receiver, owner) call used
// for a full withdrawal so no share dust is left behind.
func VaultRedeem(vault common.Address, shares *big.Int, owner common.Address) (Call, error) {
	if shares.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	return Call{To: vault, Data: pack("redeem", shares, owner, owner), Value: new(big.Int)}, nil
}

// SupplyCollateral mirrors blueSupplyCollateral.
func (d MorphoDeployment) SupplyCollateral(mp MarketParams, amount *big.Int, onBehalf common.Address) (Call, error) {
	if amount.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	return d.bundle(
		d.adapter(pack("erc20TransferFrom", mp.CollateralToken, d.GeneralAdapter1, amount)),
		d.adapter(pack("morphoSupplyCollateral", mp, amount, onBehalf, []byte{})),
	), nil
}

// SupplyCollateralBorrow mirrors blueSupplyCollateralBorrow without
// reallocations: supply collateral then borrow to receiver.
func (d MorphoDeployment) SupplyCollateralBorrow(mp MarketParams, collateral, borrow, minSharePrice *big.Int, user common.Address) (Call, error) {
	if collateral.Sign() <= 0 || borrow.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	return d.bundle(
		d.adapter(pack("erc20TransferFrom", mp.CollateralToken, d.GeneralAdapter1, collateral)),
		d.adapter(pack("morphoSupplyCollateral", mp, collateral, user, []byte{})),
		d.adapter(pack("morphoBorrow", mp, borrow, new(big.Int), minSharePrice, user)),
	), nil
}

// Borrow mirrors blueBorrow without reallocations.
func (d MorphoDeployment) Borrow(mp MarketParams, amount, minSharePrice *big.Int, receiver common.Address) (Call, error) {
	if amount.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	return d.bundle(d.adapter(pack("morphoBorrow", mp, amount, new(big.Int), minSharePrice, receiver))), nil
}

// Repay is the repay half of blueRepay / blueRepayWithdrawCollateral. In
// shares mode it pulls transfer, repays exactly the shares and skims the
// unused loan token back to the user; in assets mode it repays transfer.
type Repay struct {
	Shares        *big.Int // > 0 selects shares mode (full repay)
	Transfer      *big.Int // loan token pulled from the user
	MaxSharePrice *big.Int
}

func (d MorphoDeployment) repayCalls(mp MarketParams, r Repay, user common.Address) ([]bundleCall, error) {
	if r.Transfer == nil || r.Transfer.Sign() <= 0 || r.MaxSharePrice == nil || r.MaxSharePrice.Sign() <= 0 {
		return nil, errNonPositive
	}
	assets, shares := r.Transfer, new(big.Int)
	if r.Shares != nil && r.Shares.Sign() > 0 {
		assets, shares = new(big.Int), r.Shares
	}
	calls := []bundleCall{
		d.adapter(pack("erc20TransferFrom", mp.LoanToken, d.GeneralAdapter1, r.Transfer)),
		d.adapter(pack("morphoRepay", mp, assets, shares, r.MaxSharePrice, user, []byte{})),
	}
	if shares.Sign() > 0 {
		calls = append(calls, d.adapter(pack("erc20Transfer", mp.LoanToken, user, maxUint256)))
	}
	return calls, nil
}

// RepayOnly mirrors blueRepay.
func (d MorphoDeployment) RepayOnly(mp MarketParams, r Repay, user common.Address) (Call, error) {
	calls, err := d.repayCalls(mp, r, user)
	if err != nil {
		return Call{}, err
	}
	return d.bundle(calls...), nil
}

// RepayWithdrawCollateral mirrors blueRepayWithdrawCollateral.
func (d MorphoDeployment) RepayWithdrawCollateral(mp MarketParams, r Repay, withdraw *big.Int, user common.Address) (Call, error) {
	if withdraw.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	calls, err := d.repayCalls(mp, r, user)
	if err != nil {
		return Call{}, err
	}
	calls = append(calls, d.adapter(pack("morphoWithdrawCollateral", mp, withdraw, user)))
	return d.bundle(calls...), nil
}

// WithdrawCollateral mirrors blueWithdrawCollateral: a direct Morpho call.
func (d MorphoDeployment) WithdrawCollateral(mp MarketParams, amount *big.Int, user common.Address) (Call, error) {
	if amount.Sign() <= 0 {
		return Call{}, errNonPositive
	}
	return Call{To: d.Morpho, Data: pack("withdrawCollateral", mp, amount, user, user), Value: new(big.Int)}, nil
}

// --- share math (Morpho Blue SharesMathLib / MathLib) ---

func mulDivDown(x, y, d *big.Int) *big.Int {
	return new(big.Int).Div(new(big.Int).Mul(x, y), d)
}

func mulDivUp(x, y, d *big.Int) *big.Int {
	n := new(big.Int).Mul(x, y)
	n.Add(n, new(big.Int).Sub(d, big.NewInt(1)))
	return n.Div(n, d)
}

func toSharesUp(assets, totalAssets, totalShares *big.Int) *big.Int {
	return mulDivUp(assets, new(big.Int).Add(totalShares, virtualShares), new(big.Int).Add(totalAssets, virtualAssets))
}

func toSharesDown(assets, totalAssets, totalShares *big.Int) *big.Int {
	return mulDivDown(assets, new(big.Int).Add(totalShares, virtualShares), new(big.Int).Add(totalAssets, virtualAssets))
}

// ToBorrowAssetsUp converts borrow shares to assets, rounding up.
func (m MarketState) ToBorrowAssetsUp(shares *big.Int) *big.Int {
	return mulDivUp(shares, new(big.Int).Add(m.TotalBorrowAssets, virtualAssets), new(big.Int).Add(m.TotalBorrowShares, virtualShares))
}

func wToRay(w *big.Int) *big.Int { return new(big.Int).Mul(w, big.NewInt(1_000_000_000)) }

// MaxDepositSharePrice mirrors the SDK's vault deposit bound:
// min(mulDivUp(assets, wToRay(WAD+slippage), shares), 100 RAY).
func MaxDepositSharePrice(assets, expectedShares, slippage *big.Int) (*big.Int, error) {
	if expectedShares.Sign() <= 0 {
		return nil, errZeroShares
	}
	p := mulDivUp(assets, wToRay(new(big.Int).Add(WAD, slippage)), expectedShares)
	if p.Cmp(maxAbsoluteSharePrice) > 0 {
		p = new(big.Int).Set(maxAbsoluteSharePrice)
	}
	return p, nil
}

// MinBorrowSharePrice mirrors computeMinBorrowSharePrice.
func MinBorrowSharePrice(borrow *big.Int, m MarketState, slippage *big.Int) (*big.Int, error) {
	shares := toSharesUp(borrow, m.TotalBorrowAssets, m.TotalBorrowShares)
	if shares.Sign() == 0 {
		return nil, errZeroShares
	}
	return mulDivDown(borrow, wToRay(new(big.Int).Sub(WAD, slippage)), shares), nil
}

// MaxRepaySharePrice mirrors computeMaxRepaySharePrice on an accrued market.
func MaxRepaySharePrice(repayAssets, repayShares *big.Int, m MarketState, slippage *big.Int) (*big.Int, error) {
	assets, shares := repayAssets, toSharesDown(repayAssets, m.TotalBorrowAssets, m.TotalBorrowShares)
	if repayShares != nil && repayShares.Sign() > 0 {
		assets, shares = m.ToBorrowAssetsUp(repayShares), repayShares
	}
	if shares.Sign() == 0 {
		return nil, errZeroShares
	}
	p := mulDivUp(assets, wToRay(new(big.Int).Add(WAD, slippage)), shares)
	if p.Cmp(maxAbsoluteSharePrice) > 0 {
		p = new(big.Int).Set(maxAbsoluteSharePrice)
	}
	return p, nil
}

// wTaylorCompounded is Morpho's MathLib.wTaylorCompounded (e^(x*n) - 1, WAD).
func wTaylorCompounded(x, n *big.Int) *big.Int {
	first := new(big.Int).Mul(x, n)
	second := mulDivDown(first, first, new(big.Int).Mul(big.NewInt(2), WAD))
	third := mulDivDown(second, first, new(big.Int).Mul(big.NewInt(3), WAD))
	return first.Add(first, second).Add(first, third)
}

// AccrueBorrow returns the market with borrow interest accrued at ratePerSec
// (WAD, from the IRM) until `at`. Supply side and fee shares are not needed
// for repay bounds, so only borrow assets move, exactly as on-chain.
func (m MarketState) AccrueBorrow(ratePerSec *big.Int, at int64) MarketState {
	out := m
	elapsed := at - m.LastUpdate.Int64()
	if elapsed <= 0 || ratePerSec == nil || ratePerSec.Sign() <= 0 {
		return out
	}
	interest := mulDivDown(m.TotalBorrowAssets, wTaylorCompounded(ratePerSec, big.NewInt(elapsed)), WAD)
	out.TotalBorrowAssets = new(big.Int).Add(m.TotalBorrowAssets, interest)
	return out
}

// AccrualHorizon is how far ahead the SDK accrues before deriving a repay
// bound and the shares-mode transfer, so interest accrued until execution
// never trips maxSharePrice or underfunds a full repay.
func AccrualHorizon(now int64) int64 { return now + morphoAccrualHorizonSec }
