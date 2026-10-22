package onchain

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/privy"
)

const agent = "0x1111111111111111111111111111111111111111"
const master = "0x2222222222222222222222222222222222222222"
const hash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeRPC struct {
	gas       *big.Int
	receipt   func() (*bool, error)
	allowance *big.Int
}

func (f fakeRPC) Balance(context.Context, uint64, string) (*big.Int, error) {
	return new(big.Int).Set(f.gas), nil
}
func (fakeRPC) GasPrice(context.Context, uint64) (*big.Int, error) { return big.NewInt(1), nil }
func (f fakeRPC) Call(_ context.Context, _ uint64, _ string, data string) (*big.Int, error) {
	if strings.HasPrefix(data, "0xdd62ed3e") && f.allowance != nil {
		return new(big.Int).Set(f.allowance), nil
	}
	return big.NewInt(10000000), nil
}
func (f fakeRPC) Receipt(context.Context, uint64, string) (*bool, error) { return f.receipt() }
func (fakeRPC) Code(_ context.Context, _ uint64, address string) (string, error) {
	if strings.EqualFold(address, PrivyKernelImplementation) {
		return "0x6000", nil
	}
	return "0x", nil
}

type fakeJournal struct {
	state   string
	hashes  []string
	batch   []journal.Batch
	skipped bool
	marked  []int
}

func (j *fakeJournal) MarkStepHandoff(_ string, step int) error {
	j.marked = append(j.marked, step)
	return nil
}

func (j *fakeJournal) SkipApproval(string) error { j.skipped = true; return nil }

func (j *fakeJournal) AdvanceOnchain(_, from, to, h string) error {
	if j.state != from {
		return errors.New("invalid transition")
	}
	j.state = to
	if h != "" {
		j.hashes = append(j.hashes, h)
	}
	return nil
}

type fakeSender struct {
	calls int
	send  func() (string, error)
}

