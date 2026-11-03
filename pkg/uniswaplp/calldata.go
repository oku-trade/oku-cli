package uniswaplp

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// v3ManagerABI is the subset of the Uniswap v3 NonfungiblePositionManager the
// CLI writes to and reads from.
const v3ManagerABI = `[
{"name":"mint","type":"function","stateMutability":"payable","inputs":[{"name":"params","type":"tuple","components":[{"name":"token0","type":"address"},{"name":"token1","type":"address"},{"name":"fee","type":"uint24"},{"name":"tickLower","type":"int24"},{"name":"tickUpper","type":"int24"},{"name":"amount0Desired","type":"uint256"},{"name":"amount1Desired","type":"uint256"},{"name":"amount0Min","type":"uint256"},{"name":"amount1Min","type":"uint256"},{"name":"recipient","type":"address"},{"name":"deadline","type":"uint256"}]}],"outputs":[]},
{"name":"increaseLiquidity","type":"function","stateMutability":"payable","inputs":[{"name":"params","type":"tuple","components":[{"name":"tokenId","type":"uint256"},{"name":"amount0Desired","type":"uint256"},{"name":"amount1Desired","type":"uint256"},{"name":"amount0Min","type":"uint256"},{"name":"amount1Min","type":"uint256"},{"name":"deadline","type":"uint256"}]}],"outputs":[]},
{"name":"decreaseLiquidity","type":"function","stateMutability":"payable","inputs":[{"name":"params","type":"tuple","components":[{"name":"tokenId","type":"uint256"},{"name":"liquidity","type":"uint128"},{"name":"amount0Min","type":"uint256"},{"name":"amount1Min","type":"uint256"},{"name":"deadline","type":"uint256"}]}],"outputs":[]},
{"name":"collect","type":"function","stateMutability":"payable","inputs":[{"name":"params","type":"tuple","components":[{"name":"tokenId","type":"uint256"},{"name":"recipient","type":"address"},{"name":"amount0Max","type":"uint128"},{"name":"amount1Max","type":"uint128"}]}],"outputs":[]},
{"name":"burn","type":"function","stateMutability":"payable","inputs":[{"name":"tokenId","type":"uint256"}],"outputs":[]},
{"name":"multicall","type":"function","stateMutability":"payable","inputs":[{"name":"data","type":"bytes[]"}],"outputs":[]},
{"name":"positions","type":"function","stateMutability":"view","inputs":[{"name":"tokenId","type":"uint256"}],"outputs":[{"name":"nonce","type":"uint96"},{"name":"operator","type":"address"},{"name":"token0","type":"address"},{"name":"token1","type":"address"},{"name":"fee","type":"uint24"},{"name":"tickLower","type":"int24"},{"name":"tickUpper","type":"int24"},{"name":"liquidity","type":"uint128"},{"name":"feeGrowthInside0LastX128","type":"uint256"},{"name":"feeGrowthInside1LastX128","type":"uint256"},{"name":"tokensOwed0","type":"uint128"},{"name":"tokensOwed1","type":"uint128"}]},
{"name":"balanceOf","type":"function","stateMutability":"view","inputs":[{"name":"owner","type":"address"}],"outputs":[{"name":"","type":"uint256"}]},
{"name":"tokenOfOwnerByIndex","type":"function","stateMutability":"view","inputs":[{"name":"owner","type":"address"},{"name":"index","type":"uint256"}],"outputs":[{"name":"","type":"uint256"}]},
{"name":"ownerOf","type":"function","stateMutability":"view","inputs":[{"name":"tokenId","type":"uint256"}],"outputs":[{"name":"","type":"address"}]}
]`

