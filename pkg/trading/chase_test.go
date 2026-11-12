package trading

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChaseTarget(t *testing.T) {
	for _, tc := range []struct {
		name, side, bid, ask, want string
		precision                  int
		bad                        bool
	}{
		{"buy inside", "buy", "100", "101", "100.01", 2, false},
		{"sell inside", "sell", "100", "101", "100.99", 2, false},
		{"buy single tick", "buy", "100", "100.01", "100", 2, false},
		{"sell single tick", "sell", "100", "100.01", "100.01", 2, false},
		{"integer tick", "buy", "100000", "100002", "100001", 2, false},
		{"small price", "sell", "0.001", "0.001003", "0.001002", 0, false},
		{"size caps tick", "buy", "1", "1.1", "1.01", 4, false},
		{"magnitude boundary", "buy", "9.9999", "10.001", "10", 0, false},
		{"locked", "buy", "100", "100", "", 2, true},
		{"crossed", "sell", "101", "100", "", 2, true},
		{"missing", "buy", "", "100", "", 2, true},
		{"off grid", "buy", "100.001", "101", "", 2, true},
		{"invalid side", "bid", "100", "101", "", 2, true},
		{"invalid precision", "buy", "100", "101", "", 7, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputeChaseTargetPx(tc.side, tc.bid, tc.ask, tc.precision)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %q, %v, want %q bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestChaseDecisions(t *testing.T) {
	for _, tc := range []struct{ name, current, bid, ask, reference, distance, worst, kind, reason string }{
		{"place", "", "100", "101", "100", "2", "102", "place", ""},
		{"numeric threshold", "100.0100", "100", "101", "100", "2", "102", "hold", ""},
		{"one tick change", "100", "100", "101", "100", "2", "102", "reprice", ""},
		{"adverse leash", "100", "103", "104", "100", "2", "102", "stop", "max-distance"},
		{"favorable leash", "100", "96", "97", "100", "2", "102", "stop", "max-distance"},
		{"leash equality", "100", "101.99", "103", "100", "2", "102", "reprice", ""},
		{"hard bound", "100", "102", "103", "100", "", "102", "stop", "worst-price"},
		{"malformed leash", "100", "100", "101", "100", "bad", "102", "stop", "max-distance"},
		{"unavailable book", "100", "0", "101", "100", "2", "102", "hold", "book-unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideChase(ChaseDecisionInput{Side: "buy", BestBid: tc.bid, BestAsk: tc.ask, SizeDecimals: 2, CurrentPx: tc.current, ReferencePx: tc.reference, MaxDistance: tc.distance, WorstPx: tc.worst})
			if got.Kind != tc.kind || got.Reason != tc.reason {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestChaseRemainingAndOutcomes(t *testing.T) {
	for _, tc := range []struct {
		tracked, live string
		fresh         bool
		want          string
		ok            bool
	}{
		{"2", "1.2", true, "1.2", true}, {"1", "1.2", true, "1", true},
		{"2", "1", false, "", false}, {"2", "", true, "", false},
		{"0", "1", true, "", false}, {"2", "0", true, "", false},
		{"bad", "1", true, "", false}, {"2", "NaN", true, "", false},
	} {
		got, ok := ResolveChaseRepriceSize(tc.tracked, tc.live, tc.fresh)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%+v got %q %v", tc, got, ok)
		}
	}
	for _, tc := range []struct {
		status string
		want   ChaseOrderOutcome
	}{
		{"filled", ChaseOrderFilled}, {"open", ChaseOrderOpen}, {"triggered", ChaseOrderOpen},
		{"marginCanceled", ChaseOrderGone}, {"scheduledCancel", ChaseOrderGone},
		{"self_trade_cancelled", ChaseOrderGone}, {"tickRejected", ChaseOrderGone},
		{"unknownOid", ChaseOrderUnknown}, {"", ChaseOrderUnknown},
		{"Cannot modify canceled or filled order", ChaseOrderUnknown}, {"new-status", ChaseOrderUnknown},
	} {
		if got := ClassifyChaseOrderStatus(tc.status); got != tc.want {
			t.Fatalf("status %q got %s", tc.status, got)
		}
	}
	if !IsOrderGoneError("Cannot modify canceled or filled order") || IsOrderGoneError("filled") {
		t.Fatal("ambiguous modify rejection classification")
	}
	if !IsMinNotionalError("Order must have minimum value") || !IsRetriableChaseError("Post only would immediately match") {
		t.Fatal("transient rejection classification")
	}
}

func chaseFixture(t *testing.T) (*Intent, *Intent, ApprovedOrder) {
	t.Helper()
	p, err := NewChase(ChasePlan{AssetID: 3, Side: "buy", TotalSize: "2", ReferencePx: "100", MaxDistance: "2", SizeDecimals: 2})
	if err != nil {
		t.Fatal(err)
	}
	parent := &Intent{Kind: KindChase, Chase: p, AssetID: 3, Network: "mainnet", Account: "account", APIURL: "https://example.test", Market: "ETH", ExpiresAt: "2026-11-12T12:00:00Z", BuilderAddress: builder, MaxBuilderFee10: 10}
	child := *parent
	child.Chase = nil
	order := ApprovedOrder{AssetID: 3, IsBuy: true, Price: "101", Size: "1", Execution: mustLimit(t, "ALO")}
	return parent, &child, order
}

func TestChaseAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Intent, *Intent, *ModifyPlan, *int64, *string)
		bad    bool
	}{
		{"partial remaining", nil, false},
		{"wrong asset", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.Modifies[0].Order.AssetID++ }, true},
		{"wrong side", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.Modifies[0].Order.IsBuy = false }, true},
		{"wrong reduce only", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.Modifies[0].Order.ReduceOnly = true }, true},
		{"oversize", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.Modifies[0].Order.Size = "1.01" }, true},
		{"adverse price", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.Modifies[0].Order.Price = "102.01" }, true},
		{"favorable leash", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.Modifies[0].Order.Price = "97.99" }, true},
		{"at bound", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.Modifies[0].Order.Price = "102" }, false},
		{"GTC", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) {
			p.Modifies[0].Order.Execution = mustLimit(t, "GTC")
		}, true},
		{"trigger", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) {
			p.Modifies[0].Order.ReduceOnly = true
			p.Modifies[0].Order.Execution = mustTrigger(t, false, "101", "tp")
		}, true},
		{"foreign oid", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.Modifies[0].OrderID++ }, true},
		{"no owned oid", func(_, _ *Intent, _ *ModifyPlan, oid *int64, _ *string) { *oid = 0 }, true},
		{"always place", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.AlwaysPlace = true }, true},
		{"extra child", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) {
			next := p.Modifies[0]
			next.OrderID++
			p.Modifies = append(p.Modifies, next)
		}, true},
		{"different precision", func(_, _ *Intent, p *ModifyPlan, _ *int64, _ *string) { p.SizeDecimals = 3 }, true},
		{"account", func(_, c *Intent, _ *ModifyPlan, _ *int64, _ *string) { c.Account = "other" }, true},
		{"network", func(_, c *Intent, _ *ModifyPlan, _ *int64, _ *string) { c.Network = "testnet" }, true},
		{"endpoint", func(_, c *Intent, _ *ModifyPlan, _ *int64, _ *string) { c.APIURL = "https://other.test" }, true},
		{"market", func(_, c *Intent, _ *ModifyPlan, _ *int64, _ *string) { c.Market = "BTC" }, true},
		{"fees", func(_, c *Intent, _ *ModifyPlan, _ *int64, _ *string) { c.MaxBuilderFee10++ }, true},
		{"expiry", func(_, c *Intent, _ *ModifyPlan, _ *int64, _ *string) { c.ExpiresAt = "2027-01-01T00:00:00Z" }, true},
		{"remaining exceeds total", func(_, _ *Intent, _ *ModifyPlan, _ *int64, r *string) { *r = "3" }, true},
		{"zero remaining", func(_, _ *Intent, _ *ModifyPlan, _ *int64, r *string) { *r = "0" }, true},
		{"invalid remaining", func(_, _ *Intent, _ *ModifyPlan, _ *int64, r *string) { *r = "bad" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, child, order := chaseFixture(t)
			plan := ModifyPlan{Modifies: []ApprovedModify{{OrderID: 7, Order: order}}, SizeDecimals: 2}
			oid, remaining := int64(7), "1"
			if tc.change != nil {
				tc.change(parent, child, &plan, &oid, &remaining)
			}
			child.Kind = KindModify
			// Keep malformed native plans available to exercise the authorization boundary.
			child.Advanced = &Advanced{payload: plan}
			if err := ValidateChaseChild(parent, child, oid, remaining); (err != nil) != tc.bad {
				t.Fatalf("got %v, bad=%v", err, tc.bad)
			}
		})
	}
}