func (s *fakeSender) SendTransaction(_ context.Context, _ string, _ privy.Transaction) (string, error) {
	s.calls++
	return s.send()
}
func intent() journal.OnchainIntent {
	return journal.OnchainIntent{ID: "test-send", Kind: "send", ChainID: 42161, Sender: agent, Recipient: master, Token: "native", Amount: "0.001", Templates: []journal.OnchainTemplate{{ChainID: 42161, To: master, Value: "0x1"}}}
}
func TestExecuteTimeoutNeverRebroadcasts(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	s := &fakeSender{send: func() (string, error) { return "", context.DeadlineExceeded }}
	x := Executor{RPC: fakeRPC{gas: big.NewInt(10000000)}, Sender: s, Journal: j}
	err := x.Execute(context.Background(), intent())
	if !errors.Is(err, ErrUnknown) || j.state != "unknown" || s.calls != 1 {
		t.Fatalf("err=%v state=%s calls=%d", err, j.state, s.calls)
	}
}
func TestReceiptTimeoutAfterHash(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	s := &fakeSender{send: func() (string, error) { return hash, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	x := Executor{RPC: fakeRPC{gas: big.NewInt(10000000), receipt: func() (*bool, error) { return nil, nil }}, Sender: s, Journal: j, Poll: time.Millisecond}
	err := x.Execute(ctx, intent())
	if !errors.Is(err, ErrUnknown) || j.state != "sent" || strings.Join(j.hashes, ",") != hash || s.calls != 1 {
		t.Fatalf("err=%v state=%s hashes=%v calls=%d", err, j.state, j.hashes, s.calls)
	}
}

func TestReceiptRPCFailureThenSuccessDoesNotRebroadcast(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	s := &fakeSender{send: func() (string, error) { return hash, nil }}
	calls := 0
	x := Executor{RPC: fakeRPC{gas: big.NewInt(10000000), receipt: func() (*bool, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("RPC HTTP 403")
		}
		ok := true
		return &ok, nil
	}}, Sender: s, Journal: j, Poll: time.Millisecond}
	if err := x.Execute(context.Background(), intent()); err != nil || j.state != "confirmed" || calls != 2 || s.calls != 1 || len(j.hashes) != 1 {
		t.Fatalf("err=%v state=%s receiptCalls=%d sends=%d hashes=%v", err, j.state, calls, s.calls, j.hashes)
	}
}

func TestReceiptRPCFailurePreservesSentHash(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	s := &fakeSender{send: func() (string, error) { return hash, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Millisecond)
	defer cancel()
	x := Executor{RPC: fakeRPC{gas: big.NewInt(10000000), receipt: func() (*bool, error) {
		return nil, errors.New("RPC HTTP 403")
	}}, Sender: s, Journal: j, Poll: time.Millisecond}
	if err := x.Execute(ctx, intent()); !errors.Is(err, ErrReceiptUpstream) || j.state != "sent" || len(j.hashes) != 1 || s.calls != 1 {
		t.Fatalf("err=%v state=%s hashes=%v sends=%d", err, j.state, j.hashes, s.calls)
	}
}

func (j *fakeJournal) SetOnchainBatch(_, state string, b journal.Batch) error {
	if j.state != state {
		return errors.New("invalid batch state")
	}
	j.batch = append(j.batch, b)
	return nil
}

type fakeBatch struct {
	sends          int
	send           func() (string, error)
	latest         uint64
	pending        uint64
	mined          *ChainTx // returned once latest > nonce
	afterSendMined bool
	nonceErr       map[uint64]error
}

func (f *fakeBatch) SendCalls(context.Context, string, uint64, []privy.Call) (string, error) {
	f.sends++
	if f.afterSendMined {
		f.latest, f.pending = f.latest+1, f.pending+1
	}
	return f.send()
}
func (f *fakeBatch) Nonces(context.Context, uint64, string) (uint64, uint64, error) {
	return f.latest, f.pending, nil
}
func (f *fakeBatch) BlockNumber(context.Context, uint64) (uint64, error) { return 100, nil }
func (f *fakeBatch) FindNonceTx(_ context.Context, _ uint64, _ string, nonce, _ uint64) (*ChainTx, error) {
	if err, ok := f.nonceErr[nonce]; ok {
		return nil, err
	}
	if f.mined == nil || nonce != f.mined.Nonce {
		return nil, nil
	}
	return f.mined, nil
}

const usdc = "0x3333333333333333333333333333333333333333"
const router = "0x4444444444444444444444444444444444444444"

func swapIntent() journal.OnchainIntent {
	return journal.OnchainIntent{ID: "test-swap", Kind: "swap", ChainID: 42161, Sender: agent, Recipient: agent, Token: usdc, Amount: "0xf4240", Templates: []journal.OnchainTemplate{
		{ChainID: 42161, To: usdc, Value: "0x0", Data: "0x095ea7b3" + strings.Repeat("0", 24) + router[2:] + strings.Repeat("0", 58) + "0f4240", Token: usdc, Amount: "0xf4240"},
		{ChainID: 42161, To: router, Value: "0x0", Data: "0x48430c16cafe", Token: usdc, Amount: "0xf4240", Spender: router},
	}}
}

func TestSequentialApproveThenTradeAndResume(t *testing.T) {
	in := swapIntent()
	ok := true
	for _, start := range []int{0, 1} {
		j := &fakeJournal{state: "sending"}
		allowance := big.NewInt(0)
		if start == 1 {
			allowance.SetInt64(1000000)
		}
		s := &fakeSender{send: func() (string, error) { allowance.SetInt64(1000000); return hash, nil }}
		x := Executor{RPC: fakeRPC{gas: big.NewInt(10000000), allowance: allowance, receipt: func() (*bool, error) { return &ok, nil }}, Sender: s, Journal: j}
		if err := x.ExecuteSequential(context.Background(), in, start); err != nil {
			t.Fatalf("start=%d: %v", start, err)
		}
		if s.calls != 2-start || j.state != "confirmed" || len(j.hashes) != 2-start {
			t.Fatalf("start=%d calls=%d state=%s hashes=%v", start, s.calls, j.state, j.hashes)
		}
	}
}

func TestSequentialSkipsCoveredApproval(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	s := &fakeSender{send: func() (string, error) { return hash, nil }}
	ok := true
	x := Executor{RPC: fakeRPC{gas: big.NewInt(10000000), allowance: big.NewInt(1000000), receipt: func() (*bool, error) { return &ok, nil }}, Sender: s, Journal: j}
	if err := x.ExecuteSequential(context.Background(), swapIntent(), 0); err != nil || s.calls != 1 || !j.skipped {
		t.Fatalf("err=%v sends=%d skipped=%v", err, s.calls, j.skipped)
	}
}

type missingCodeRPC struct{ fakeRPC }

func (missingCodeRPC) Code(context.Context, uint64, string) (string, error) { return "0x", nil }
func TestBatchRefusesUndeployedImplementation(t *testing.T) {
	in := swapIntent()
	in.ChainID = 123456
	for i := range in.Templates {
		in.Templates[i].ChainID = in.ChainID
	}
	j := &fakeJournal{state: "sending"}
	b := &fakeBatch{send: func() (string, error) { return "transaction-id", nil }}
	x := Executor{RPC: missingCodeRPC{fakeRPC{gas: big.NewInt(10000000)}}, Journal: j}
	if err := x.ExecuteBatch(context.Background(), in, b, b); !errors.Is(err, ErrBatchUnavailable) || b.sends != 0 {
		t.Fatalf("err=%v sends=%d", err, b.sends)
	}
}

func TestRootstockUsesLegacyGasPrice(t *testing.T) {
	x := Executor{RPC: fakeRPC{gas: big.NewInt(10000000)}}
	price, err := x.legacyGasPrice(context.Background(), 30)
	if err != nil || price != "0x1" {
		t.Fatalf("legacy price=%s err=%v", price, err)
	}
}

func kernelInput(in journal.OnchainIntent) string {
	return encodeKernel(kernelBatchMode, templateCalls(in))
}

// The approval and swap go out as one wallet_sendCalls handoff. The nonce and
// block are durable before it, the mined self-call is found by nonce and its
// receipt confirms the whole intent.
func TestExecuteBatchSendsOnceAndConfirmsByNonce(t *testing.T) {
	in := swapIntent()
	ok := true
	j := &fakeJournal{state: "sending"}
	b := &fakeBatch{latest: 5, pending: 5, afterSendMined: true, send: func() (string, error) { return "b4966a89-8983-4b1b-a93a-b104799527f5", nil },
		mined: &ChainTx{Hash: hash, From: agent, To: agent, Input: kernelInput(in), Nonce: 5}}
	x := Executor{RPC: fakeRPC{gas: big.NewInt(1 << 40), receipt: func() (*bool, error) { return &ok, nil }}, Journal: j, Poll: time.Millisecond}
	if err := x.ExecuteBatch(context.Background(), in, b, b); err != nil {
		t.Fatal(err)
	}
	if b.sends != 1 || j.state != "confirmed" || strings.Join(j.hashes, ",") != hash || len(j.batch) == 0 || j.batch[0].Nonce != 5 || j.batch[0].FromBlock != 100-batchBlockMargin {
		t.Fatalf("sends=%d state=%s hashes=%v batch=%+v", b.sends, j.state, j.hashes, j.batch)
	}
}

// An ambiguous handoff must stay unknown with the nonce recorded, never failed.
func TestExecuteBatchAmbiguousHandoffStaysUnknown(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	b := &fakeBatch{latest: 5, pending: 5, send: func() (string, error) { return "", context.DeadlineExceeded }}
	x := Executor{RPC: fakeRPC{gas: big.NewInt(1 << 40)}, Journal: j}
	err := x.ExecuteBatch(context.Background(), swapIntent(), b, b)
	if !errors.Is(err, ErrUnknown) || errors.Is(err, ErrNotBroadcast) || j.state != "unknown" || b.sends != 1 || len(j.batch) != 1 {
		t.Fatalf("err=%v state=%s sends=%d batch=%v", err, j.state, b.sends, j.batch)
	}
}

// A different transaction at the reserved nonce is not this batch: the intent
// stays unresolved rather than adopting someone else's receipt.
func TestExecuteBatchRejectsForeignTransactionAtNonce(t *testing.T) {
	in := swapIntent()
	j := &fakeJournal{state: "sending"}
	foreign := swapIntent()
	foreign.Templates[1].Data = "0x48430c16beef"
	b := &fakeBatch{latest: 5, pending: 5, afterSendMined: true, send: func() (string, error) { return "b4966a89-8983-4b1b-a93a-b104799527f5", nil },
		mined: &ChainTx{Hash: hash, From: agent, To: agent, Input: kernelInput(foreign), Nonce: 5}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	x := Executor{RPC: fakeRPC{gas: big.NewInt(1 << 40)}, Journal: j, Poll: time.Millisecond}
	if err := x.ExecuteBatch(ctx, in, b, b); !errors.Is(err, ErrUnknown) || j.state != "unknown" || len(j.hashes) != 0 {
		t.Fatalf("err=%v state=%s hashes=%v", err, j.state, j.hashes)
	}
}

// Preflight failures close the intent before any handoff: a pending agent
// transaction (nonce not reservable) and insufficient gas both send nothing.
func TestExecuteBatchPreflightClosesWithoutSending(t *testing.T) {
	for name, tc := range map[string]struct {
		gas     int64
		pending uint64
		want    error
	}{"pending tx": {1 << 40, 6, ErrNotBroadcast}, "no gas": {0, 5, ErrGas}} {
		t.Run(name, func(t *testing.T) {
			j := &fakeJournal{state: "sending"}
			b := &fakeBatch{latest: 5, pending: tc.pending, send: func() (string, error) { return "x", nil }}
			x := Executor{RPC: fakeRPC{gas: big.NewInt(tc.gas)}, Journal: j}
			err := x.ExecuteBatch(context.Background(), swapIntent(), b, b)
			if !errors.Is(err, ErrNotBroadcast) || !errors.Is(err, tc.want) || j.state != "failed" || b.sends != 0 {
				t.Fatalf("err=%v state=%s sends=%d", err, j.state, b.sends)
			}
		})
	}
}

// A later sequential step that fails preflight after an earlier step confirmed
// closes the intent (nothing else was handed off) instead of leaving it
// blocking in "sending".
func TestExecuteLaterStepPreflightClosesPartial(t *testing.T) {
	in := intent()
	in.Templates = append(in.Templates, journal.OnchainTemplate{ChainID: 42161, To: master, Value: "0x1", Token: usdc, Amount: "0xffffffffff"})
	ok := true
	j := &fakeJournal{state: "sending"}
	s := &fakeSender{send: func() (string, error) { return hash, nil }}
	x := Executor{RPC: fakeRPC{gas: big.NewInt(10000000), receipt: func() (*bool, error) { return &ok, nil }}, Sender: s, Journal: j, Poll: time.Millisecond}
	err := x.Execute(context.Background(), in)
	if !errors.Is(err, ErrNotBroadcast) || !errors.Is(err, ErrBalance) || j.state != "failed" || s.calls != 1 || len(j.hashes) != 1 {
		t.Fatalf("err=%v state=%s sends=%d hashes=%v", err, j.state, s.calls, j.hashes)
	}
}

// A concurrent agent transaction can take the reserved nonce before the batch.
// Lookup must skip it and find the batch at the next nonce, not fail forever.
func TestLocateBatchSkipsNonceTakenByConcurrentTx(t *testing.T) {
	in := swapIntent()
	b := &fakeBatch{latest: 7, pending: 7, nonceErr: map[uint64]error{5: ErrNonceBeforeBlock},
		mined: &ChainTx{Hash: hash, From: agent, To: agent, Input: kernelInput(in), Nonce: 6}}
	got, err := LocateBatch(context.Background(), in, journal.Batch{Nonce: 5, FromBlock: 100}, b)
	if err != nil || got != hash {
		t.Fatalf("hash=%q err=%v", got, err)
	}
}

type failingJournal struct{ fakeJournal }

func (j *failingJournal) AdvanceOnchain(string, string, string, string) error {
	return errors.New("disk full")
}

// If the preflight closure cannot be journaled, the intent still blocks: the
// caller must see OUTCOME_UNKNOWN, never "nothing broadcast, prepare again".
func TestPreflightJournalFailureIsUnknown(t *testing.T) {
	j := &failingJournal{fakeJournal{state: "sending"}}
	b := &fakeBatch{latest: 5, pending: 5}
	x := Executor{RPC: fakeRPC{gas: big.NewInt(0)}, Journal: j}
	err := x.ExecuteBatch(context.Background(), swapIntent(), b, b)
	if !errors.Is(err, ErrUnknown) || errors.Is(err, ErrNotBroadcast) || b.sends != 0 {
		t.Fatalf("err=%v sends=%d", err, b.sends)
	}
}
