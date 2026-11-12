package cli

import (
	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

// The modify contract has no whole-position grouping and requires positive
// size. Do not turn dynamic position protection into a fixed-size replacement.
func (e *env) checkModifyTargets(in *trading.Intent, oids []int64) error {
	p, err := e.profile()
	if err != nil {
		return err
	}
	var orders api.GetOpenOrdersResponse
	if err := e.perpsRead(p, "GetOpenOrders", map[string]any{"address": in.Account, "skipCache": true}, &orders); err != nil {
		return err
	}
	for _, oid := range oids {
		order := findModifyOrder(orders.OpenOrders, oid)
		if order == nil || order.Coin != in.Market {
			return output.Errorf(output.CodeInvalidArgument, "order %d is not open on %s", oid, in.Market)
		}
		if order.IsPositionTpsl {
			return output.Errorf(output.CodeInvalidArgument, "order %d: %v", oid, trading.ErrPositionTpslModify).WithExec(output.ExecNone)
		}
	}
	return nil
}

func (e *env) checkModifyIntent(in *trading.Intent) error {
	if in.Advanced == nil {
		return nil
	}
	plan, ok := in.Advanced.Payload().(trading.ModifyPlan)
	if !ok {
		return nil
	}
	oids := make([]int64, 0, len(plan.Modifies))
	for _, modify := range plan.Modifies {
		oids = append(oids, modify.OrderID)
	}
	return e.checkModifyTargets(in, oids)
}

func findModifyOrder(orders []api.Order, oid int64) *api.Order {
	for i := range orders {
		if int64(orders[i].Oid) == oid {
			return &orders[i]
		}
		if child := findModifyOrder(orders[i].Children, oid); child != nil {
			return child
		}
	}
	return nil
}
