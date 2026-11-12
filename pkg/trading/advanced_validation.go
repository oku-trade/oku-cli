package trading

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/decimal"
)

const maxSafeID int64 = 1<<53 - 1

func validAsset(asset int) bool { return asset >= 0 && int64(asset) <= maxSafeID }

func (r Retracement) validate() error {
	if (r.pct == "") == (r.px == "") {
		return fmt.Errorf("retracement requires exactly one percent or price")
	}
	if r.px != "" {
		if !positive(r.px) {
			return fmt.Errorf("invalid retracement price")
		}
		return nil
	}
	if !strings.HasSuffix(r.pct, "%") {
		return fmt.Errorf("invalid retracement percent")
	}
	canonical, err := decimal.Canonical(strings.TrimSuffix(r.pct, "%"))
	if err != nil {
		return err
	}
	expected, err := NewPercentRetracement(canonical)
	if err != nil {
		return err
	}
	if expected.pct != r.pct {
		return fmt.Errorf("retracement percent must have four fractional digits")
	}
	return nil
}

func validateApprovedOrder(o ApprovedOrder, allowZero bool) error {
	if !validAsset(o.AssetID) {
		return fmt.Errorf("invalid order asset")
	}
	if !positive(o.Price) || (!positive(o.Size) && !(allowZero && o.Size == "0")) {
		return fmt.Errorf("order requires positive canonical price and size")
	}
	if o.Cloid != "" && !cloidRE.MatchString(o.Cloid) {
		return fmt.Errorf("invalid approved client order id")
	}
	return o.Execution.validate()
}

