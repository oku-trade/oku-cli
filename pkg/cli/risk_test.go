package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/journal"

	"github.com/oku-trade/oku-cli/pkg/signing"
)

// Synthetic Connect boundary for risk actions: the fake backend grants the
// risk scope and builds updateLeverage / updateIsolatedMargin payloads. No
// live exchange or deployed backend is involved.

func riskHarness(t *testing.T) *harness {
	h := newHarness(t)
	h.fake.scopes = []string{"read", "trade", "risk"}
	h.enroll()
	return h
}

func prepareRisk(t *testing.T, h *harness, args ...string) (string, string) {
	t.Helper()
	r := h.run(append(args, "--json")...)
	if r.code != 0 {
		t.Fatalf("prepare %v: %s %s", args, r.stdout, r.stderr)
	}
	return r.data()["intent_id"].(string), r.data()["digest"].(string)
}

func completeConfirm() (int, string) { return 200, `{"status":"completed","hlOid":"0"}` }

func assertSignedByAgent(t *testing.T, h *harness, wantAction string) {
	t.Helper()
	sa := h.fake.lastSigned["signedAction"].(map[string]any)
	if sa["actionJson"] != wantAction {
		t.Fatalf("signed %v, want %s", sa["actionJson"], wantAction)
	}
	var nonce, v uint64
	fmt.Sscan(sa["nonce"].(string), &nonce)
	conn, _ := signing.ActionHash(sa["actionJson"].(string), "", nonce)
	s := sa["signature"].(map[string]any)
	fmt.Sscan(s["v"].(string), &v)
	got, _ := signing.RecoverAddress(signing.AgentDigest(conn), signing.Signature{R: s["r"].(string), S: s["s"].(string), V: v})
	if !strings.EqualFold(got, h.fake.redeemed.Agent) {
		t.Fatalf("signed by %s, want agent %s", got, h.fake.redeemed.Agent)
	}
}

func TestRiskLeverageCompletedWithZeroOID(t *testing.T) {
	h := riskHarness(t)
	h.fake.confirm = completeConfirm
	id, dg := prepareRisk(t, h, "positions", "leverage", "--market", "BTC", "--leverage", "7", "--mode", "isolated")
	if h.fake.creates != 0 {
		t.Fatal("prepare contacted CreateAction")
	}
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.code != 0 || r.data()["state"] != "submitted" {
		t.Fatalf("submit: %s %s", r.stdout, r.stderr)
	}
	assertSignedByAgent(t, h, `{"type":"updateLeverage","asset":0,"isCross":false,"leverage":7}`)
	if r = h.run("orders", "submit", "--intent", id, "--digest", dg, "--json"); h.fake.confirms != 1 {
		t.Fatalf("resent: %s", r.stdout)
	}
}

func TestRiskMarginCompletedExactUnits(t *testing.T) {
	h := riskHarness(t)
	h.fake.confirm = completeConfirm
	id, dg := prepareRisk(t, h, "positions", "margin", "--market", "BTC", "--amount", "-12.345678")
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.code != 0 || r.data()["state"] != "submitted" {
		t.Fatalf("submit: %s", r.stdout)
	}
	assertSignedByAgent(t, h, `{"type":"updateIsolatedMargin","asset":0,"isBuy":true,"ntli":-12345678}`)
}

// After a signed ConfirmAction, anything but "completed" is unknown for
// risk actions, including "error" and an OID-less "submitted". Unknown
// blocks the next exposure and is never resent.
func TestRiskNonCompletedAfterSendStaysUnknown(t *testing.T) {
	for _, status := range []string{"submitted", "unresolved", "error", "pending"} {
		t.Run(status, func(t *testing.T) {
			h := riskHarness(t)
			h.fake.confirm = func() (int, string) { return 200, `{"status":"` + status + `","hlOid":"0"}` }
			id, dg := prepareRisk(t, h, "positions", "leverage", "--market", "BTC", "--leverage", "3", "--mode", "cross")
			r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
			if r.errCode() != "OUTCOME_UNKNOWN" || h.fake.confirms != 1 {
				t.Fatalf("got %d %s", r.code, r.stdout)
			}
			if r = h.run("orders", "submit", "--intent", id, "--digest", dg, "--json"); r.errCode() != "OUTCOME_UNKNOWN" || h.fake.confirms != 1 {
				t.Fatalf("resend: %s", r.stdout)
			}
			id2, dg2 := prepareRisk(t, h, "positions", "margin", "--market", "BTC", "--amount", "1")
			if r = h.run("orders", "submit", "--intent", id2, "--digest", dg2, "--json"); r.errCode() != "OUTCOME_UNKNOWN" || h.fake.creates != 1 {
				t.Fatalf("new risk while unknown: %s creates=%d", r.stdout, h.fake.creates)
			}
			// Reconciling with the same non-completed state keeps it unknown.
			h.fake.actions["act1"] = status
			h.run("orders", "reconcile", "--json")
			if r = h.run("orders", "status", "--intent", id, "--json"); r.data()["state"] != "unknown" {
				t.Fatalf("after reconcile %s: %s", status, r.stdout)
			}
			// Venue proof of completion resolves it.
			h.fake.actions["act1"] = "completed"
			h.run("orders", "reconcile", "--json")
			if r = h.run("orders", "status", "--intent", id, "--json"); r.data()["state"] != "submitted" {
				t.Fatalf("after completed: %s", r.stdout)
			}
		})
	}
}

