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
func TestExecutePersistsHashBeforeReceipt(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	s := &fakeSender{send: func() (string, error) { return hash, nil }}
	rpc := fakeRPC{gas: big.NewInt(10000000), receipt: func() (*bool, error) {
		if j.state != "sent" || len(j.hashes) != 1 || j.hashes[0] != hash {
			t.Fatal("receipt polled before hash committed")
		}
		yes := true
		return &yes, nil
	}}
	err := (Executor{RPC: rpc, Sender: s, Journal: j}).Execute(context.Background(), intent())
	if err != nil || j.state != "confirmed" || s.calls != 1 {
		t.Fatalf("err=%v state=%s calls=%d", err, j.state, s.calls)
	}
}
func TestExecuteInsufficientGasNeverBroadcasts(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	s := &fakeSender{send: func() (string, error) { t.Fatal("broadcasted without gas"); return "", nil }}
	err := (Executor{RPC: fakeRPC{gas: big.NewInt(0)}, Sender: s, Journal: j}).Execute(context.Background(), intent())
	if !errors.Is(err, ErrGas) || s.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, s.calls)
	}
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
	if !errors.Is(err, ErrUnknown) || j.state != "unknown" || strings.Join(j.hashes, ",") != hash || s.calls != 1 {
		t.Fatalf("err=%v state=%s hashes=%v calls=%d", err, j.state, j.hashes, s.calls)
	}
}