func (a *Advanced) validate() error {
	if a == nil || a.payload == nil {
		return fmt.Errorf("missing advanced payload")
	}
	switch p := a.payload.(type) {
	case OrderPlan:
		if len(p.Orders) == 0 || len(p.Orders) > 3 {
			return fmt.Errorf("order plan requires one parent and at most two children")
		}
		if p.SlippageBps < 0 || p.SlippageBps >= 10000 {
			return fmt.Errorf("invalid order slippage")
		}
		if p.ClosePosition != (p.Grouping == "positionTpsl") {
			return fmt.Errorf("close-position grouping differs")
		}
		switch p.Grouping {
		case "na":
			if len(p.Orders) != 1 {
				return fmt.Errorf("ungrouped plan requires exactly one order")
			}
		case "positionTpsl":
			if len(p.Orders) != 1 || p.Orders[0].Size != "0" || !p.Orders[0].ReduceOnly || !p.Orders[0].Execution.IsTrigger() {
				return fmt.Errorf("position TP/SL requires one size-zero reduce-only trigger")
			}
		case "normalTpsl":
			if len(p.Orders) < 2 || p.Orders[0].Execution.IsTrigger() || p.Orders[0].ReduceOnly {
				return fmt.Errorf("entry TP/SL requires a non-reduce-only limit parent")
			}
			seen := map[string]bool{}
			for _, child := range p.Orders[1:] {
				_, _, tpsl, ok := child.Execution.Trigger()
				if !ok || seen[tpsl] || !child.ReduceOnly || child.IsBuy == p.Orders[0].IsBuy || child.AssetID != p.Orders[0].AssetID {
					return fmt.Errorf("invalid entry TP/SL child")
				}
				seen[tpsl] = true
			}
		default:
			return fmt.Errorf("unsupported order grouping")
		}
		for _, o := range p.Orders {
			if err := validateApprovedOrder(o, p.ClosePosition); err != nil {
				return err
			}
			if err := validateOrderPrecision(o, p.SizeDecimals); err != nil {
				return err
			}
			market, trigger, _, ok := o.Execution.Trigger()
			if ok && market {
				expected, err := AggressivePrice(trigger, p.SizeDecimals, o.IsBuy, p.SlippageBps)
				if err != nil {
					return err
				}
				if expected != o.Price {
					return fmt.Errorf("trigger market price differs from approved slippage")
				}
			}
		}
	case ModifyPlan:
		if len(p.Modifies) == 0 {
			return fmt.Errorf("modify plan must not be empty")
		}
		if p.PositionTpsl {
			return ErrPositionTpslModify
		}
		seen := map[int64]bool{}
		for _, m := range p.Modifies {
			if m.OrderID <= 0 || m.OrderID > maxSafeID || seen[m.OrderID] {
				return fmt.Errorf("invalid or duplicate modify target")
			}
			seen[m.OrderID] = true
			if err := validateApprovedOrder(m.Order, false); err != nil {
				return err
			}
			if err := validateOrderPrecision(m.Order, p.SizeDecimals); err != nil {
				return err
			}
			market, trigger, _, ok := m.Order.Execution.Trigger()
			if ok {
				if !m.Order.ReduceOnly {
					return fmt.Errorf("trigger modification must be reduce-only")
				}
				if market {
					expected, err := AggressivePrice(trigger, p.SizeDecimals, m.Order.IsBuy, 1000)
					if err != nil {
						return err
					}
					if expected != m.Order.Price {
						return fmt.Errorf("trigger modification requires backend 1000 bps price")
					}
				}
			}
		}
	case TWAPPlan:
		if !validAsset(p.AssetID) || !positive(p.Size) || p.Minutes < 5 || p.Minutes > 10080 {
			return fmt.Errorf("invalid TWAP asset, size or duration")
		}
		if p.Conditions != nil {
			if p.Conditions.Trigger == nil && p.Conditions.StopPrice == nil {
				return fmt.Errorf("empty TWAP conditions")
			}
			if p.Conditions.Trigger != nil && !positive(p.Conditions.Trigger.Price) {
				return fmt.Errorf("invalid TWAP trigger")
			}
			if p.Conditions.StopPrice != nil && !positive(*p.Conditions.StopPrice) {
				return fmt.Errorf("invalid TWAP stop")
			}
		}
	case CancelTWAPPlan:
		if !validAsset(p.AssetID) || p.TWAPID <= 0 || p.TWAPID > maxSafeID {
			return fmt.Errorf("invalid TWAP cancel target")
		}
	case ScalePlan:
		if p.SizeDecimals < 0 || p.SizeDecimals > 6 {
			return fmt.Errorf("invalid scale size decimals")
		}
		if len(p.Orders) < 2 || len(p.Orders) > 100 || p.TotalOrders != len(p.Orders) {
			return fmt.Errorf("invalid scale count")
		}
		total := new(big.Rat)
		for _, o := range p.Orders {
			if err := validateApprovedOrder(o, false); err != nil {
				return err
			}
			size, _ := decimal.Rat(o.Size)
			total.Add(total, size)
		}
		totalString, err := decimal.Canonical(total.FloatString(p.SizeDecimals))
		if err != nil {
			return err
		}
		first := p.Orders[0]
		expected, err := ScaleOrders(first.AssetID, first.IsBuy, totalString, p.StartPx, p.EndPx, p.SizeSkew, p.TotalOrders, p.SizeDecimals, p.TIF, first.ReduceOnly)
		if err != nil {
			return err
		}
		for i, o := range p.Orders {
			expected[i].Cloid = o.Cloid
		}
		if !reflect.DeepEqual(expected, p.Orders) {
			return fmt.Errorf("scale ladder differs from source parameters")
		}
	case TrailingStopPlan:
		if !validAsset(p.AssetID) || !positive(p.Size) {
			return fmt.Errorf("invalid trailing stop asset or size")
		}
		if p.ActivationPrice != nil && !positive(*p.ActivationPrice) {
			return fmt.Errorf("invalid trailing activation price")
		}
		return p.Retracement.validate()
	case CancelManyPlan:
		if len(p.Cancels) == 0 {
			return fmt.Errorf("cancel plan must not be empty")
		}
		seen := map[CancelTarget]bool{}
		for _, c := range p.Cancels {
			if !validAsset(c.AssetID) || c.OrderID <= 0 || c.OrderID > maxSafeID || seen[c] {
				return fmt.Errorf("invalid or duplicate cancel target")
			}
			seen[c] = true
		}
	default:
		return fmt.Errorf("unsupported advanced payload %T", a.payload)
	}
	return nil
}

func validateOrderPrecision(o ApprovedOrder, sizeDecimals int) error {
	if sizeDecimals < 0 || sizeDecimals > 6 {
		return fmt.Errorf("invalid size decimals")
	}
	if decimal.Decimals(o.Size) > sizeDecimals {
		return fmt.Errorf("order size exceeds market precision")
	}
	if err := decimal.ValidatePerpPrice(o.Price, sizeDecimals); err != nil {
		return err
	}
	if _, px, _, ok := o.Execution.Trigger(); ok {
		return decimal.ValidatePerpPrice(px, sizeDecimals)
	}
	return nil
}

