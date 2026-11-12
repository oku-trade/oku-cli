package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

type chaseFixture struct {
	app     *App
	out     bytes.Buffer
	now     time.Time
	bid     string
	status  string
	live    string
	fills   []map[string]any
	fake    *fakeOku
	profile *resolved
}

// This fixture creates no signer or login session. Any accidental execution
// reaches the unsigned authentication boundary and fails locally.
func newChaseFixture(t *testing.T) *chaseFixture {
	t.Helper()
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("OKU_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	f := &chaseFixture{now: time.Now().UTC().Truncate(time.Second), bid: "50000", status: "open", live: "0.01", fills: []map[string]any{}}
	fake, backend := newFake(t)
	f.fake = fake
	info := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture request", 400)
			return
		}
		var response any
		switch req["type"] {
		case "l2Book":
			response = map[string]any{"coin": "BTC", "levels": [][]map[string]string{{{"px": f.bid}}, {{"px": "50010"}}}}
		case "orderStatus":
			response = map[string]any{"status": "order", "order": map[string]any{"status": f.status, "order": map[string]any{"coin": "BTC", "side": "B", "oid": int64(req["oid"].(float64)), "sz": f.live, "limitPx": "50001"}}}
		case "userFillsByTime":
			response = f.fills
		default:
			t.Errorf("unexpected public read %v", req["type"])
			http.Error(w, "unexpected read", 400)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(info.Close)
	f.app = &App{Stdout: &f.out, Stderr: io.Discard, Stdin: strings.NewReader(""), Now: func() time.Time { return f.now }, Sleep: sleepCtx, Getenv: func(k string) string {
		if k == "OKU_HYPERLIQUID_INFO_URL" {
			return info.URL
		}
		return ""
	}, Version: "test"}
	f.app.wrapHandlers = func(h map[string]*handler) {
		h["orders chase"] = &handler{flags: chaseFlags, run: f.app.cmdOrdersChase, stream: true, doneEvent: "done"}
	}
	if code := f.app.Run([]string{"profiles", "add", "main", "--account", acct, "--api-url", backend.URL}); code != 0 {
		t.Fatalf("profile: %s", f.out.String())
	}
	f.profile = &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: backend.URL}
	return f
}

func (f *chaseFixture) run(args ...string) run {
	f.out.Reset()
	code := f.app.Run(append([]string{"orders", "chase", "--json"}, args...))
	r := run{code: code, stdout: f.out.String()}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) > 0 {
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &r.env); err != nil {
			r.env = map[string]any{"fixture_decode_error": err.Error()}
		}
	}
	return r
}