const v3PoolABI = `[
{"name":"getPool","type":"function","stateMutability":"view","inputs":[{"name":"","type":"address"},{"name":"","type":"address"},{"name":"","type":"uint24"}],"outputs":[{"name":"","type":"address"}]},
{"name":"slot0","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"sqrtPriceX96","type":"uint160"},{"name":"tick","type":"int24"},{"name":"observationIndex","type":"uint16"},{"name":"observationCardinality","type":"uint16"},{"name":"observationCardinalityNext","type":"uint16"},{"name":"feeProtocol","type":"uint8"},{"name":"unlocked","type":"bool"}]},
{"name":"liquidity","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint128"}]},
{"name":"tickSpacing","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"int24"}]},
{"name":"fee","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint24"}]},
{"name":"token0","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
{"name":"token1","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
{"name":"feeGrowthGlobal0X128","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
{"name":"feeGrowthGlobal1X128","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
{"name":"ticks","type":"function","stateMutability":"view","inputs":[{"name":"tick","type":"int24"}],"outputs":[{"name":"liquidityGross","type":"uint128"},{"name":"liquidityNet","type":"int128"},{"name":"feeGrowthOutside0X128","type":"uint256"},{"name":"feeGrowthOutside1X128","type":"uint256"},{"name":"tickCumulativeOutside","type":"int56"},{"name":"secondsPerLiquidityOutsideX128","type":"uint160"},{"name":"secondsOutside","type":"uint32"},{"name":"initialized","type":"bool"}]}
]`

const v4ABI = `[
{"name":"modifyLiquidities","type":"function","stateMutability":"payable","inputs":[{"name":"unlockData","type":"bytes"},{"name":"deadline","type":"uint256"}],"outputs":[]},
{"name":"getPoolAndPositionInfo","type":"function","stateMutability":"view","inputs":[{"name":"tokenId","type":"uint256"}],"outputs":[{"name":"poolKey","type":"tuple","components":[{"name":"currency0","type":"address"},{"name":"currency1","type":"address"},{"name":"fee","type":"uint24"},{"name":"tickSpacing","type":"int24"},{"name":"hooks","type":"address"}]},{"name":"info","type":"uint256"}]},
{"name":"getPositionLiquidity","type":"function","stateMutability":"view","inputs":[{"name":"tokenId","type":"uint256"}],"outputs":[{"name":"liquidity","type":"uint128"}]},
{"name":"poolKeys","type":"function","stateMutability":"view","inputs":[{"name":"poolId","type":"bytes25"}],"outputs":[{"name":"currency0","type":"address"},{"name":"currency1","type":"address"},{"name":"fee","type":"uint24"},{"name":"tickSpacing","type":"int24"},{"name":"hooks","type":"address"}]},
{"name":"ownerOf","type":"function","stateMutability":"view","inputs":[{"name":"tokenId","type":"uint256"}],"outputs":[{"name":"","type":"address"}]},
{"name":"getSlot0","type":"function","stateMutability":"view","inputs":[{"name":"poolId","type":"bytes32"}],"outputs":[{"name":"sqrtPriceX96","type":"uint160"},{"name":"tick","type":"int24"},{"name":"protocolFee","type":"uint24"},{"name":"lpFee","type":"uint24"}]},
{"name":"getLiquidity","type":"function","stateMutability":"view","inputs":[{"name":"poolId","type":"bytes32"}],"outputs":[{"name":"liquidity","type":"uint128"}]},
{"name":"getFeeGrowthInside","type":"function","stateMutability":"view","inputs":[{"name":"poolId","type":"bytes32"},{"name":"tickLower","type":"int24"},{"name":"tickUpper","type":"int24"}],"outputs":[{"name":"feeGrowthInside0X128","type":"uint256"},{"name":"feeGrowthInside1X128","type":"uint256"}]},
{"name":"getPositionInfo","type":"function","stateMutability":"view","inputs":[{"name":"poolId","type":"bytes32"},{"name":"owner","type":"address"},{"name":"tickLower","type":"int24"},{"name":"tickUpper","type":"int24"},{"name":"salt","type":"bytes32"}],"outputs":[{"name":"liquidity","type":"uint128"},{"name":"feeGrowthInside0LastX128","type":"uint256"},{"name":"feeGrowthInside1LastX128","type":"uint256"}]}
]`

const tokenABI = `[
{"name":"approve","type":"function","stateMutability":"nonpayable","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"name":"","type":"bool"}]},
{"name":"allowance","type":"function","stateMutability":"view","inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],"outputs":[{"name":"","type":"uint256"}]},
{"name":"balanceOf","type":"function","stateMutability":"view","inputs":[{"name":"owner","type":"address"}],"outputs":[{"name":"","type":"uint256"}]},
{"name":"decimals","type":"function","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]}
]`

