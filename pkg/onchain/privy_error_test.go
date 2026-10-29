package onchain

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/privy"
)

func TestPrivyHandoffErrorsNeverProveNotBroadcast(t *testing.T) {
	for _, cause := range []string{"nonce too low", "simulation failed", "execution reverted", "insufficient funds", "remote_error", "Unauthorized"} {
		for _, batch := range []bool{false, true} {
			t.Run(cause+map[bool]string{false: "/single", true: "/batch"}[batch], func(t *testing.T) {
				upstream := &privy.Error{Status: http.StatusBadRequest, Code: "transaction_broadcast_failure", Description: cause}
				if cause == "Unauthorized" {
					upstream.Status, upstream.Code = http.StatusUnauthorized, "Unauthorized"
				}
				j := &fakeJournal{state: "sending"}
				s := &fakeSender{send: func() (string, error) { return "", upstream }}
				b := &fakeBatch{latest: 5, pending: 5, send: func() (string, error) { return "", upstream }}
				x := Executor{RPC: fakeRPC{gas: big.NewInt(1 << 40)}, Sender: s, Journal: j}
				var err error
				if batch {
					err = x.ExecuteBatch(context.Background(), swapIntent(), b, b)
				} else {
					err = x.Execute(context.Background(), intent())
				}
				var recovered *privy.Error
				if !errors.Is(err, ErrUnknown) || errors.Is(err, ErrNotBroadcast) || !errors.As(err, &recovered) || recovered != upstream {
					t.Fatalf("unsafe outcome or lost upstream cause: %v", err)
				}
				if j.state != "unknown" || len(j.hashes) != 0 || s.calls+b.sends != 1 || !strings.Contains(err.Error(), cause) {
					t.Fatalf("err=%v journal=%+v sends=%d", err, j, s.calls+b.sends)
				}
				if batch && (len(j.batch) != 1 || j.batch[0].Nonce != 5) {
					t.Fatalf("lost batch recovery metadata: %+v", j.batch)
				}
			})
		}
	}
}

func TestPartialSequentialSuccessPreservedAfterPrivy400(t *testing.T) {
	j := &fakeJournal{state: "sending"}
	upstream := &privy.Error{Status: 400, Code: "transaction_broadcast_failure", Description: "nonce too low"}
	calls := 0
	allowance := big.NewInt(0)
	s := &fakeSender{send: func() (string, error) {
		calls++
		if calls == 1 {
			allowance.SetInt64(1000000)
			return hash, nil
		}
		return "", upstream
	}}
	ok := true
	x := Executor{RPC: fakeRPC{gas: big.NewInt(1 << 40), allowance: allowance, receipt: func() (*bool, error) { return &ok, nil }}, Sender: s, Journal: j}
	err := x.ExecuteSequential(context.Background(), swapIntent(), 0)
	var recovered *privy.Error
	if !errors.Is(err, ErrUnknown) || errors.Is(err, ErrNotBroadcast) || !errors.As(err, &recovered) || j.state != "unknown" || calls != 2 || len(j.hashes) != 1 || j.hashes[0] != hash {
		t.Fatalf("err=%v journal=%+v sends=%d", err, j, calls)
	}
}

type simulationRejectRPC struct{ fakeRPC }

func (simulationRejectRPC) Estimate(context.Context, uint64, string, []journal.OnchainTemplate) (*big.Int, error) {
	return nil, errors.New("simulation failed before provider handoff")
}

func TestLocalSimulationRejectionProvesNoHandoff(t *testing.T) {
	for _, batch := range []bool{false, true} {
		j := &fakeJournal{state: "sending"}
		s := &fakeSender{send: func() (string, error) { t.Fatal("unexpected handoff"); return "", nil }}
		b := &fakeBatch{send: s.send}
		x := Executor{RPC: simulationRejectRPC{fakeRPC{gas: big.NewInt(1 << 40)}}, Sender: s, Journal: j}
		var err error
		if batch {
			err = x.ExecuteBatch(context.Background(), swapIntent(), b, b)
		} else {
			err = x.Execute(context.Background(), intent())
		}
		if !errors.Is(err, ErrNotBroadcast) || errors.Is(err, ErrUnknown) || j.state != "failed" || s.calls+b.sends != 0 {
			t.Fatalf("err=%v journal=%+v sends=%d", err, j, s.calls+b.sends)
		}
	}
}

func TestPrivyCauseSurvivesJournalFailure(t *testing.T) {
	for _, batch := range []bool{false, true} {
		upstream := &privy.Error{Status: 400, Code: "transaction_broadcast_failure"}
		j := &failingJournal{fakeJournal{state: "sending"}}
		s := &fakeSender{send: func() (string, error) { return "", upstream }}
		b := &fakeBatch{latest: 5, pending: 5, send: s.send}
		x := Executor{RPC: fakeRPC{gas: big.NewInt(1 << 40)}, Sender: s, Journal: j}
		var err error
		if batch {
			err = x.ExecuteBatch(context.Background(), swapIntent(), b, b)
		} else {
			err = x.Execute(context.Background(), intent())
		}
		var recovered *privy.Error
		if !errors.Is(err, ErrUnknown) || errors.Is(err, ErrNotBroadcast) || !errors.As(err, &recovered) || !strings.Contains(err.Error(), "journal failure") || j.state != "sending" || s.calls+b.sends != 1 {
			t.Fatalf("err=%v journal=%+v sends=%d", err, j, s.calls+b.sends)
		}
	}
}
