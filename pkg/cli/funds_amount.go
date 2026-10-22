package cli

import (
	"math/big"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

func isMaxAmount(s string) bool { return strings.EqualFold(s, "max") || strings.EqualFold(s, "all") }

// resolveMaxAmount reads the latest chain state, not indexed holdings. A native
// reserve covers ordinary gas but cannot guarantee future fees or L2 data fees.
func (a *App) resolveMaxAmount(e *env, chain uint64, token, wallet string, decimals int) (string, *big.Int, error) {
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
		reserve := "0.001"
		if chain == 1 {
			reserve = "0.01"
		}
		if override := strings.TrimSpace(e.cmd.String("gas-reserve")); override != "" {
			reserve = override
		}
		units, parseErr := parseNonnegativeUnits(reserve, 18)
		if parseErr != nil {
			return "", nil, output.Wrap(output.CodeInvalidDecimal, parseErr, "gas reserve")
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