// permit2ABI matches the web app's Permit2 fragment.
const permit2ABI = `[
{"name":"approve","type":"function","stateMutability":"nonpayable","inputs":[{"name":"token","type":"address"},{"name":"spender","type":"address"},{"name":"amount","type":"uint160"},{"name":"expiration","type":"uint48"}],"outputs":[]},
{"name":"allowance","type":"function","stateMutability":"view","inputs":[{"name":"","type":"address"},{"name":"","type":"address"},{"name":"","type":"address"}],"outputs":[{"name":"amount","type":"uint160"},{"name":"expiration","type":"uint48"},{"name":"nonce","type":"uint48"}]}
]`

var (
	v3Manager = mustABI(v3ManagerABI)
	v3Pool    = mustABI(v3PoolABI)
	v4        = mustABI(v4ABI)
	erc20     = mustABI(tokenABI)
	permit2   = mustABI(permit2ABI)
)

func mustABI(s string) abi.ABI {
	a, err := abi.JSON(strings.NewReader(s))
	if err != nil {
		panic(err)
	}
	return a
}

func pack(a abi.ABI, method string, args ...any) []byte {
	b, err := a.Pack(method, args...)
	if err != nil {
		// Every caller passes typed, validated values; a failure is a bug.
		panic(fmt.Sprintf("abi pack %s: %v", method, err))
	}
	return b
}

func hexData(b []byte) string { return "0x" + hex.EncodeToString(b) }

// Deadline is a unix-seconds transaction deadline.
type Deadline int64

func (d Deadline) big() *big.Int { return big.NewInt(int64(d)) }

// V3MintParams mirrors INonfungiblePositionManager.MintParams.
type V3MintParams struct {
	Token0         common.Address `abi:"token0"`
	Token1         common.Address `abi:"token1"`
	Fee            *big.Int       `abi:"fee"`
	TickLower      *big.Int       `abi:"tickLower"`
	TickUpper      *big.Int       `abi:"tickUpper"`
	Amount0Desired *big.Int       `abi:"amount0Desired"`
	Amount1Desired *big.Int       `abi:"amount1Desired"`
	Amount0Min     *big.Int       `abi:"amount0Min"`
	Amount1Min     *big.Int       `abi:"amount1Min"`
	Recipient      common.Address `abi:"recipient"`
	Deadline       *big.Int       `abi:"deadline"`
}

type v3IncreaseParams struct {
	TokenId        *big.Int `abi:"tokenId"`
	Amount0Desired *big.Int `abi:"amount0Desired"`
	Amount1Desired *big.Int `abi:"amount1Desired"`
	Amount0Min     *big.Int `abi:"amount0Min"`
	Amount1Min     *big.Int `abi:"amount1Min"`
	Deadline       *big.Int `abi:"deadline"`
}

type v3DecreaseParams struct {
	TokenId    *big.Int `abi:"tokenId"`
	Liquidity  *big.Int `abi:"liquidity"`
	Amount0Min *big.Int `abi:"amount0Min"`
	Amount1Min *big.Int `abi:"amount1Min"`
	Deadline   *big.Int `abi:"deadline"`
}

type v3CollectParams struct {
	TokenId    *big.Int       `abi:"tokenId"`
	Recipient  common.Address `abi:"recipient"`
	Amount0Max *big.Int       `abi:"amount0Max"`
	Amount1Max *big.Int       `abi:"amount1Max"`
}

// V3Mint encodes NonfungiblePositionManager.mint.
func V3Mint(p V3MintParams) []byte { return pack(v3Manager, "mint", p) }

// V3Increase encodes increaseLiquidity, as the v3-sdk addCallParameters does
// for an existing token id.
func V3Increase(tokenID *big.Int, desired, min Amounts, deadline Deadline) []byte {
	return pack(v3Manager, "increaseLiquidity", v3IncreaseParams{tokenID, desired.Amount0, desired.Amount1, min.Amount0, min.Amount1, deadline.big()})
}

func v3Collect(tokenID *big.Int, recipient common.Address) []byte {
	return pack(v3Manager, "collect", v3CollectParams{tokenID, recipient, MaxUint128, MaxUint128})
}

