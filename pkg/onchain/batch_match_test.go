package onchain

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain/onchaintest"
)

type kernelFixture struct {
	Tx struct {
		Hash, From, To, Input, Value, Nonce string
	} `json:"tx"`
	Sender    string                    `json:"sender"`
	Templates []journal.OnchainTemplate `json:"templates"`
}

func loadKernelFixture(t *testing.T) (journal.OnchainIntent, *ChainTx) {
	t.Helper()
	raw, err := os.ReadFile("testdata/kernel_batch_arbitrum.json")
	if err != nil {
		t.Fatal(err)
	}
	var f kernelFixture
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return journal.OnchainIntent{ID: "live", Kind: "swap", ChainID: 42161, Sender: f.Sender, Templates: f.Templates},
		&ChainTx{Hash: f.Tx.Hash, From: f.Tx.From, To: f.Tx.To, Input: f.Tx.Input, Value: f.Tx.Value}
}

// encodeKernel encodes calls with the shared fixture encoder, swapping in an
// arbitrary execution mode for adversarial cases.
func encodeKernel(mode string, calls []kernelCall) string {
	var cs []onchaintest.Call
	for _, c := range calls {
		cs = append(cs, onchaintest.Call{To: c.To, Value: c.Value, Data: c.Data})
	}
	enc := onchaintest.KernelBatch(cs)
	return enc[:10] + mode + enc[10+64:]
}

func templateCalls(in journal.OnchainIntent) []kernelCall {
	var calls []kernelCall
	for _, t := range in.Templates {
		v, _ := Quantity(t.Value)
		calls = append(calls, kernelCall{To: t.To, Value: v, Data: strings.ToLower(strings.TrimPrefix(t.Data, "0x"))})
	}
	return calls
}

// The real Privy/Kernel calldata from the live approve+swap must match its
// journal exactly; the encoder used for adversarial cases must reproduce it.
func TestBatchMatchesLivePrivyKernelCalldata(t *testing.T) {
	in, tx := loadKernelFixture(t)
	if !BatchMatches(in, tx) {
		t.Fatal("live batch rejected")
	}
	if got := encodeKernel(kernelBatchMode, templateCalls(in)); got != strings.ToLower(tx.Input) {
		t.Fatal("test encoder does not reproduce live Kernel calldata")
	}
}

// Any agent-signed self-call that merely contains the journaled bytes must be
// rejected: extra calls, reordering, a changed value or target, a non-batch or
// try-mode execute, nonzero tx value, or the payload hidden inside other data.
func TestBatchMatchesRejectsLookalikes(t *testing.T) {
	in, live := loadKernelFixture(t)
	calls := templateCalls(in)
	other := kernelCall{To: "0x" + strings.Repeat("66", 20), Value: big.NewInt(0), Data: "a9059cbb"}
	mutate := func(f func([]kernelCall) []kernelCall) string {
		cp := make([]kernelCall, len(calls))
		copy(cp, calls)
		return encodeKernel(kernelBatchMode, f(cp))
	}
	cases := map[string]*ChainTx{
		"extra call":    {Input: mutate(func(c []kernelCall) []kernelCall { return append(c, other) })},
		"reordered":     {Input: mutate(func(c []kernelCall) []kernelCall { return []kernelCall{c[1], c[0]} })},
		"value changed": {Input: mutate(func(c []kernelCall) []kernelCall { c[1].Value = big.NewInt(1); return c })},
		"target changed": {Input: mutate(func(c []kernelCall) []kernelCall {
			c[0].To = other.To
			return c
		})},
		"try mode":         {Input: encodeKernel("0101"+strings.Repeat("0", 60), calls)},
		"single-call mode": {Input: encodeKernel(strings.Repeat("0", 64), calls)},
		"payload embedded": {Input: mutate(func(c []kernelCall) []kernelCall {
			return []kernelCall{{To: other.To, Value: big.NewInt(0), Data: strings.TrimPrefix(live.Input, "0x")}}
		})},
		"tx value": {Input: live.Input, Value: "0x1"},
		"trailing": {Input: live.Input + strings.Repeat("00", 31) + "01"},
	}
	for name, tx := range cases {
		t.Run(name, func(t *testing.T) {
			tx.From, tx.To = live.From, live.To
			if BatchMatches(in, tx) {
				t.Fatal("lookalike accepted")
			}
		})
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(live.Input, "0x")); err != nil {
		t.Fatal(err)
	}
}
