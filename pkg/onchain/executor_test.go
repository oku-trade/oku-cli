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
	gas     *big.Int
	receipt func() (*bool, error)
}

func (f fakeRPC) Balance(context.Context, uint64, string) (*big.Int, error) {
	return new(big.Int).Set(f.gas), nil
}
func (fakeRPC) GasPrice(context.Context, uint64) (*big.Int, error) { return big.NewInt(1), nil }
func (fakeRPC) Call(context.Context, uint64, string, string) (*big.Int, error) {
	return big.NewInt(10000000), nil
}
func (f fakeRPC) Receipt(context.Context, uint64, string) (*bool, error) { return f.receipt() }

type fakeJournal struct {
	state  string
	hashes []string
}

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
