package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/lending"
)

// lendingVault is the seeded intent's target.
var lendingVault = common.HexToAddress("0x5555555555555555555555555555555555555555")

// seedSequentialLending journals a two-step earn intent handed off at step
// 0. With hash set the hash is recorded (state sent, step in flight, as when
// the process stops waiting for the receipt); without it the broadcast
// returned no hash (state unknown).
func seedSequentialLending(t *testing.T, h *harness, hash string) (*journal.Journal, string) {
	t.Helper()
	agent := h.fake.agentAddress
	vault := lendingVault
	plan, err := json.Marshal(lending.Plan{Protocol: protoMorpho, Action: "deposit", Chain: 42161, Owner: common.HexToAddress(agent), Vault: &vault, Expect: lending.Want{"vault:x:deposit": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	tpl := journal.OnchainTemplate{ChainID: 42161, To: vault.Hex(), Data: "0x01", Value: "0x0"}
	now := time.Now().UTC()
	in := journal.OnchainIntent{ID: "lending-seq", Kind: kindEarn, ChainID: 42161, Sender: agent, Recipient: agent, Token: vault.Hex(), Amount: "1", Templates: []journal.OnchainTemplate{tpl, tpl}, Lending: plan, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(lendingQuoteTTL).Format(time.RFC3339Nano)}
	j, err := openJournal(&resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.CreateOnchain(in); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return j.BeginOnchain(in.ID, in.Digest(), agent) },
		func() error { return j.SetExecutionMode(in.ID, "sequential") },
		func() error { return j.MarkStepHandoff(in.ID, 0) },
	} {
		if err = step(); err != nil {
			t.Fatal(err)
		}
	}
	next := "unknown"
	if hash != "" {
		next = "sent"
	}
	if err = j.AdvanceOnchain(in.ID, "sending", next, hash); err != nil {
		t.Fatal(err)
	}
	return j, in.ID
}

// receiptRPC answers receipt and transaction lookups for one hash sent by
// agent to the seeded vault; mined controls whether the receipt exists.
func receiptRPC(t *testing.T, agent, hash string, mined bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("rpc request: %v", err)
			return
		}
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0xa4b1"}`)
		case "eth_blockNumber":
			fmt.Fprint(w, `{"result":"0x100"}`)
		case "eth_getBlockByNumber":
			fmt.Fprintf(w, `{"result":{"hash":%q}}`, hash)
		case "eth_getTransactionByHash":
			fmt.Fprintf(w, `{"result":{"hash":%q,"from":%q,"to":%q,"input":"0x01","value":"0x0","chainId":"0xa4b1"}}`, hash, agent, lendingVault.Hex())
		case "eth_getTransactionReceipt":
			if !mined {
				fmt.Fprint(w, `{"result":null}`)
				return
			}
			fmt.Fprintf(w, `{"result":{"status":"0x1","transactionHash":%q,"blockHash":%q,"blockNumber":"0x10"}}`, hash, hash)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A confirmed intermediate step whose process died before the cursor moved
// must become resumable, not a permanent unknown that blocks the chain.
func TestLendingStatusResumesAfterIntermediateReceipt(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	hash := "0x" + strings.Repeat("ab", 32)
	j, id := seedSequentialLending(t, h, hash)
	h.rpcURL = receiptRPC(t, h.fake.agentAddress, hash, true)
	r := h.run("earn", "status", "--intent", id, "--json")
	if r.code != 0 || r.data()["state"] != "sending" {
		t.Fatalf("status: %s %s", r.stdout, r.stderr)
	}
	rec, err := j.GetOnchain(id)
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	if rec.StepIndex != 1 || rec.StepInFlight || len(rec.Hashes) != 1 {
		t.Fatalf("cursor not advanced past the confirmed step: %+v", rec)
	}
}

// A last-recorded hash that is not the cursor's step stays unresolved.
func TestSequentialStepConfirmedOnlyAtCursor(t *testing.T) {
	r := &journal.OnchainRecord{OnchainIntent: journal.OnchainIntent{Templates: make([]journal.OnchainTemplate, 3)}, Hashes: []string{"a"}, ExecutionMode: "sequential", StepIndex: 0}
	if !sequentialStepConfirmed(r) {
		t.Fatal("step 0 at cursor 0 should be resumable")
	}
	r.StepIndex = 1
	if sequentialStepConfirmed(r) {
		t.Fatal("hash behind the cursor treated as the cursor step")
	}
	r.StepIndex, r.Batch = 0, &journal.Batch{}
	if sequentialStepConfirmed(r) {
		t.Fatal("batch treated as sequential")
	}
}

// Reconcile attaches the recovered hash and reports through the lending
// status of the intent's own kind, not the send status.
func TestLendingReconcileReportsLendingStatus(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	// The broadcast returned no hash, leaving the intent unknown.
	j, id := seedSequentialLending(t, h, "")
	j.Close()
	hash := "0x" + strings.Repeat("cd", 32)
	h.rpcURL = receiptRPC(t, h.fake.agentAddress, hash, false)
	r := h.run("earn", "reconcile", "--intent", id, "--tx-hash", hash, "--json")
	if r.code != 0 || r.data()["kind"] != kindEarn || r.data()["plan"] == nil {
		t.Fatalf("lending reconcile: %s %s", r.stdout, r.stderr)
	}
	if hashes, _ := r.data()["tx_hashes"].([]any); len(hashes) != 1 || hashes[0] != hash {
		t.Fatalf("recovered hash not attached: %v", r.data())
	}
}

// A receipt checked against an older snapshot must not advance a cursor that
// submit has since moved, nor clear the newer step's in-flight marker.
func TestStaleReceiptDoesNotAdvanceNewerCursor(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h0 := "0x" + strings.Repeat("ab", 32)
	h1 := "0x" + strings.Repeat("cd", 32)
	j, id := seedSequentialLending(t, h, h0)
	defer j.Close()
	stale, err := j.GetOnchain(id)
	if err != nil {
		t.Fatal(err)
	}
	// Submit confirms h0 and hands off step 1 while the status call waits.
	for _, step := range []func() error{
		func() error { return j.AdvanceOnchain(id, "sent", "sending", "") },
		func() error { return j.MarkStepHandoff(id, 1) },
		func() error { return j.AdvanceOnchain(id, "sending", "sent", h1) },
	} {
		if err = step(); err != nil {
			t.Fatal(err)
		}
	}
	rpcURL := receiptRPC(t, h.fake.agentAddress, h0, true)
	a := &App{Getenv: func(key string) string {
		if strings.HasPrefix(key, "OKU_RPC_URL_") {
			return rpcURL
		}
		return ""
	}}
	e := &env{app: a, ctx: context.Background(), g: globals{timeout: time.Second}}
	got, err := a.resolveReceipt(e, j, stale)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "sent" || got.StepIndex != 1 || !got.StepInFlight || len(got.Hashes) != 2 {
		t.Fatalf("stale receipt moved the newer row: %+v", got)
	}
}

// seedStuckBatch journals the shape a reverted 7702 submit left behind: unknown,
// batch mode, nonce recorded, no hash. warrantValidBefore sets the router
// warrant inside the plan's swap (0 for a plan with no swap).
func seedStuckBatch(t *testing.T, h *harness, expires time.Time, warrantValidBefore int64) (*journal.Journal, string) {
	t.Helper()
	agent := h.fake.agentAddress
	owner := common.HexToAddress(agent)
	d, _ := lending.Morpho(1)
	id := loopMarket().ID()
	router, _ := lending.OkuRouter(1)
	plan := lending.Plan{Protocol: protoMorpho, Action: "loop-open-up", Chain: 1, Owner: owner, MarketID: &id, Tokens: []common.Address{loopColl, loopLoan}, Router: &router}
	var templates []journal.OnchainTemplate
	if warrantValidBefore > 0 {
		borrow := big.NewInt(1_000_000)
		trade, err := lending.DecodeRouterTrade(common.FromHex(routerFillValid(t, loopLoan, loopColl, d.GeneralAdapter1, borrow, warrantValidBefore)))
		if err != nil {
			t.Fatal(err)
		}
		mp := loopMarket()
		call, err := d.LoopIncrease(mp, nil, borrow, new(big.Int), lending.Swap{Router: router, Trade: trade, MinOut: big.NewInt(1)}, owner)
		if err != nil {
			t.Fatal(err)
		}
		templates = []journal.OnchainTemplate{{ChainID: 1, To: call.To.Hex(), Data: call.Hex(), Value: "0x0"}}
		plan.Expect = lending.Want{}.Borrow(id, borrow).Supply(id, big.NewInt(1)).SupplyRest(id).Sell(loopLoan, loopColl, borrow).Flash()
	} else {
		plan.Expect = lending.Want{"vault:x:deposit": "1"}
		templates = []journal.OnchainTemplate{{ChainID: 1, To: lendingVault.Hex(), Data: "0x01", Value: "0x0"}}
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	in := journal.OnchainIntent{ID: "stuck-batch", Kind: kindLoop, ChainID: 1, Sender: agent, Recipient: agent, Token: loopColl.Hex(), Amount: "1", Templates: templates, Lending: raw, CreatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), ExpiresAt: expires.UTC().Format(time.RFC3339Nano)}
	j, err := openJournal(&resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.CreateOnchain(in); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return j.BeginOnchain(in.ID, in.Digest(), agent) },
		func() error { return j.SetExecutionMode(in.ID, "batch") },
		func() error { return j.SetOnchainBatch(in.ID, "sending", journal.Batch{Nonce: 8, FromBlock: 100}) },
		func() error { return j.AdvanceOnchain(in.ID, "sending", "unknown", "") },
	} {
		if err = step(); err != nil {
			t.Fatal(err)
		}
	}
	return j, in.ID
}

// nonceRPC answers a Base-like chain id 1 RPC with fixed latest and pending
// nonces and no mined batch.
func nonceRPC(t *testing.T, latest, pending uint64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("rpc request: %v", err)
			return
		}
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"result":"0x1"}`)
		case "eth_blockNumber":
			fmt.Fprint(w, `{"result":"0x64"}`)
		case "eth_getTransactionCount":
			n := latest
			if req.Params[1] == "pending" {
				n = pending
			}
			fmt.Fprintf(w, `{"result":"0x%x"}`, n)
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func stuckStatus(t *testing.T, h *harness, j *journal.Journal, id string) string {
	t.Helper()
	h.run("loop", "status", "--intent", id, "--json")
	rec, err := j.GetOnchain(id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.State
}

// The exact stuck shape: unknown, batch, nonce 8 recorded, no hash, nonce
// still 8, intent expired and the swap warrant lapsed.
func TestLoopStatusClosesExpiredUnbroadcastBatch(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.rpcChain = "1"
	j, id := seedStuckBatch(t, h, time.Now().Add(-time.Hour), time.Now().Add(-30*time.Minute).Unix())
	defer j.Close()
	h.rpcURL = nonceRPC(t, 8, 8)
	if got := stuckStatus(t, h, j, id); got != "failed" {
		t.Fatalf("lapsed unbroadcast batch is %s, want failed", got)
	}
}

// Every unsafe variant stays unknown: not expired, warrant still valid, no
// swap warrant to bound a late broadcast, nonce moved or pending.
func TestLoopStatusKeepsBatchThatMayStillLand(t *testing.T) {
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name            string
		expires         time.Time
		warrant         int64
		latest, pending uint64
	}{
		{"not expired", future, past.Unix(), 8, 8},
		{"warrant still valid", past, future.Unix(), 8, 8},
		{"no swap warrant", past, 0, 8, 8},
		{"nonce used", past, past.Unix(), 9, 9},
		{"pending transaction", past, past.Unix(), 8, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			h.rpcChain = "1"
			j, id := seedStuckBatch(t, h, tc.expires, tc.warrant)
			defer j.Close()
			h.rpcURL = nonceRPC(t, tc.latest, tc.pending)
			if got := stuckStatus(t, h, j, id); got != "unknown" {
				t.Fatalf("batch that may still land became %s", got)
			}
		})
	}
}
