package trading

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func mustLimit(t *testing.T, tif string) Execution {
	t.Helper()
	e, err := NewLimitExecution(tif)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func mustTrigger(t *testing.T, market bool, px, tpsl string) Execution {
	t.Helper()
	e, err := NewTriggerExecution(market, px, tpsl)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func mustPercent(t *testing.T, pct string) Retracement {
	t.Helper()
	r, err := NewPercentRetracement(pct)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func mustDistance(t *testing.T, px string) Retracement {
	t.Helper()
	r, err := NewPriceRetracement(px)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAdvancedActionContracts(t *testing.T) {
	parent := ApprovedOrder{AssetID: 3, IsBuy: true, Price: "100", Size: "2", Execution: mustLimit(t, "GTC")}
	tp := ApprovedOrder{AssetID: 3, IsBuy: false, Price: "108.9", Size: "1", ReduceOnly: true, Execution: mustTrigger(t, true, "110", "tp")}
	sl := ApprovedOrder{AssetID: 3, IsBuy: false, Price: "89", Size: "2", ReduceOnly: true, Execution: mustTrigger(t, false, "90", "sl")}
	position := sl
	position.Size = "0"
	modified := sl
	modified.Price = "81"
	modified.Execution = mustTrigger(t, true, "90", "sl")
	scale, err := ScaleOrders(3, true, "6", "90", "100", "1", 3, 2, "GTC", false)
	if err != nil {
		t.Fatal(err)
	}
	stop := "80"
	activation := "120"
	cases := []struct {
		name string
		plan AdvancedPayload
		wire string
	}{
		{"entry TP and SL", OrderPlan{Grouping: "normalTpsl", Orders: []ApprovedOrder{parent, tp, sl}, SlippageBps: 100, SizeDecimals: 2}, `{"type":"order","orders":[{"a":3,"b":true,"p":"100","s":"2","r":false,"t":{"limit":{"tif":"Gtc"}}},{"a":3,"b":false,"p":"108.9","s":"1","r":true,"t":{"trigger":{"isMarket":true,"triggerPx":"110","tpsl":"tp"}}},{"a":3,"b":false,"p":"89","s":"2","r":true,"t":{"trigger":{"isMarket":false,"triggerPx":"90","tpsl":"sl"}}}],"grouping":"normalTpsl","builder":{"b":"0xd637f2a36c1a3b37d57ef4c7022cb183d8922f2c","f":10}}`},
		{"position stop", OrderPlan{Grouping: "positionTpsl", Orders: []ApprovedOrder{position}, ClosePosition: true, SizeDecimals: 2}, `{"type":"order","orders":[{"a":3,"b":false,"p":"89","s":"0","r":true,"t":{"trigger":{"isMarket":false,"triggerPx":"90","tpsl":"sl"}}}],"grouping":"positionTpsl"}`},
		{"atomic modify", ModifyPlan{Modifies: []ApprovedModify{{OrderID: 7, Order: parent}, {OrderID: 8, Order: modified}}, SizeDecimals: 2}, `{"type":"batchModify","modifies":[{"oid":7,"order":{"a":3,"b":true,"p":"100","s":"2","r":false,"t":{"limit":{"tif":"Gtc"}}}},{"oid":8,"order":{"a":3,"b":false,"p":"81","s":"2","r":true,"t":{"trigger":{"isMarket":true,"triggerPx":"90","tpsl":"sl"}}}}]}`},
		{"unconditional modify", ModifyPlan{Modifies: []ApprovedModify{{OrderID: 8, Order: modified}}, AlwaysPlace: true, SizeDecimals: 2}, `{"type":"batchModify","modifies":[{"oid":8,"order":{"a":3,"b":false,"p":"81","s":"2","r":true,"t":{"trigger":{"isMarket":true,"triggerPx":"90","tpsl":"sl"}}}}],"a":true}`},
		{"TWAP", TWAPPlan{AssetID: 3, IsBuy: true, Size: "2", Minutes: 60}, `{"type":"twapOrder","twap":{"a":3,"b":true,"s":"2","r":false,"m":60,"t":false}}`},
		{"conditional TWAP", TWAPPlan{AssetID: 3, IsBuy: true, Size: "2", Minutes: 60, Randomize: true, Conditions: &TWAPConditions{Trigger: &TWAPTrigger{Price: "110", ActivateAbove: true}, StopPrice: &stop}}, `{"type":"twapOrder","twap":{"a":3,"b":true,"s":"2","r":false,"m":60,"t":true},"details":{"t":{"p":"110","a":true},"s":"80"}}`},
		{"trigger-only TWAP", TWAPPlan{AssetID: 3, IsBuy: false, Size: "2", Minutes: 5, Conditions: &TWAPConditions{Trigger: &TWAPTrigger{Price: "90"}}}, `{"type":"twapOrder","twap":{"a":3,"b":false,"s":"2","r":false,"m":5,"t":false},"details":{"t":{"p":"90","a":false},"s":null}}`},
		{"stop-only TWAP", TWAPPlan{AssetID: 3, IsBuy: false, Size: "2", Minutes: 10080, Conditions: &TWAPConditions{StopPrice: &stop}}, `{"type":"twapOrder","twap":{"a":3,"b":false,"s":"2","r":false,"m":10080,"t":false},"details":{"t":null,"s":"80"}}`},
		{"cancel TWAP", CancelTWAPPlan{AssetID: 3, TWAPID: 9}, `{"type":"twapCancel","a":3,"t":9}`},
		{"scale", ScalePlan{Orders: scale, StartPx: "90", EndPx: "100", TotalOrders: 3, SizeSkew: "1", TIF: "GTC", SizeDecimals: 2}, `{"type":"order","orders":[{"a":3,"b":true,"p":"90","s":"2","r":false,"t":{"limit":{"tif":"Gtc"}}},{"a":3,"b":true,"p":"95","s":"2","r":false,"t":{"limit":{"tif":"Gtc"}}},{"a":3,"b":true,"p":"100","s":"2","r":false,"t":{"limit":{"tif":"Gtc"}}}],"grouping":"na"}`},
		{"trailing percent", TrailingStopPlan{AssetID: 3, IsBuy: false, Size: "2", ReduceOnly: true, Retracement: mustPercent(t, "2.5")}, `{"type":"trailingStop","asset":3,"isBuy":false,"sz":"2","reduceOnly":true,"retracement":{"pct":"2.5000%"},"activationPx":null}`},
		{"trailing distance", TrailingStopPlan{AssetID: 3, IsBuy: false, Size: "2", ReduceOnly: true, Retracement: mustDistance(t, "5"), ActivationPrice: &activation}, `{"type":"trailingStop","asset":3,"isBuy":false,"sz":"2","reduceOnly":true,"retracement":{"px":"5"},"activationPx":"120"}`},
		{"cancel many", CancelManyPlan{Cancels: []CancelTarget{{AssetID: 3, OrderID: 7}, {AssetID: 100001, OrderID: 8}}}, `{"type":"cancel","cancels":[{"a":3,"o":7},{"a":100001,"o":8}]}`},
	}
	for _, market := range []bool{false, true} {
		for _, tpsl := range []string{"tp", "sl"} {
			o := sl
			o.ReduceOnly = false
			o.Execution = mustTrigger(t, market, "90", tpsl)
			if market {
				o.Price = "89.1"
			}
			name := fmt.Sprintf("trigger market=%v %s", market, tpsl)
			wire := fmt.Sprintf(`{"type":"order","orders":[{"a":3,"b":false,"p":%q,"s":"2","r":false,"t":{"trigger":{"isMarket":%v,"triggerPx":"90","tpsl":%q}}}],"grouping":"na"}`, o.Price, market, tpsl)
			cases = append(cases, struct {
				name string
				plan AdvancedPayload
				wire string
			}{name, OrderPlan{Grouping: "na", Orders: []ApprovedOrder{o}, SlippageBps: 100, SizeDecimals: 2}, wire})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			advanced, err := NewAdvanced(tc.plan)
			if err != nil {
				t.Fatal(err)
			}
			in := &Intent{Kind: advanced.Kind(), Advanced: advanced, BuilderAddress: builder, MaxBuilderFee10: 10}
			if _, err := ValidateAction(in, tc.wire); err != nil {
				t.Fatalf("backend fixture rejected: %v", err)
			}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			var restored Intent
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if in.Digest() != restored.Digest() || !reflect.DeepEqual(in.Advanced, restored.Advanced) {
				t.Fatal("persistence changed approved plan or digest")
			}
			if _, err := ValidateAction(&restored, tc.wire); err != nil {
				t.Fatal(err)
			}
			object, err := actionObject([]byte(tc.wire))
			if err != nil {
				t.Fatal(err)
			}
			mutateSignedFields(t, in, object, object, "$")
			in.Kind = KindMargin
			if _, err := ValidateAction(in, tc.wire); err == nil {
				t.Fatal("accepted incompatible payload kind")
			}
		})
	}
}

// Exercise every signed leaf through the real boundary, including false/zero
// values, and remove every required key. Optional builder omission is allowed.
func mutateSignedFields(t *testing.T, in *Intent, root map[string]any, value any, path string) {
	t.Helper()
	reject := func(name string) {
		t.Helper()
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ValidateAction(in, string(raw))
		var mismatch *Mismatch
		if !errors.As(err, &mismatch) {
			t.Errorf("%s accepted mutation: %s (err=%v)", name, raw, err)
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for key, old := range v {
			location := path + "." + key
			if key != "builder" && key != "c" {
				delete(v, key)
				reject(location + " omitted")
				v[key] = old
			}
			switch leaf := old.(type) {
			case string:
				v[key] = leaf + "x"
				reject(location)
				v[key] = old
			case bool:
				v[key] = !leaf
				reject(location)
				v[key] = old
			case json.Number:
				n, err := leaf.Int64()
				if err != nil {
					t.Fatal(err)
				}
				v[key] = n + 1
				reject(location)
				v[key] = old
			case nil:
				v[key] = "1"
				reject(location)
				v[key] = old
			default:
				mutateSignedFields(t, in, root, old, location)
			}
			if key != "builder" && key != "c" {
				if old != nil {
					v[key] = nil
					reject(location + " null")
					v[key] = old
				}
				delete(v, key)
				v[strings.ToUpper(key)] = old
				if strings.ToUpper(key) != key {
					reject(location + " case alias")
				}
				delete(v, strings.ToUpper(key))
				v[key] = old
			}
		}
	case []any:
		for i, child := range v {
			mutateSignedFields(t, in, root, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

func TestAdvancedRejectsIllegalPlans(t *testing.T) {
	limit := ApprovedOrder{AssetID: 0, IsBuy: true, Price: "100", Size: "1", Execution: mustLimit(t, "GTC")}
	trigger := limit
	trigger.Execution = mustTrigger(t, true, "100", "sl")
	trigger.Price = "101"
	for _, p := range []AdvancedPayload{
		nil, OrderPlan{}, OrderPlan{Grouping: "na", Orders: []ApprovedOrder{limit, limit}},
		OrderPlan{Grouping: "positionTpsl", Orders: []ApprovedOrder{trigger}},
		OrderPlan{Grouping: "na", Orders: []ApprovedOrder{trigger}, SizeDecimals: 2},
		OrderPlan{Grouping: "normalTpsl", Orders: []ApprovedOrder{limit, trigger}, SizeDecimals: 2},
		ModifyPlan{}, ModifyPlan{Modifies: []ApprovedModify{{OrderID: 1, Order: limit}, {OrderID: 1, Order: limit}}},
		TWAPPlan{Size: "1", Minutes: 4}, TWAPPlan{Size: "1", Minutes: 10081}, TWAPPlan{Size: "1", Minutes: 5, Conditions: &TWAPConditions{}},
		CancelTWAPPlan{}, ScalePlan{Orders: []ApprovedOrder{limit, limit}, TotalOrders: 2, SizeDecimals: -1},
		TrailingStopPlan{Size: "1"}, CancelManyPlan{}, CancelManyPlan{Cancels: []CancelTarget{{OrderID: 1}, {OrderID: 1}}},
	} {
		if _, err := NewAdvanced(p); err == nil {
			t.Errorf("accepted invalid %T: %+v", p, p)
		}
	}
	for _, raw := range []string{
		`{"limit":{"tif":"Gtc"},"trigger":{"isMarket":false,"triggerPx":"1","tpsl":"sl"}}`,
		`{"trigger":{"isMarket":true,"triggerPx":"1","tpsl":"unknown"}}`,
		`{}`, `null`,
	} {
		var e Execution
		if err := json.Unmarshal([]byte(raw), &e); err == nil {
			t.Errorf("accepted execution %s", raw)
		}
	}
	for _, pct := range []string{"0", "100", "1.00001", "NaN", "1e1"} {
		if _, err := NewPercentRetracement(pct); err == nil {
			t.Errorf("accepted percentage %q", pct)
		}
	}
	for _, raw := range []string{`{"pct":"2.5000%","px":"1"}`, `{"pct":"2.5%"}`, `{}`} {
		var r Retracement
		if err := json.Unmarshal([]byte(raw), &r); err == nil {
			t.Errorf("accepted retracement %s", raw)
		}
	}
}

func TestAdvancedCloidAndClosedFallback(t *testing.T) {
	in := limitIntent()
	order := ApprovedOrder{AssetID: in.AssetID, IsBuy: true, Price: in.Price, Size: in.Size, Execution: mustLimit(t, "GTC")}
	plan := OrderPlan{Grouping: "na", Orders: []ApprovedOrder{order}, SizeDecimals: 3}
	advanced, err := NewAdvanced(plan)
	if err != nil {
		t.Fatal(err)
	}
	in.Advanced = advanced
	id, err := ValidateAction(in, goodLimit)
	if err != nil || id != "0x0123456789abcdef0123456789abcdef" {
		t.Fatalf("generated cloid: %q %v", id, err)
	}
	plan.Orders[0].Cloid = id
	in.Advanced, err = NewAdvanced(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, wire := range []string{
		strings.Replace(goodLimit, id, "0x1123456789abcdef0123456789abcdef", 1),
		strings.Replace(goodLimit, `,"c":"`+id+`"`, "", 1),
		strings.Replace(goodLimit, `"s":"0.001"`, `"s":"0.001","s":"0.001"`, 1),
		goodLimit + ` {}`,
	} {
		if _, err := ValidateAction(in, wire); err == nil {
			t.Errorf("accepted ambiguous or changed cloid: %s", wire)
		}
	}
	in.Advanced = nil
	in.OrderType = "stop_market"
	in.WorstPx = in.Price
	if _, err := ValidateAction(in, strings.Replace(goodLimit, "Gtc", "Ioc", 1)); err == nil {
		t.Fatal("conditional order fell back to legacy limit validation")
	}
}

func TestAdvancedBindsSourceAndSnapshotsInput(t *testing.T) {
	orders, err := ScaleOrders(0, true, "6", "90", "100", "1", 3, 2, "GTC", false)
	if err != nil {
		t.Fatal(err)
	}
	p := ScalePlan{Orders: orders, StartPx: "90", EndPx: "100", TotalOrders: 3, SizeSkew: "1", TIF: "GTC", SizeDecimals: 2}
	a, err := NewAdvanced(p)
	if err != nil {
		t.Fatal(err)
	}
	in := &Intent{Kind: KindScale, Advanced: a}
	before := in.Digest()
	orders[0].Price = "91"
	if before != in.Digest() {
		t.Fatal("caller mutated approved economics through input slice")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ old, new string }{{`"size_skew":"1"`, `"size_skew":"2"`}, {`"start_px":"90"`, `"start_px":"91"`}, {`"total_orders":3`, `"total_orders":4`}} {
		var restored Intent
		if err := json.Unmarshal([]byte(strings.Replace(string(raw), change.old, change.new, 1)), &restored); err == nil {
			t.Errorf("source economics accepted with inconsistent ladder: %s", change.new)
		}
	}
}
