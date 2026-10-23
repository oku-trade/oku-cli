package cli

import "testing"

// orders intents lists the journal locally, and --unresolved returns exactly
// what doctor counts.
func TestOrdersIntentsListsJournal(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	id, _ := prepare(t, h)
	r := h.run("orders", "intents", "--json")
	if r.code != 0 {
		t.Fatalf("intents: %s %s", r.stdout, r.stderr)
	}
	list, _ := r.data()["intents"].([]any)
	if len(list) != 1 {
		t.Fatalf("want 1 intent: %s", r.stdout)
	}
	row := list[0].(map[string]any)
	if row["intent_id"] != id || row["state"] != "prepared" || row["unresolved"] != false || row["current_api"] != true {
		t.Fatalf("row: %v", row)
	}
	if r := h.run("orders", "intents", "--unresolved", "--json"); len(r.data()["intents"].([]any)) != 0 {
		t.Fatalf("prepared intent reported unresolved: %s", r.stdout)
	}
}
