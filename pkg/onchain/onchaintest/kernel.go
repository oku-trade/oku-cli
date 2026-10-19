// Package onchaintest provides test helpers for onchain batch fixtures.
package onchaintest

import (
	"math/big"
	"strings"
)

// Call is one batched call for KernelBatch.
type Call struct {
	To    string
	Value *big.Int
	Data  string // hex, with or without 0x
}

// KernelBatch ABI-encodes Kernel execute(batchMode, abi.encode(Execution[])),
// matching the calldata Privy's EIP-7702 wallet_sendCalls produces.
func KernelBatch(calls []Call) string {
	w := func(v *big.Int) string { return strings.Repeat("0", 64-len(v.Text(16))) + v.Text(16) }
	u := func(n int) string { return w(big.NewInt(int64(n))) }
	var heads, tails []string
	offset := 32 * len(calls)
	for _, c := range calls {
		data := strings.ToLower(strings.TrimPrefix(c.Data, "0x"))
		value := c.Value
		if value == nil {
			value = new(big.Int)
		}
		heads = append(heads, u(offset))
		pad := (len(data)/2 + 31) / 32 * 32
		el := strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(c.To, "0x")) + w(value) + u(96) + u(len(data)/2) + data + strings.Repeat("0", 2*pad-len(data))
		tails = append(tails, el)
		offset += len(el) / 2
	}
	exec := u(32) + u(len(calls)) + strings.Join(heads, "") + strings.Join(tails, "")
	pad := strings.Repeat("0", (64-len(exec)%64)%64)
	return "0xe9ae5c53" + "01" + strings.Repeat("0", 62) + u(64) + u(len(exec)/2) + exec + pad
}
