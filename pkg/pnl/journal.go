package pnl

import (
	"math/big"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

// Skipped counts journal swaps FromRecords left out.
type Skipped struct {
	// OtherRecipient swaps delivered their output to an address other than
	// the agent, so they did not change the agent's position.
	OtherRecipient int `json:"other_recipient"`
	// Invalid swaps had unparseable amounts or tokens.
	Invalid int `json:"invalid"`
}

// FromRecords converts an agent's journal swaps (journal.ListOnchainSwaps) to
// Swaps. A record with a receipt fill uses the measured amount and gas; one
// without falls back to MinOut and is Approximate.
func FromRecords(records []*journal.OnchainRecord, agent string, nativeAliases ...string) ([]Swap, Skipped) {
	var out []Swap
	var skipped Skipped
	for _, r := range records {
		if !strings.EqualFold(r.Recipient, agent) || !strings.EqualFold(r.Sender, agent) {
			skipped.OtherRecipient++
			continue
		}
		s := Swap{ID: r.ID, InToken: Key(r.Token, nativeAliases...), OutToken: Key(r.OutToken, nativeAliases...)}
		in, ok := new(big.Int).SetString(r.Amount, 10)
		if !ok {
			skipped.Invalid++
			continue
		}
		s.AmountIn = in
		received := r.MinOut
		if r.Fill != nil && r.Fill.Received != "" {
			received = r.Fill.Received
			if g, ok := new(big.Int).SetString(r.Fill.Gas, 10); ok {
				s.Gas = g
			}
			if v, ok := new(big.Rat).SetString(r.Fill.InValue); ok && r.Fill.InValue != "" && v.Sign() > 0 {
				s.InValue = v
			}
		} else {
			s.Approximate = true
		}
		out2, ok := new(big.Int).SetString(received, 10)
		if !ok || in.Sign() <= 0 || out2.Sign() <= 0 || s.InToken == s.OutToken {
			skipped.Invalid++
			continue
		}
		s.AmountOut = out2
		out = append(out, s)
	}
	return out, skipped
}
