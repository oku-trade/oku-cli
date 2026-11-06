package lending

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gfx-labs/chain-config/dist/networks"
)

// loopABI covers the swap legs of loop bundles: the Oku Router fill the
// swap service quotes, and the Euler Swapper / SwapVerifier periphery
// (evk-periphery src/Swaps). Its method names collide with vault methods,
// so Decode reports them with a "loop:" prefix.
var loopABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(`[
{"type":"function","name":"fillQuoteTokenToToken","inputs":[{"name":"sellTokenAddress","type":"address"},{"name":"buyTokenAddress","type":"address"},{"name":"target","type":"address"},{"name":"approvalTarget","type":"address"},{"name":"swapCallData","type":"bytes"},{"name":"sellAmount","type":"uint256"},{"name":"feeAmount","type":"uint256"},{"name":"recipient","type":"address"},{"name":"warrant","type":"tuple","components":[{"name":"nonce","type":"uint160"},{"name":"validBefore","type":"uint48"},{"name":"validAfter","type":"uint48"},{"name":"verifyingSigner","type":"address"},{"name":"signature","type":"bytes"}]}],"outputs":[]},
{"type":"function","name":"swap","inputs":[{"name":"params","type":"tuple","components":[{"name":"handler","type":"bytes32"},{"name":"mode","type":"uint256"},{"name":"account","type":"address"},{"name":"tokenIn","type":"address"},{"name":"tokenOut","type":"address"},{"name":"vaultIn","type":"address"},{"name":"accountIn","type":"address"},{"name":"receiver","type":"address"},{"name":"amountOut","type":"uint256"},{"name":"data","type":"bytes"}]}],"outputs":[]},
{"type":"function","name":"sweep","inputs":[{"name":"token","type":"address"},{"name":"amountMin","type":"uint256"},{"name":"to","type":"address"}],"outputs":[]},
{"type":"function","name":"deposit","inputs":[{"name":"token","type":"address"},{"name":"vault","type":"address"},{"name":"amountMin","type":"uint256"},{"name":"account","type":"address"}],"outputs":[]},
{"type":"function","name":"repay","inputs":[{"name":"token","type":"address"},{"name":"vault","type":"address"},{"name":"repayAmount","type":"uint256"},{"name":"account","type":"address"}],"outputs":[]},
{"type":"function","name":"multicall","inputs":[{"name":"calls","type":"bytes[]"}],"outputs":[]},
{"type":"function","name":"verifyAmountMinAndSkim","inputs":[{"name":"vault","type":"address"},{"name":"receiver","type":"address"},{"name":"amountMin","type":"uint256"},{"name":"deadline","type":"uint256"}],"outputs":[]},
{"type":"function","name":"verifyDebtMax","inputs":[{"name":"vault","type":"address"},{"name":"account","type":"address"},{"name":"amountMax","type":"uint256"},{"name":"deadline","type":"uint256"}],"outputs":[]}
]`))
	if err != nil {
		panic(err)
	}
	return a
}()

func packLoop(method string, args ...any) []byte {
	data, err := loopABI.Pack(method, args...)
	if err != nil {
		panic(fmt.Sprintf("lending: pack %s: %v", method, err))
	}
	return data
}

// RouterTrade is a decoded Oku Router fillQuoteTokenToToken call.
type RouterTrade struct {
	Sell, Buy, Recipient common.Address
	Amount, Fee          *big.Int
	ValidBefore          uint64 // warrant expiry, unix seconds
	Data                 []byte
}

// DecodeRouterTrade decodes and bounds router calldata from the swap
// service: a token-to-token fill whose fee is under 1% of the input.
func DecodeRouterTrade(data []byte) (RouterTrade, error) {
	d, err := Decode(common.Address{}, data)
	if err != nil {
		return RouterTrade{}, err
	}
	if d.Method != "loop:fillQuoteTokenToToken" {
		return RouterTrade{}, fmt.Errorf("swap is %s, not a router token-to-token fill", d.Method)
	}
	t := RouterTrade{Sell: d.Args[0].(common.Address), Buy: d.Args[1].(common.Address), Amount: d.Args[5].(*big.Int), Fee: d.Args[6].(*big.Int), Recipient: d.Args[7].(common.Address), Data: data}
	w := abi.ConvertType(d.Args[8], new(struct {
		Nonce           *big.Int
		ValidBefore     *big.Int
		ValidAfter      *big.Int
		VerifyingSigner common.Address
		Signature       []byte
	})).(*struct {
		Nonce           *big.Int
		ValidBefore     *big.Int
		ValidAfter      *big.Int
		VerifyingSigner common.Address
		Signature       []byte
	})
	if w.ValidBefore != nil && w.ValidBefore.IsUint64() {
		t.ValidBefore = w.ValidBefore.Uint64()
	}
	if t.Amount.Sign() <= 0 || new(big.Int).Mul(t.Fee, big.NewInt(100)).Cmp(t.Amount) >= 0 {
		return t, errors.New("router fill amount or fee is out of bounds")
	}
	return t, nil
}

// EulerSwap is the per-chain Euler swap periphery the backend loop uses.
// The addresses are Euler's public evk-periphery deployments.
type EulerSwap struct {
	EVC, Swapper, SwapVerifier common.Address
}

var eulerSwapDeployments = map[uint64]EulerSwap{
	56: {Swapper: common.HexToAddress("0x8e39500a6672D701616ED4943a5Cc5C79Ab38643"), SwapVerifier: common.HexToAddress("0x2d09e4C7Ad9c21Cf093046Da69030Dc17d01deFC")},
}

// EulerSwapFor returns the chain's Euler swap periphery.
func EulerSwapFor(chain uint64) (EulerSwap, error) {
	evc, err := Euler(chain)
	if err != nil {
		return EulerSwap{}, err
	}
	d, ok := eulerSwapDeployments[chain]
	if !ok {
		return EulerSwap{}, fmt.Errorf("euler swap periphery is not deployed on chain %d", chain)
	}
	d.EVC = evc
	return d, nil
}

// OkuRouter is the chain's Oku Router from chain-config.
func OkuRouter(chain uint64) (common.Address, error) {
	n, err := networks.NetworkByNumber(chain)
	if err != nil || n == nil || n.Oku.Router == (common.Address{}) {
		return common.Address{}, fmt.Errorf("no Oku Router on chain %d", chain)
	}
	return n.Oku.Router, nil
}
