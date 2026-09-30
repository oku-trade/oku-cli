// Package decimal validates exact decimal strings. Financial values are never
// converted through float64.
package decimal

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// canonicalRE matches the backend canonical_decimal_string rule
// (account/proto/gfxcafe/oku/account/v2/validate.proto).
var canonicalRE = regexp.MustCompile(`^(0|[1-9]\d*)(\.\d*[1-9])?$`)

var inputRE = regexp.MustCompile(`^\d+(\.\d+)?$`)

// IsCanonical reports whether s is canonical (no leading/trailing zeros).
func IsCanonical(s string) bool { return canonicalRE.MatchString(s) }

// Canonical normalizes a non-negative plain decimal ("0.0100" -> "0.01").
// Signs, exponents, and blanks are rejected.
func Canonical(s string) (string, error) {
	if !inputRE.MatchString(s) {
		return "", fmt.Errorf("%q is not a plain non-negative decimal", s)
	}
	intPart, frac, _ := strings.Cut(s, ".")
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return intPart, nil
	}
	return intPart + "." + frac, nil
}

// Decimals returns the number of fractional digits of a canonical decimal.
func Decimals(s string) int {
	_, frac, _ := strings.Cut(s, ".")
	return len(frac)
}

// SigFigs returns significant digits of a canonical decimal.
func SigFigs(s string) int {
	digits := strings.TrimLeft(strings.Replace(s, ".", "", 1), "0")
	return len(digits)
}

// IsZero reports whether a canonical decimal is zero.
func IsZero(s string) bool { return s == "0" }

// Rat parses a canonical decimal exactly.
func Rat(s string) (*big.Rat, bool) { return new(big.Rat).SetString(s) }

// ValidatePerpPrice applies Hyperliquid perp price rules: at most 5
// significant figures unless integral, and at most 6-szDecimals decimals.
func ValidatePerpPrice(px string, szDecimals int) error {
	maxDec := 6 - szDecimals
	if Decimals(px) > maxDec {
		return fmt.Errorf("price %s has more than %d decimals", px, maxDec)
	}
	if Decimals(px) > 0 && SigFigs(px) > 5 {
		return fmt.Errorf("price %s has more than 5 significant figures", px)
	}
	return nil
}

// ValidateSize checks size precision against szDecimals.
func ValidateSize(sz string, szDecimals int) error {
	if IsZero(sz) {
		return fmt.Errorf("size must be greater than zero")
	}
	if Decimals(sz) > szDecimals {
		return fmt.Errorf("size %s has more than %d decimals", sz, szDecimals)
	}
	return nil
}