func TestChaseNativeBytesAndCancellation(t *testing.T) {
	parent, child, order := chaseFixture(t)
	child.Kind = KindModify
	child.Advanced = &Advanced{payload: ModifyPlan{Modifies: []ApprovedModify{{OrderID: 7, Order: order}}, SizeDecimals: 2}}
	wire := `{"type":"batchModify","modifies":[{"oid":7,"order":{"a":3,"b":true,"p":"101","s":"1","r":false,"t":{"limit":{"tif":"Alo"}}}}]}`
	if _, err := ValidateChaseAction(parent, child, 7, "1", wire); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(wire, `"101"`, `"102"`, 1), strings.Replace(wire, `"oid":7`, `"oid":8`, 1), strings.Replace(wire, `"Alo"`, `"Gtc"`, 1)} {
		if _, err := ValidateChaseAction(parent, child, 7, "1", bad); err == nil {
			t.Fatal("accepted changed signing bytes")
		}
	}
	child.Kind = KindCancelMany
	child.ExpiresAt = "2027-01-01T00:00:00Z"
	child.Advanced = &Advanced{payload: CancelManyPlan{Cancels: []CancelTarget{{AssetID: 3, OrderID: 7}}}}
	if _, err := ValidateChaseAction(parent, child, 7, "0", `{"type":"cancel","cancels":[{"a":3,"o":7}]}`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateChaseChild(parent, child, 8, "0"); err == nil {
		t.Fatal("canceled foreign order")
	}
	if _, err := ValidateAction(parent, wire); err == nil {
		t.Fatal("parent policy became signable")
	}
	child.Kind = KindOrder
	child.ExpiresAt = parent.ExpiresAt
	order.Size = "2"
	child.Advanced = &Advanced{payload: OrderPlan{Grouping: "na", Orders: []ApprovedOrder{order}, SizeDecimals: 2}}
	if err := ValidateChaseChild(parent, child, 0, "2"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateChaseChild(parent, child, 7, "2"); err == nil {
		t.Fatal("placed while an owned order exists")
	}
	if err := ValidateChaseChild(parent, child, 0, "1"); err == nil {
		t.Fatal("placed after partial fill without owned oid")
	}
}

func TestChaseApprovalBoundsAndDigest(t *testing.T) {
	for _, tc := range []struct {
		side, reference, distance, want string
		precision                       int
	}{
		{"buy", "100", "2.009", "102", 2}, {"sell", "100", "2.0091", "97.991", 2},
		{"buy", "100", "", "9007199254740991", 2}, {"sell", "100", "", "0.0001", 2},
	} {
		got, err := ChaseWorstPrice(tc.side, tc.reference, tc.distance, tc.precision)
		if err != nil || got != tc.want {
			t.Fatalf("%+v got %q %v", tc, got, err)
		}
	}
	parent, _, _ := chaseFixture(t)
	raw, err := json.Marshal(parent)
	if err != nil {
		t.Fatal(err)
	}
	var restored Intent
	if err := json.Unmarshal(raw, &restored); err != nil || restored.Digest() != parent.Digest() {
		t.Fatalf("round trip: %v", err)
	}
	digest := parent.Digest()
	for _, change := range []func(*ChasePlan){
		func(p *ChasePlan) { p.WorstPx = "101" }, func(p *ChasePlan) { p.ReferencePx = "100.01" },
		func(p *ChasePlan) { p.TotalSize = "1" }, func(p *ChasePlan) { p.ReduceOnly = true },
		func(p *ChasePlan) { p.MaxDistance = "3" }, func(p *ChasePlan) { p.AssetID++ },
	} {
		next := *parent.Chase
		change(&next)
		candidate := *parent
		candidate.Chase = &next
		if candidate.Digest() == digest {
			t.Fatal("approval policy not bound in digest")
		}
	}
	if err := json.Unmarshal([]byte(strings.Replace(string(raw), `"worst_px":"102"`, `"worst_px":"103"`, 1)), &restored); err == nil {
		t.Fatal("decoded policy outside leash")
	}
	if _, err := NewChase(ChasePlan{AssetID: 3, Side: "buy", TotalSize: "2.001", ReferencePx: "100", MaxDistance: "2", SizeDecimals: 2}); err == nil {
		t.Fatal("accepted excess size precision")
	}
}

func TestChaseSellAndNoLeashAuthorization(t *testing.T) {
	for _, tc := range []struct {
		side, distance, price string
		bad                   bool
	}{
		{"sell", "2", "98", false},
		{"sell", "2", "97.999", true},
		{"sell", "2", "102.01", true},
		{"sell", "", "0.0001", false},
		{"buy", "", "9007199254740991", false},
		{"buy", "", "9007199254740992", true},
	} {
		parent, child, order := chaseFixture(t)
		plan := *parent.Chase
		plan.Side, plan.MaxDistance, plan.WorstPx, plan.ReduceOnly = tc.side, tc.distance, "", true
		var err error
		parent.Chase, err = NewChase(plan)
		if err != nil {
			t.Fatal(err)
		}
		order.IsBuy, order.Price, order.ReduceOnly = tc.side == "buy", tc.price, true
		child.Kind = KindModify
		child.Advanced = &Advanced{payload: ModifyPlan{Modifies: []ApprovedModify{{OrderID: 7, Order: order}}, SizeDecimals: 2}}
		if err := ValidateChaseChild(parent, child, 7, "1"); (err != nil) != tc.bad {
			t.Fatalf("%+v got %v", tc, err)
		}
	}
}
