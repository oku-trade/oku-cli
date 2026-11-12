package journal

import (
	"errors"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

func chaseParent(t *testing.T, j *Journal) *Record {
	t.Helper()
	p, err := trading.NewChase(trading.ChasePlan{AssetID: 0, Side: "buy", TotalSize: "1", ReferencePx: "100", MaxDistance: "5", SizeDecimals: 2})
	if err != nil {
		t.Fatal(err)
	}
	in := intent("chase-parent", "https://one.example")
	in.Kind = trading.KindChase
	in.Chase = p
	in.Size = "1"
	in.Side = "buy"
	in.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	r, _, err := j.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChaseAtomicChildAndRecovery(t *testing.T) {
	j := open(t)
	parent := chaseParent(t, j)
	c, err := j.CreateChase(parent)
	if err != nil {
		t.Fatal(err)
	}
	stale := *c
	execution, err := trading.NewLimitExecution("ALO")
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := trading.NewAdvanced(trading.OrderPlan{Grouping: "na", Orders: []trading.ApprovedOrder{{AssetID: 0, IsBuy: true, Price: "100", Size: "1", Execution: execution}}, SizeDecimals: 2})
	if err != nil {
		t.Fatal(err)
	}
	child := parent.Intent
	child.ID = "chase-child"
	child.Kind = trading.KindOrder
	child.Chase = nil
	child.Advanced = advanced
	r, err := j.CreateChaseChild(c, child)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.PutChase(&stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	child.ID = "chase-stale-child"
	if _, err = j.CreateChaseChild(&stale, child); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale child: %v", err)
	}
	if _, err = j.Get(child.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back child survived: %v", err)
	}
	reopened, err := Open("p")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	recovered, err := reopened.ChaseForChild(r.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != ChasePending || recovered.PendingIntent != r.Intent.ID || recovered.Digest != parent.Digest {
		t.Fatalf("lost child ownership: %+v", recovered)
	}
	unlock, err := j.LockChase(parent.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err = reopened.LockChase(parent.Intent.ID); !errors.Is(err, config.ErrLocked) {
		t.Fatalf("concurrent owner allowed: %v", err)
	}
}

func TestChaseCancellationGateValidatesExactNativeAction(t *testing.T) {
	for _, kind := range []string{trading.KindCancelMany, trading.KindCancelTWAP} {
		t.Run(kind, func(t *testing.T) {
			j := open(t)
			blocker := created(t, j, intent("blocking-intent", "https://one.example"))
			blocker.State = StateUnknown
			if err := j.Put(blocker, "fixture unknown action"); err != nil {
				t.Fatal(err)
			}
			in := intent("cancel-native", "https://one.example")
			var payload trading.AdvancedPayload = trading.CancelManyPlan{Cancels: []trading.CancelTarget{{AssetID: 3, OrderID: 77}}}
			action := `{"type":"cancel","cancels":[{"a":3,"o":77}]}`
			if kind == trading.KindCancelTWAP {
				payload = trading.CancelTWAPPlan{AssetID: 3, TWAPID: 77}
				action = `{"type":"twapCancel","a":3,"t":77}`
			}
			advanced, err := trading.NewAdvanced(payload)
			if err != nil {
				t.Fatal(err)
			}
			in.Kind, in.Advanced = kind, advanced
			rec, _, err := j.Create(in)
			if err != nil {
				t.Fatal(err)
			}
			rec.State, rec.ActionID, rec.ActionJSON = StateCreated, "native-cancel", action
			if err = j.Put(rec, ""); err != nil {
				t.Fatal(err)
			}
			if _, err = j.BeginSubmission(rec, "fixture-signer-identifier"); err != nil {
				t.Fatalf("exact cancellation blocked: %v", err)
			}
			in.ID = "cancel-mismatch"
			bad, _, err := j.Create(in)
			if err != nil {
				t.Fatal(err)
			}
			bad.State, bad.ActionID, bad.ActionJSON = StateCreated, "mismatched-cancel", `{"type":"order","orders":[],"grouping":"na"}`
			if err = j.Put(bad, ""); err != nil {
				t.Fatal(err)
			}
			if _, err = j.BeginSubmission(bad, "fixture-signer-identifier"); !errors.Is(err, ErrConflict) {
				t.Fatalf("noncancel bypassed gate: %v", err)
			}
		})
	}
}
