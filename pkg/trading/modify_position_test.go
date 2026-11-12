package trading

import (
	"strings"
	"testing"
)

func TestPositionTpslModifyContract(t *testing.T) {
	plan := ModifyPlan{PositionTpsl: true, SizeDecimals: 2, Modifies: []ApprovedModify{{OrderID: 8, Order: ApprovedOrder{
		AssetID: 3, Price: "81", Size: "2", ReduceOnly: true, Execution: mustTrigger(t, true, "90", "sl"),
	}}}}
	advanced, err := NewAdvanced(plan)
	if err != nil {
		t.Fatal(err)
	}
	in := &Intent{Kind: KindModify, Advanced: advanced}
	// account buildModifyAction emits no grouping or unconditional-place flag.
	// The existing oid keeps positionTpsl grouping, even with positive wire size.
	wire := `{"type":"batchModify","modifies":[{"oid":8,"order":{"a":3,"b":false,"p":"81","s":"2","r":true,"t":{"trigger":{"isMarket":true,"triggerPx":"90","tpsl":"sl"}}}}]}`
	if _, err := ValidateAction(in, wire); err != nil {
		t.Fatalf("backend position TP/SL fixture rejected: %v", err)
	}
	for _, change := range [][2]string{
		{`"s":"2"`, `"s":"1"`},
		{`"oid":8`, `"oid":9`},
		{`"triggerPx":"90"`, `"triggerPx":"91"`},
		{`"tpsl":"sl"`, `"tpsl":"tp"`},
		{`"type":"batchModify"`, `"type":"batchModify","grouping":"positionTpsl"`},
		{`"type":"batchModify"`, `"type":"batchModify","a":true`},
	} {
		if _, err := ValidateAction(in, strings.Replace(wire, change[0], change[1], 1)); err == nil {
			t.Errorf("accepted mismatch %s", change[1])
		}
	}
	digest := in.Digest()
	plan.PositionTpsl = false
	in.Advanced, err = NewAdvanced(plan)
	if err != nil {
		t.Fatal(err)
	}
	if in.Digest() == digest {
		t.Fatal("whole-position semantics not bound into digest")
	}
}
