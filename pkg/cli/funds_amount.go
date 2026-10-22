package cli

import (
	"github.com/oku-trade/oku-cli/pkg/journal"
	"math/big"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// routeGasLimit is a conservative gas limit for one swap or bridge execution
// (approve plus route call in a 7702 batch).
const routeGasLimit = 800_000

func isMaxAmount(s string) bool { return strings.EqualFold(s, "max") || strings.EqualFold(s, "all") }

// resolveMaxAmount reads the latest chain state, not indexed holdings. A native
// reserve covers ordinary gas but cannot guarantee future fees or L2 data fees.
func (a *App) resolveMaxAmount(e *env, chain uint64, token, wallet string, decimals int, destinations ...string) (string, *big.Int, error) {
	ctx, cancel := e.requestCtx()
	defer cancel()
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	var raw *big.Int
	var err error
	if token == "native" {
		raw, err = rpc.Balance(ctx, chain, wallet)
	} else {
		raw, err = rpc.Call(ctx, chain, token, "0x70a08231"+strings.Repeat("0", 24)+strings.ToLower(wallet[2:]))
	}
	if err != nil {
		return "", nil, output.Wrap(output.CodeUpstream, err, "reading onchain input balance")
	}
	if token == "native" {
		var units *big.Int
		if override := strings.TrimSpace(e.cmd.String("gas-reserve")); override != "" {
			units, err = parseNonnegativeUnits(override, 18)
			if err != nil {
				return "", nil, output.Wrap(output.CodeInvalidDecimal, err, "gas reserve")
			}
		} else {
			destination := wallet
			if len(destinations) > 0 {
				destination = destinations[0]
			}
			if !onchain.ValidAddress(destination) {
				destination = wallet
			}
			units, err = rpc.Estimate(ctx, chain, wallet, []journal.OnchainTemplate{{ChainID: chain, To: destination, Value: "0x0"}})
			if err != nil {
				return "", nil, output.Wrap(output.CodeUpstream, err, "estimating maximum spend gas")
			}
			// Swap and bridge calldata is unknown before the quote, so a transfer
			// estimate badly undersizes them. Floor the reserve at a route-sized
			// gas limit priced at the current gas price.
			if e.cmd.String("from") != "" {
				price, perr := rpc.GasPrice(ctx, chain)
				if perr != nil {
					return "", nil, output.Wrap(output.CodeUpstream, perr, "reading gas price")
				}
				if floor := new(big.Int).Mul(price, big.NewInt(routeGasLimit)); floor.Cmp(units) > 0 {
					units = floor
				}
			}
			// Estimate includes a 30% gas-limit buffer. Add another 50% and an L2 data-fee margin.
			units.Mul(units, big.NewInt(3))
			units.Div(units, big.NewInt(2))
			if chain != 1 {
				units.Add(units, big.NewInt(100000000000000))
			}
		}
		raw.Sub(raw, units)
	}
	if raw.Sign() <= 0 || raw.BitLen() > 256 {
		return "", nil, output.Errorf(output.CodeInsufficientBalance, "balance does not exceed gas reserve or is zero")
	}
	return formatUnits(raw, decimals), raw, nil
}

func parseNonnegativeUnits(s string, decimals int) (*big.Int, error) {
	if s == "0" {
		return big.NewInt(0), nil
	}
	return parseUnits(s, decimals)
}

func formatUnits(raw *big.Int, decimals int) string {
	if decimals == 0 {
		return raw.String()
	}
	digits := raw.String()
	if len(digits) <= decimals {
		digits = strings.Repeat("0", decimals-len(digits)+1) + digits
	}
	split := len(digits) - decimals
	fraction := strings.TrimRight(digits[split:], "0")
	if fraction == "" {
		return digits[:split]
	}
	return digits[:split] + "." + fraction
}
