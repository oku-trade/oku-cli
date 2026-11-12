package cli

import (
	"testing"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

func TestChasePolicyManagementEntryPaths(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL); r.code != 0 {
		t.Fatal(r.stdout)
	}
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method == "info" && body["type"] == "l2Book" {
			return map[string]any{"coin": "BTC", "levels": []any{[]any{map[string]any{"px": "49999"}}, []any{map[string]any{"px": "50002"}}}}, true
		}
		return nil, false
	}
	prepared := h.run("orders", "chase", "--market", "BTC", "--side", "buy", "--size", "0.01", "--max-distance", "100", "--intent-id", "chase-management", "--json")
	if prepared.code != 0 {
		t.Fatal(prepared.stdout)
	}
	digest := prepared.data()["digest"].(string)
	r := h.run("orders", "submit", "--intent", "chase-management", "--digest", digest, "--json")
	if r.code == 0 || r.errCode() != "USAGE" {
		t.Fatal(r.stdout)
	}
	j, err := journal.Open("main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	})
	rec, err := j.Get("chase-management")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.CreateChase(rec); err != nil {
		t.Fatal(err)
	}
	r = h.run("orders", "status", "--intent", "chase-management", "--json")
	if r.code != 0 || r.data()["chase"].(map[string]any)["state"] != "ready" {
		t.Fatal(r.stdout)
	}
	r = h.run("orders", "abandon", "--intent", "chase-management", "--json")
	if r.code == 0 || r.errCode() != "INTENT_STATE" {
		t.Fatal(r.stdout)
	}
	r = h.run("orders", "reconcile", "--json")
	if r.code != 0 {
		t.Fatal(r.stdout)
	}
	c, err := j.GetChase("chase-management")
	if err != nil {
		t.Fatal(err)
	}
	if c.State != journal.ChaseStopped {
		t.Fatalf("unstarted approved chase was not stopped: %v", c.State)
	}
	r = h.run("orders", "abandon", "--intent", "chase-management", "--json")
	if r.code != 0 {
		t.Fatal(r.stdout)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if h.fake.creates != 0 || h.fake.confirms != 0 {
		t.Fatal("management of an unstarted policy sent an action")
	}
}