// V3Collect encodes collect of everything owed to recipient, like the web
// app's claimLiquidityFees and removeLiquidity.
func V3Collect(tokenID *big.Int, recipient common.Address) []byte { return v3Collect(tokenID, recipient) }

// V3Decrease encodes multicall([decreaseLiquidity, collect]) as the web app's
// removeLiquidity does; burn appends burn(tokenId) to close the position. The
// periphery rejects a zero-liquidity decrease, so closing an already emptied
// position only collects and burns.
func V3Decrease(tokenID, liquidity *big.Int, min Amounts, deadline Deadline, recipient common.Address, burn bool) []byte {
	var calls [][]byte
	if liquidity.Sign() > 0 {
		calls = append(calls, pack(v3Manager, "decreaseLiquidity", v3DecreaseParams{tokenID, liquidity, min.Amount0, min.Amount1, deadline.big()}))
	}
	calls = append(calls, v3Collect(tokenID, recipient))
	if burn {
		calls = append(calls, pack(v3Manager, "burn", tokenID))
	}
	return pack(v3Manager, "multicall", calls)
}

// V4 PositionManager actions (v4-periphery Actions.sol).
const (
	actionIncreaseLiquidity = 0x00
	actionDecreaseLiquidity = 0x01
	actionMintPosition      = 0x02
	actionBurnPosition      = 0x03
	actionSettle            = 0x0b
	actionSettlePair        = 0x0d
	actionTakePair          = 0x11
	actionCloseCurrency     = 0x12
	actionSweep             = 0x14
)

// PoolKey is a Uniswap v4 pool key. Currency 0x0 is the native token.
type PoolKey struct {
	Currency0   common.Address `abi:"currency0"`
	Currency1   common.Address `abi:"currency1"`
	Fee         *big.Int       `abi:"fee"`
	TickSpacing *big.Int       `abi:"tickSpacing"`
	Hooks       common.Address `abi:"hooks"`
}

func abiType(t string, components ...abi.ArgumentMarshaling) abi.Type {
	ty, err := abi.NewType(t, "", components)
	if err != nil {
		panic(err)
	}
	return ty
}

var (
	tAddress = abiType("address")
	tUint24  = abiType("uint24")
	tInt24   = abiType("int24")
	tUint128 = abiType("uint128")
	tUint256 = abiType("uint256")
	tBool    = abiType("bool")
	tBytes   = abiType("bytes")
	tBytesN  = abiType("bytes[]")
	tPoolKey = abiType("tuple",
		abi.ArgumentMarshaling{Name: "currency0", Type: "address"},
		abi.ArgumentMarshaling{Name: "currency1", Type: "address"},
		abi.ArgumentMarshaling{Name: "fee", Type: "uint24"},
		abi.ArgumentMarshaling{Name: "tickSpacing", Type: "int24"},
		abi.ArgumentMarshaling{Name: "hooks", Type: "address"})
)

func encode(types []abi.Type, values ...any) []byte {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		args[i] = abi.Argument{Type: t}
	}
	b, err := args.Pack(values...)
	if err != nil {
		panic(fmt.Sprintf("abi encode: %v", err))
	}
	return b
}

// PoolID is keccak256(abi.encode(poolKey)), the web app's getV4PoolId.
func (k PoolKey) PoolID() common.Hash {
	return crypto.Keccak256Hash(encode([]abi.Type{tAddress, tAddress, tUint24, tInt24, tAddress}, k.Currency0, k.Currency1, k.Fee, k.TickSpacing, k.Hooks))
}

// HasNative reports whether one side of the pool is the native token.
func (k PoolKey) HasNative() bool {
	return k.Currency0 == (common.Address{}) || k.Currency1 == (common.Address{})
}

func (k PoolKey) native() common.Address {
	if k.Currency0 == (common.Address{}) {
		return k.Currency0
	}
	return k.Currency1
}

type v4Plan struct {
	actions []byte
	params  [][]byte
}

func (p *v4Plan) add(action byte, param []byte) {
	p.actions = append(p.actions, action)
	p.params = append(p.params, param)
}

func (p *v4Plan) modify(deadline Deadline) []byte {
	unlock := encode([]abi.Type{tBytes, tBytesN}, p.actions, p.params)
	return pack(v4, "modifyLiquidities", unlock, deadline.big())
}

