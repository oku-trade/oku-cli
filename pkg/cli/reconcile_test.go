package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

// Regression for M-08: records beyond the first batch of 50 are reconciled
// in one call, with batches still bounded.
func TestReconcileReachesBeyondFirstBatch(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	p, err := (&env{app: NewApp("t")}).profile()
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	const total, unknowns = 123, 3
	for i := 0; i < total; i++ {
		in := trading.Intent{Version: 1, ID: fmt.Sprintf("rec-%03d", i), Kind: trading.KindOrder, Network: p.Network, Account: p.Account, APIURL: p.APIURL, CreatedAt: time.Unix(int64(i), 0).UTC().Format(time.RFC3339)}
		rec, _, err := j.Create(in)
		if err != nil {
			t.Fatal(err)
		}
		rec.State, rec.ActionID = journal.StateCreated, fmt.Sprintf("act-rec-%03d", i)
		if err = j.Put(rec, ""); err != nil {
			t.Fatal(err)
		}
		if _, err = j.BeginSubmission(rec, "signer"); err != nil {
			t.Fatal(err)
		}
		rec.State, rec.ServerState, rec.OrderID = journal.StateSubmitted, "submitted", int64(i+1)
		h.fake.actions[rec.ActionID], h.fake.oids[rec.ActionID] = "submitted", fmt.Sprint(i+1)
		if err = j.Put(rec, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Mark the newest records unknown only after seeding, since an unknown
	// record gates further submissions.
	for i := total - unknowns; i < total; i++ {
		rec, err := j.Get(fmt.Sprintf("rec-%03d", i))
		if err != nil {
			t.Fatal(err)
		}
		rec.State, rec.ServerState, rec.OrderID = journal.StateUnknown, "", 0
		h.fake.actions[rec.ActionID] = "filled"
		if err = j.Put(rec, "timeout"); err != nil {
			t.Fatal(err)
		}
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	r := h.run("orders", "reconcile", "--json")
	if r.code != 0 {
		t.Fatalf("reconcile: %s", r.stdout)
	}
	if r.data()["still_unknown"] != float64(0) {
		t.Fatalf("unknown records starved: %v", r.data()["still_unknown"])
	}
	for i := total - unknowns; i < total; i++ {
		s := h.run("orders", "status", "--intent", fmt.Sprintf("rec-%03d", i), "--json")
		if s.data()["state"] != "submitted" {
			t.Fatalf("rec-%03d not reconciled: %s", i, s.stdout)
		}
	}
	if h.fake.maxReconcile == 0 || h.fake.maxReconcile > reconcileBatch {
		t.Fatalf("batch size %d exceeds %d", h.fake.maxReconcile, reconcileBatch)
	}
}

// An accepted order sent through another API origin (for example staging)
// must not make reconcile fail: it is settled from Hyperliquid's orderStatus,
// and an order Hyperliquid cannot settle is skipped and reported.
func TestReconcileOtherOriginUsesHyperliquid(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	p, err := (&env{app: NewApp("t")}).profile()
	if err != nil {
		t.Fatal(err)
	}
	// Records are seeded the way an earlier staging profile with the same
	// name would have written them.
	j, err := journal.OpenBinding(journal.Binding{Profile: p.Name, Network: p.Network, Account: strings.ToLower(p.Account), Origin: "https://staging.example"})
	if err != nil {
		t.Fatal(err)
	}
	seed := func(id string, oid int64) {
		in := trading.Intent{Version: 1, ID: id, Kind: trading.KindOrder, Network: p.Network, Account: p.Account, APIURL: "https://staging.example", CreatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339)}
		rec, _, err := j.Create(in)
		if err != nil {
			t.Fatal(err)
		}
		rec.State, rec.ActionID = journal.StateCreated, "act-"+id
		if err = j.Put(rec, ""); err != nil {
			t.Fatal(err)
		}
		if _, err = j.BeginSubmission(rec, "signer"); err != nil {
			t.Fatal(err)
		}
		rec.State, rec.ServerState, rec.OrderID = journal.StateSubmitted, "submitted", oid
		if err = j.Put(rec, ""); err != nil {
			t.Fatal(err)
		}
	}
	seed("other-filled", 11)
	seed("other-open", 12)
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	h.fake.hlOrders = map[int64]string{11: "filled", 12: "open"}
	r := h.run("orders", "reconcile", "--json")
	if r.code != 0 {
		t.Fatalf("reconcile failed on other-origin intents: %s", r.stdout)
	}
	if s := h.run("orders", "status", "--intent", "other-filled", "--json"); s.data()["server_state"] != "filled" {
		t.Fatalf("filled order not settled: %s", s.stdout)
	}
	skipped, _ := r.data()["skipped_other_api"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["intent_id"] != "other-open" {
		t.Fatalf("open order not reported as skipped: %s", r.stdout)
	}
}