func TestRiskWrongPayloadRefusedBeforeSign(t *testing.T) {
	cases := map[string][]string{
		`{"type":"updateLeverage","asset":0,"isCross":false,"leverage":8}`:       {"positions", "leverage", "--market", "BTC", "--leverage", "7", "--mode", "isolated"},
		`{"type":"updateLeverage","asset":0,"isCross":true,"leverage":7}`:        {"positions", "leverage", "--market", "BTC", "--leverage", "7", "--mode", "isolated"},
		`{"type":"updateLeverage","asset":1,"isCross":false,"leverage":7}`:       {"positions", "leverage", "--market", "BTC", "--leverage", "7", "--mode", "isolated"},
		`{"type":"updateIsolatedMargin","asset":0,"isBuy":true,"ntli":2000000}`:  {"positions", "margin", "--market", "BTC", "--amount", "1"},
		`{"type":"updateIsolatedMargin","asset":0,"isBuy":true,"ntli":-1000000}`: {"positions", "margin", "--market", "BTC", "--amount", "1"},
		`{"type":"updateLeverage","asset":0,"isCross":false,"leverage":7,"x":1}`: {"positions", "leverage", "--market", "BTC", "--leverage", "7", "--mode", "isolated"},
		`{"type":"order","orders":[],"grouping":"na"}`:                           {"positions", "margin", "--market", "BTC", "--amount", "1"},
	}
	for action, args := range cases {
		h := riskHarness(t)
		h.fake.riskAction = func(string, map[string]any) string { return action }
		id, dg := prepareRisk(t, h, args...)
		r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
		if r.errCode() != "PAYLOAD_MISMATCH" || h.fake.confirms != 0 {
			t.Errorf("%s: %s confirms=%d", action, r.stdout, h.fake.confirms)
		}
		if h.fake.actions["act1"] != "cancelled" {
			t.Errorf("%s: unsigned mismatched action not cancelled", action)
		}
	}
}

func TestRiskErrorBeforeSendIsRejected(t *testing.T) {
	h := riskHarness(t)
	h.fake.confirm = func() (int, string) { return 400, `{"code":"failed_precondition","message":"no isolated position"}` }
	id, dg := prepareRisk(t, h, "positions", "margin", "--market", "BTC", "--amount", "1")
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "REJECTED" {
		t.Fatalf("definite reject: %s", r.stdout)
	}
}

// A created (unsigned) risk action is cleaned up with the risk scope; a
// device without it still abandons locally and reports the skipped cleanup.
func TestRiskAbandonUsesRiskScope(t *testing.T) {
	for _, tc := range []struct {
		scopes  []string
		cleaned bool
	}{{[]string{"read", "risk"}, true}, {[]string{"read", "trade"}, false}} {
		h := newHarness(t)
		h.fake.scopes = tc.scopes
		h.enroll()
		id, _ := prepareRisk(t, h, "positions", "leverage", "--market", "BTC", "--leverage", "7", "--mode", "isolated")
		// Put the intent in "created": the server holds an unsigned action.
		j, err := journal.Open("main")
		if err != nil {
			t.Fatal(err)
		}
		rec, _ := j.Get(id)
		rec.State, rec.ActionID = journal.StateCreated, "act9"
		rec.ActionJSON = `{"type":"updateLeverage","asset":0,"isCross":false,"leverage":7}`
		if err := j.Put(rec, "test"); err != nil {
			t.Fatal(err)
		}
		j.Close()
		h.fake.actions["act9"] = "pending"
		r := h.run("orders", "abandon", "--intent", id, "--json")
		if r.code != 0 || r.data()["state"] != "abandoned" || h.fake.confirms != 0 {
			t.Fatalf("abandon: %s", r.stdout)
		}
		cleanup, _ := r.data()["server_cleanup"].(string)
		if tc.cleaned != (h.fake.actions["act9"] == "cancelled") || tc.cleaned != (cleanup == "requested") {
			t.Fatalf("scopes %v: cleanup %q server %q", tc.scopes, cleanup, h.fake.actions["act9"])
		}
		if !tc.cleaned && !strings.Contains(cleanup, "risk") {
			t.Fatalf("skipped cleanup not explained: %q", cleanup)
		}
	}
}
