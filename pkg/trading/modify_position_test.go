package trading

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPositionTpslModifyRejected(t *testing.T) {
	plan := ModifyPlan{PositionTpsl: true, SizeDecimals: 2, Modifies: []ApprovedModify{{OrderID: 8, Order: ApprovedOrder{
		AssetID: 3, Price: "81", Size: "2", ReduceOnly: true, Execution: mustTrigger(t, true, "90", "sl"),
	}}}}
	if _, err := NewAdvanced(plan); !errors.Is(err, ErrPositionTpslModify) {
		t.Fatalf("accepted unsupported whole-position modify: %v", err)
	}
	raw, err := json.Marshal(struct {
		Type    string     `json:"type"`
		Payload ModifyPlan `json:"payload"`
	}{KindModify, plan})
	if err != nil {
		t.Fatal(err)
	}
	var restored Advanced
	if err := json.Unmarshal(raw, &restored); !errors.Is(err, ErrPositionTpslModify) {
		t.Fatalf("restored unsupported persisted approval: %v", err)
	}
	in := &Intent{Kind: KindModify, Advanced: &Advanced{payload: plan}}
	wire := `{"type":"batchModify","modifies":[{"oid":8,"order":{"a":3,"b":false,"p":"81","s":"2","r":true,"t":{"trigger":{"isMarket":true,"triggerPx":"90","tpsl":"sl"}}}}]}`
	if _, err := ValidateAction(in, wire); err == nil || !strings.Contains(err.Error(), "whole-position TP/SL") {
		t.Fatalf("accepted wire that cannot express whole-position protection: %v", err)
	}
}
