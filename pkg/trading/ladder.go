package trading

import (
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/oku-trade/oku-cli/pkg/decimal"
)

// AggressivePrice follows the backend's market and trigger contract: arithmetic
// is truncated to eight decimals, then price precision is truncated downward
// for either side. WorstPrice deliberately uses a different sell-side bound.
func AggressivePrice(reference string, szDecimals int, isBuy bool, bps int) (string, error) {
	if !positive(reference) || szDecimals < 0 || szDecimals > 6 || bps <= 0 || bps >= 10000 {
		return "", fmt.Errorf("invalid aggressive price inputs")
	}
	ref, _ := decimal.Rat(reference)
	ref = truncateRat(ref, 8)
	multiplier := 10000 - bps
	if isBuy {
		multiplier = 10000 + bps
	}
	value := truncateRat(new(big.Rat).Mul(ref, big.NewRat(int64(multiplier), 10000)), 8)
	return truncatePrice(value, szDecimals)
}

func truncateRat(value *big.Rat, places int) *big.Rat {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	units := new(big.Int).Quo(new(big.Int).Mul(value.Num(), scale), value.Denom())
	return new(big.Rat).SetFrac(units, scale)
}

func truncatePrice(value *big.Rat, szDecimals int) (string, error) {
	d := 6 - szDecimals
	truncated := truncateRat(value, d)
	s, err := decimal.Canonical(truncated.FloatString(d))
	if err != nil {
		return "", err
	}
	if decimal.Decimals(s) > 0 && decimal.SigFigs(s) > 5 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil)
		units := new(big.Int).Quo(new(big.Int).Mul(truncated.Num(), scale), truncated.Denom())
		divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(units.String())-5)), nil)
		units.Mul(new(big.Int).Quo(units, divisor), divisor)
		s, err = decimal.Canonical(new(big.Rat).SetFrac(units, scale).FloatString(d))
		if err != nil {
			return "", err
		}
	}
	if !positive(s) {
		return "", fmt.Errorf("price truncates to zero")
	}
	return s, nil
}

// ScaleOrders reproduces the backend ladder, including its float64 geometric
// weights and final-child remainder allocation. Prices and returned economics
// remain canonical strings. Approval binds every returned child exactly.
func ScaleOrders(assetID int, isBuy bool, total, start, end, skew string, count, szDecimals int, tif string, reduceOnly bool) ([]ApprovedOrder, error) {
	if !validAsset(assetID) || count < 2 || count > 100 || szDecimals < 0 || szDecimals > 6 || !positive(total) || !positive(start) || !positive(end) || !positive(skew) {
		return nil, fmt.Errorf("invalid scale inputs")
	}
	if err := decimal.ValidateSize(total, szDecimals); err != nil {
		return nil, err
	}
	execution, err := NewLimitExecution(tif)
	if err != nil {
		return nil, err
	}
	skewFloat, err := strconv.ParseFloat(skew, 64)
	if err != nil || math.IsNaN(skewFloat) || math.IsInf(skewFloat, 0) || skewFloat <= 0 {
		return nil, fmt.Errorf("invalid scale skew")
	}
	totalRat, _ := decimal.Rat(total)
	totalFloat, _ := totalRat.Float64()
	if math.IsInf(totalFloat, 0) || totalFloat <= 0 {
		return nil, fmt.Errorf("scale size out of range")
	}
	startRat, _ := decimal.Rat(start)
	endRat, _ := decimal.Rat(end)
	startRat = truncateRat(startRat, 6)
	endRat = truncateRat(endRat, 6)
	step := truncateRat(new(big.Rat).Quo(new(big.Rat).Sub(endRat, startRat), big.NewRat(int64(count-1), 1)), 6)
	weights := make([]float64, count)
	weightSum := 0.0
	for i := range weights {
		weights[i] = math.Pow(skewFloat, float64(i)/float64(count-1))
		weightSum += weights[i]
	}
	if math.IsInf(weightSum, 0) || math.IsNaN(weightSum) || weightSum <= 0 {
		return nil, fmt.Errorf("scale weights out of range")
	}
	orders := make([]ApprovedOrder, count)
	sizeSum := new(big.Rat)
	sizes := make([]*big.Rat, count)
	for i, weight := range weights {
		childFloat := totalFloat * weight / weightSum
		child, ok := decimal.Rat(strconv.FormatFloat(childFloat, 'f', szDecimals+8, 64))
		if !ok {
			return nil, fmt.Errorf("invalid scale child size")
		}
		sizes[i] = truncateRat(child, szDecimals)
		if sizes[i].Sign() <= 0 {
			return nil, fmt.Errorf("scale child size truncates to zero")
		}
		sizeSum.Add(sizeSum, sizes[i])
	}
	sizes[count-1].Add(sizes[count-1], new(big.Rat).Sub(totalRat, sizeSum))
	for i := range orders {
		px := new(big.Rat).Add(startRat, new(big.Rat).Mul(step, big.NewRat(int64(i), 1)))
		if i == count-1 {
			px = endRat
		}
		price, err := truncatePrice(px, szDecimals)
		if err != nil {
			return nil, err
		}
		size, err := decimal.Canonical(sizes[i].FloatString(szDecimals))
		if err != nil {
			return nil, err
		}
		if !positive(size) {
			return nil, fmt.Errorf("scale child size truncates to zero")
		}
		orders[i] = ApprovedOrder{AssetID: assetID, IsBuy: isBuy, Price: price, Size: size, ReduceOnly: reduceOnly, Execution: execution}
	}
	return orders, nil
}