func (f *chaseFixture) prepare(t *testing.T) (*journal.Journal, *journal.Record) {
	t.Helper()
	r := f.run("--market", "BTC", "--side", "buy", "--size", "0.01", "--max-distance", "10")
	if r.code != 0 {
		t.Fatal(r.stdout)
	}
	j, err := openJournal(f.profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	})
	rec, err := j.Get(r.data()["intent_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return j, rec
}

func TestChaseCommandApprovalAndResume(t *testing.T) {
	f := newChaseFixture(t)
	j, parent := f.prepare(t)
	if _, err := j.GetChase(parent.Intent.ID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("prepare activated engine: %v", err)
	}
	if r := f.run("--intent", parent.Intent.ID, "--digest", "wrong"); r.code == 0 || r.errCode() != string(output.CodePayloadMismatch) {
		t.Fatalf("digest accepted: %s", r.stdout)
	}
	// Expiry stops an approved policy without ever unlocking or placing.
	f.now = f.now.Add(10 * time.Minute)
	r := f.run("--intent", parent.Intent.ID, "--digest", parent.Digest)
	if r.code != 0 || r.data()["state"] != "stopped" || r.data()["reason"] != "expired" {
		t.Fatalf("expiry: %s", r.stdout)
	}
	r = f.run("--resume", parent.Intent.ID)
	if r.code != 0 || r.data()["state"] != "stopped" {
		t.Fatalf("resume terminal: %s", r.stdout)
	}
	if f.fake.creates != 0 || f.fake.confirms != 0 {
		t.Fatal("fixture unexpectedly executed an action")
	}
}

func TestChaseResumeAdoptsDurableOrderAndAuthoritativeFills(t *testing.T) {
	f := newChaseFixture(t)
	j, parent := f.prepare(t)
	c, err := j.CreateChase(parent)
	if err != nil {
		t.Fatal(err)
	}
	c.OrderID = 42
	c.OrderIDs = []int64{41, 42}
	c.State = journal.ChaseResting
	if err = j.PutChase(c); err != nil {
		t.Fatal(err)
	}
	f.status = "filled"
	f.live = "0"
	f.fills = []map[string]any{{"oid": 41, "tid": 1, "sz": "0.004", "px": "50000", "coin": "BTC", "side": "B"}, {"oid": 42, "tid": 2, "sz": "0.006", "px": "50010", "coin": "BTC", "side": "B"}, {"oid": 999, "tid": 3, "sz": "10", "px": "1", "coin": "BTC", "side": "B"}}
	r := f.run("--resume", parent.Intent.ID)
	if r.code != 0 || r.data()["reason"] != "filled" || r.data()["filled_size"] != "0.01" || r.data()["average_price"] != "50006" {
		t.Fatalf("adoption: %s", r.stdout)
	}
	if f.fake.creates != 0 || f.fake.confirms != 0 {
		t.Fatal("terminal recovery executed an action")
	}
}

func TestChaseFreshRemainingAndPendingSigningFailClosed(t *testing.T) {
	f := newChaseFixture(t)
	j, parent := f.prepare(t)
	c, err := j.CreateChase(parent)
	if err != nil {
		t.Fatal(err)
	}
	c.OrderID = 42
	c.OrderIDs = []int64{42}
	c.State = journal.ChaseResting
	if err = j.PutChase(c); err != nil {
		t.Fatal(err)
	}
	e := &env{app: f.app, ctx: context.Background(), g: globals{timeout: time.Second}}
	runner := chaseRunner{e: e, p: f.profile, j: j, parent: parent, c: c}
	execution, err := trading.NewLimitExecution("ALO")
	if err != nil {
		t.Fatal(err)
	}
	child, err := runner.child(trading.ModifyPlan{Modifies: []trading.ApprovedModify{{OrderID: 42, Order: trading.ApprovedOrder{AssetID: parent.Intent.AssetID, IsBuy: true, Price: "50002", Size: "0.01", Execution: execution}}}, SizeDecimals: parent.Intent.Chase.SizeDecimals}, false)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := j.CreateChaseChild(c, child)
	if err != nil {
		t.Fatal(err)
	}
	f.live = "0.006"
	f.fills = []map[string]any{{"oid": 42, "tid": 1, "sz": "0.004", "px": "50001", "coin": "BTC", "side": "B"}}
	if err = e.validateChaseChild(j, rec); err == nil {
		t.Fatal("stale replacement could restore a partial fill")
	}
	// A persisted signing state is not interpreted as rejected or filled.
	rec.State = journal.StateSigning
	if err = j.Put(rec, "fixture interrupted signing"); err != nil {
		t.Fatal(err)
	}
	r := f.run("--resume", parent.Intent.ID)
	if r.code == 0 {
		t.Fatal("pending signing resumed unsafely")
	}
	recovered, err := j.GetChase(c.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != journal.ChaseUnknown || recovered.PendingIntent != rec.Intent.ID || recovered.OrderID != 42 {
		t.Fatalf("lost recovery data: %+v", recovered)
	}
	if f.fake.creates != 0 || f.fake.confirms != 0 {
		t.Fatal("ambiguous recovery executed an action")
	}
}

type chaseBrokenWriter struct{}

func (chaseBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestChaseReadAndProgressErrors(t *testing.T) {
	f := newChaseFixture(t)
	e := &env{app: f.app, ctx: context.Background(), stream: true, g: globals{timeout: time.Second}}
	f.app.Stdout = chaseBrokenWriter{}
	if err := e.chaseEvent(&journal.ChaseRecord{IntentID: "chase-test"}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost progress error: %v", err)
	}
	f.app.Getenv = func(string) string { return "http://example.com/info" }
	if _, _, err := e.chaseBook(context.Background(), "BTC"); err == nil {
		t.Fatal("unsafe endpoint accepted")
	}
}

func TestChaseCleanupFailureKeepsOwnedOrderRecoverable(t *testing.T) {
	f := newChaseFixture(t)
	j, parent := f.prepare(t)
	c, err := j.CreateChase(parent)
	if err != nil {
		t.Fatal(err)
	}
	c.OrderID, c.OrderIDs, c.State = 42, []int64{42}, journal.ChaseResting
	if err = j.PutChase(c); err != nil {
		t.Fatal(err)
	}
	f.app.Sleep = func(context.Context, time.Duration) error { return io.ErrUnexpectedEOF }
	r := f.run("--resume", parent.Intent.ID)
	if r.code == 0 {
		t.Fatal("unsigned cancellation without credentials reported success")
	}
	recovered, err := j.GetChase(c.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State == journal.ChaseStopped || recovered.OrderID != 42 || recovered.PendingIntent != "" {
		t.Fatalf("lost recoverable resting order: %+v", recovered)
	}
	records, err := j.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range records {
		if rec.Intent.Kind != trading.KindCancelMany {
			continue
		}
		found = true
		plan := rec.Intent.Advanced.Payload().(trading.CancelManyPlan)
		if len(plan.Cancels) != 1 || plan.Cancels[0].OrderID != 42 || rec.State != journal.StateAbandoned {
			t.Fatalf("unsafe cancellation recovery: %+v", rec)
		}
	}
	if !found || f.fake.creates != 0 || f.fake.confirms != 0 {
		t.Fatal("cancellation did not stop at the real unsigned credential boundary")
	}
}