// settle pays the open deltas, then closes both currencies, as the web app's
// deployV4Position does. A native pool sweeps leftover native back to the
// owner so excess msg.value never stays in the PositionManager.
func (p *v4Plan) settle(k PoolKey, owner common.Address) {
	if k.HasNative() {
		p.add(actionSettle, encode([]abi.Type{tAddress, tUint256, tBool}, k.Currency0, new(big.Int), true))
		p.add(actionSettle, encode([]abi.Type{tAddress, tUint256, tBool}, k.Currency1, new(big.Int), true))
	} else {
		p.add(actionSettlePair, encode([]abi.Type{tAddress, tAddress}, k.Currency0, k.Currency1))
	}
	p.add(actionCloseCurrency, encode([]abi.Type{tAddress}, k.Currency0))
	p.add(actionCloseCurrency, encode([]abi.Type{tAddress}, k.Currency1))
	if k.HasNative() {
		p.add(actionSweep, encode([]abi.Type{tAddress, tAddress}, k.native(), owner))
	}
}

// take sends both currencies to recipient (TAKE_PAIR), sweeping native like
// the web app's removeV4Liquidity and collectV4Fees.
func (p *v4Plan) take(k PoolKey, recipient common.Address) {
	p.add(actionTakePair, encode([]abi.Type{tAddress, tAddress, tAddress}, k.Currency0, k.Currency1, recipient))
	if k.HasNative() {
		p.add(actionSweep, encode([]abi.Type{tAddress, tAddress}, k.native(), recipient))
	}
}

// V4Mint encodes MINT_POSITION followed by settlement.
func V4Mint(k PoolKey, r Range, liquidity *big.Int, max Amounts, owner common.Address, deadline Deadline) []byte {
	var p v4Plan
	p.add(actionMintPosition, encode([]abi.Type{tPoolKey, tInt24, tInt24, tUint256, tUint128, tUint128, tAddress, tBytes},
		k, big.NewInt(int64(r.Lower)), big.NewInt(int64(r.Upper)), liquidity, max.Amount0, max.Amount1, owner, []byte{}))
	p.settle(k, owner)
	return p.modify(deadline)
}

// V4Increase encodes INCREASE_LIQUIDITY followed by settlement.
func V4Increase(k PoolKey, tokenID, liquidity *big.Int, max Amounts, owner common.Address, deadline Deadline) []byte {
	var p v4Plan
	p.add(actionIncreaseLiquidity, encode([]abi.Type{tUint256, tUint256, tUint128, tUint128, tBytes}, tokenID, liquidity, max.Amount0, max.Amount1, []byte{}))
	p.settle(k, owner)
	return p.modify(deadline)
}

// V4Decrease encodes DECREASE_LIQUIDITY (zero liquidity collects fees only)
// followed by TAKE_PAIR.
func V4Decrease(k PoolKey, tokenID, liquidity *big.Int, min Amounts, recipient common.Address, deadline Deadline) []byte {
	var p v4Plan
	p.add(actionDecreaseLiquidity, encode([]abi.Type{tUint256, tUint256, tUint128, tUint128, tBytes}, tokenID, liquidity, min.Amount0, min.Amount1, []byte{}))
	p.take(k, recipient)
	return p.modify(deadline)
}

// V4Burn encodes BURN_POSITION followed by TAKE_PAIR: removes all liquidity,
// collects fees and burns the NFT.
func V4Burn(k PoolKey, tokenID *big.Int, min Amounts, recipient common.Address, deadline Deadline) []byte {
	var p v4Plan
	p.add(actionBurnPosition, encode([]abi.Type{tUint256, tUint128, tUint128, tBytes}, tokenID, min.Amount0, min.Amount1, []byte{}))
	p.take(k, recipient)
	return p.modify(deadline)
}

// ERC20Approve encodes approve(spender, amount).
func ERC20Approve(spender common.Address, amount *big.Int) []byte {
	return pack(erc20, "approve", spender, amount)
}

// Permit2Approve encodes Permit2.approve(token, spender, amount, expiration).
func Permit2Approve(token, spender common.Address, amount *big.Int, expiration uint64) []byte {
	return pack(permit2, "approve", token, spender, amount, new(big.Int).SetUint64(expiration))
}
