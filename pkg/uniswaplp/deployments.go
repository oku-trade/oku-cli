package uniswaplp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gfx-labs/chain-config/dist/networks"
)

// Version is a Uniswap concentrated-liquidity protocol version.
type Version string

const (
	V3 Version = "v3"
	V4 Version = "v4"
)

// ParseVersion validates a user-supplied version.
func ParseVersion(s string) (Version, error) {
	switch Version(strings.ToLower(strings.TrimSpace(s))) {
	case V3:
		return V3, nil
	case V4:
		return V4, nil
	}
	return "", fmt.Errorf("version must be v3 or v4, got %q", s)
}

// Deployment is the set of Uniswap contracts the CLI uses on one chain.
type Deployment struct {
	ChainID uint64
	Version Version
	// Manager is the NonfungiblePositionManager (v3) or PositionManager (v4).
	Manager common.Address
	// Factory is the v3 pool factory. Zero for v4.
	Factory common.Address
	// StateView and Permit2 are v4 only.
	StateView common.Address
	Permit2   common.Address
	// WrappedNative is the v3 wrapped native token, used when depositing native.
	WrappedNative common.Address
	NativeSymbol  string
}

// ErrUnsupported reports a chain or version without an LP deployment the web
// app offers.
var ErrUnsupported = errors.New("uniswap liquidity is not supported on this chain")

// Lookup resolves the deployment the Oku web app uses for liquidity. Like the
// web app it excludes deprecated and lite chains; v4 needs a PositionManager
// and StateView, v3 a NonfungiblePositionManager and factory.
func Lookup(chain uint64, v Version) (Deployment, error) {
	n, err := networks.NetworkByNumber(chain)
	if err != nil || n == nil {
		return Deployment{}, fmt.Errorf("%w: unknown chain %d", ErrUnsupported, chain)
	}
	if n.Deprecated || n.LiteChain {
		return Deployment{}, fmt.Errorf("%w: chain %d", ErrUnsupported, chain)
	}
	d := Deployment{ChainID: chain, Version: v, NativeSymbol: n.NativeCurrency.Symbol}
	zero := common.Address{}
	switch v {
	case V3:
		d.Manager, d.Factory, d.WrappedNative = n.Uniswap.NonfungiblePositionManager, n.Uniswap.PoolFactory, n.Uniswap.WrappedNativeAddress
		if d.Manager == zero || d.Factory == zero {
			return Deployment{}, fmt.Errorf("%w: no Uniswap v3 position manager on chain %d", ErrUnsupported, chain)
		}
	case V4:
		d.Manager, d.StateView, d.Permit2 = n.Uniswapv4.PositionManager, n.Uniswapv4.StateView, n.Uniswapv4.Permit2
		if d.Manager == zero || d.StateView == zero {
			return Deployment{}, fmt.Errorf("%w: no Uniswap v4 position manager on chain %d", ErrUnsupported, chain)
		}
		if d.Permit2 == zero {
			d.Permit2 = canonicalPermit2
		}
	default:
		return Deployment{}, fmt.Errorf("unknown version %q", v)
	}
	return d, nil
}

// canonicalPermit2 is the web app's getPermit2Address fallback.
var canonicalPermit2 = common.HexToAddress("0x000000000022D473030F116dDEE9F6B43aC78BA3")

// Supported lists every (chain, version) pair with an LP deployment.
func Supported() []Deployment {
	var out []Deployment
	for _, n := range networks.AllNetworks {
		if n.ChainId <= 0 {
			continue
		}
		for _, v := range []Version{V3, V4} {
			if d, err := Lookup(uint64(n.ChainId), v); err == nil {
				out = append(out, d)
			}
		}
	}
	return out
}

// V4DefaultTickSpacing is the web app's V4_FEE_TIER_DEFAULTS.
func V4DefaultTickSpacing(fee int) (int, bool) {
	switch fee {
	case 100:
		return 1, true
	case 500:
		return 10, true
	case 3000:
		return 60, true
	case 10000:
		return 200, true
	}
	return 0, false
}
