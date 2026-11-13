package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

func TestHistoricalPositionModifyJournalRecovery(t *testing.T) {
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
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	})

	// This is the persisted shape produced by the whole-position modify build.
	const historicalPlan = `{"type":"modify","payload":{"modifies":[{"oid":42,"order":{"a":0,"b":false,"p":"44100","s":"0.01","r":true,"t":{"trigger":{"isMarket":true,"triggerPx":"49000","tpsl":"sl"}}}}],"size_decimals":5,"position_tpsl":true}}`
	var advanced trading.Advanced
	if err := json.Unmarshal([]byte(historicalPlan), &advanced); err != nil {
		t.Fatal(err)
	}
	var unsigned []*journal.Record
	for _, id := range []string{"legacy-completed", "legacy-prepared", "legacy-created", "unrelated-unknown"} {
		in := trading.Intent{Version: 1, ID: id, Kind: trading.KindModify, Network: p.Network, Account: p.Account, APIURL: p.APIURL, Market: "BTC", Advanced: &advanced,
			CreatedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		if id == "unrelated-unknown" {
			in.Kind, in.Advanced = trading.KindOrder, nil
		}
		rec, _, err := j.Create(in)
		if err != nil {
			t.Fatal(err)
		}
		if in.Advanced != nil {
			// Compute the historical digest independently of Advanced.MarshalJSON.
			fields, err := json.Marshal(struct {
				trading.Intent
				Advanced json.RawMessage `json:"advanced"`
			}{in, json.RawMessage(historicalPlan)})
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(fields)
			if rec.Digest != hex.EncodeToString(sum[:]) {
				t.Fatal("historical intent digest changed")
			}
		}
		if id == "legacy-prepared" {
			unsigned = append(unsigned, rec)
			continue
		}
		rec.State, rec.ActionID = journal.StateCreated, "act-"+id
		if err := j.Put(rec, ""); err != nil {
			t.Fatal(err)
		}
		if id == "legacy-created" {
			unsigned = append(unsigned, rec)
			continue
		}
		if _, err := j.BeginSubmission(rec, "signer"); err != nil {
			t.Fatal(err)
		}
		rec.State, rec.ServerState = journal.StateSubmitted, "filled"
		if id == "unrelated-unknown" {
			rec.State, rec.ServerState = journal.StateUnknown, ""
			h.fake.actions[rec.ActionID] = "filled"
		}
		if err := j.Put(rec, ""); err != nil {
			t.Fatal(err)
		}
	}

	for _, args := range [][]string{
		{"orders", "status", "--intent", "legacy-completed", "--json"},
		{"orders", "intents", "--json"},
		{"orders", "intents", "--unresolved", "--json"},
		{"orders", "reconcile", "--json"},
	} {
		if r := h.run(args...); r.code != 0 {
			t.Fatalf("%v: %s %s", args, r.stdout, r.stderr)
		}
	}
	if r := h.run("orders", "status", "--intent", "unrelated-unknown", "--json"); r.data()["server_state"] != "filled" {
		t.Fatalf("unrelated action not reconciled: %s", r.stdout)
	}
	for _, rec := range unsigned {
		r := h.run("orders", "submit", "--intent", rec.Intent.ID, "--digest", rec.Digest, "--json")
		if r.errCode() != "INVALID_ARGUMENT" || !strings.Contains(r.stdout, "whole-position TP/SL") || h.fake.creates != 0 || h.fake.lastSigned != nil {
			t.Fatalf("historical approval was not refused before signing: %s %s", r.stdout, r.stderr)
		}
		if r := h.run("orders", "abandon", "--intent", rec.Intent.ID, "--json"); r.code != 0 || r.data()["state"] != "abandoned" {
			t.Fatalf("cannot abandon historical approval: %s %s", r.stdout, r.stderr)
		}
	}
}