// validateAdvancedAction compares complete signed objects after validating their
// exact schema. Only generated CLOIDs and the explicitly capped builder policy
// are variable. Missing fields, added fields and reordered children fail closed.
func validateAdvancedAction(in *Intent, actionJSON string) (string, error) {
	if err := in.Advanced.validate(); err != nil {
		return "", mismatch("invalid approved plan: %v", err)
	}
	if in.Kind != in.Advanced.Kind() {
		return "", mismatch("advanced payload kind differs from intent")
	}
	var expected map[string]any
	var orders []ApprovedOrder
	allowBuilder := false
	switch p := in.Advanced.payload.(type) {
	case OrderPlan:
		expected = map[string]any{"type": "order", "orders": p.Orders, "grouping": p.Grouping}
		orders = p.Orders
		allowBuilder = true
	case ScalePlan:
		expected = map[string]any{"type": "order", "orders": p.Orders, "grouping": "na"}
		orders = p.Orders
		allowBuilder = true
	case ModifyPlan:
		expected = map[string]any{"type": "batchModify", "modifies": p.Modifies}
		if p.AlwaysPlace {
			expected["a"] = true
		}
		for _, m := range p.Modifies {
			orders = append(orders, m.Order)
		}
	case TWAPPlan:
		twap := p
		twap.Conditions = nil
		expected = map[string]any{"type": "twapOrder", "twap": twap}
		if p.Conditions != nil {
			expected["details"] = p.Conditions
		}
	case CancelTWAPPlan:
		expected = map[string]any{"type": "twapCancel", "a": p.AssetID, "t": p.TWAPID}
	case TrailingStopPlan:
		raw, err := json.Marshal(p)
		if err != nil {
			return "", mismatch("encode trailing plan: %v", err)
		}
		if err := json.Unmarshal(raw, &expected); err != nil {
			return "", mismatch("decode trailing plan: %v", err)
		}
		expected["type"] = "trailingStop"
	case CancelManyPlan:
		expected = map[string]any{"type": "cancel", "cancels": p.Cancels}
	}
	actual, err := actionObject([]byte(actionJSON))
	if err != nil {
		return "", mismatch("decode action: %v", err)
	}
	if allowBuilder {
		if b, ok := actual["builder"]; ok {
			builder := b.(map[string]any)
			fee, err := builder["f"].(json.Number).Int64()
			if err != nil || fee < 0 || fee > int64(in.MaxBuilderFee10) || in.BuilderAddress == "" || !strings.EqualFold(builder["b"].(string), in.BuilderAddress) {
				return "", mismatch("builder differs from approved policy")
			}
			delete(actual, "builder")
		}
	}
	cloid := ""
	if len(orders) > 0 {
		var actualOrders []any
		if actual["type"] == "order" {
			actualOrders, _ = actual["orders"].([]any)
		} else if actual["type"] == "batchModify" {
			modifies, _ := actual["modifies"].([]any)
			for _, m := range modifies {
				actualOrders = append(actualOrders, m.(map[string]any)["order"])
			}
		}
		if len(actualOrders) != len(orders) {
			return "", mismatch("order count differs")
		}
		for i, o := range actualOrders {
			fields := o.(map[string]any)
			if c, ok := fields["c"]; ok {
				id := c.(string)
				if id != "" && !cloidRE.MatchString(id) {
					return "", mismatch("invalid client order id")
				}
				if i == 0 {
					cloid = id
				}
				if orders[i].Cloid == "" {
					delete(fields, "c")
				}
			}
		}
	}
	raw, err := json.Marshal(expected)
	if err != nil {
		return "", mismatch("encode approved action: %v", err)
	}
	want, err := actionObject(raw)
	if err != nil {
		return "", mismatch("decode approved action: %v", err)
	}
	if !reflect.DeepEqual(actual, want) {
		return "", mismatch("signed fields differ from approved %s plan", in.Kind)
	}
	return cloid, nil
}

func actionObject(raw []byte) (map[string]any, error) {
	var object map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&object); err != nil {
		return nil, err
	}
	return object, nil
}
