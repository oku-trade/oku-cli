package cli

import (
	"strings"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

func (e *env) prepareOrderPlan(in *trading.Intent, m *api.Market, buy bool) error {
	if err := rejectPerpFlags(e, "trigger-price", "close-position", "take-profit-price", "stop-loss-price", "tp-limit-price", "sl-limit-price", "tp-size", "sl-size", "price", "tif", "slippage-bps"); err != nil {
		return err
	}
	typ := in.OrderType
	triggerKind := strings.HasPrefix(typ, "stop_") || strings.HasPrefix(typ, "take_")
	marketKind := strings.HasSuffix(typ, "_market")
	attached := str(e, "take-profit-price") != "" || str(e, "stop-loss-price") != ""
	if triggerKind && attached {
		return output.Errorf(output.CodeInvalidArgument, "entry TP/SL cannot be attached to another trigger")
	}
	closePosition := e.cmd.Bool("close-position")
	if closePosition && !triggerKind {
		return output.Errorf(output.CodeInvalidArgument, "--close-position requires a trigger order type")
	}
	if closePosition {
		if e.cmd.IsSet("reduce-only") && !in.ReduceOnly {
			return output.Errorf(output.CodeInvalidArgument, "whole-position triggers are always reduce-only")
		}
		in.ReduceOnly = true
	}
	if !triggerKind && str(e, "trigger-price") != "" {
		return output.Errorf(output.CodeInvalidArgument, "--trigger-price requires a trigger order type")
	}
	if triggerKind && e.cmd.IsSet("tif") {
		return output.Errorf(output.CodeInvalidArgument, "--tif is not valid for trigger orders")
	}
	slip := e.cmd.Int("slippage-bps")
	needsSlip := marketKind || attached
	if !needsSlip && slip != 0 && typ != "market" {
		return output.Errorf(output.CodeInvalidArgument, "--slippage-bps requires a market-style order or attached TP/SL")
	}
	if needsSlip || typ == "market" {
		if slip == 0 {
			slip = defaultSlippageBps
		}
		if slip < 1 || slip > 1000 {
			return output.Errorf(output.CodeInvalidArgument, "--slippage-bps must be 1-1000")
		}
	}
	plan := trading.OrderPlan{Grouping: "na", SlippageBps: slip, ClosePosition: closePosition, SizeDecimals: m.SzDecimals}
	order := trading.ApprovedOrder{AssetID: m.AssetID, IsBuy: buy, Size: in.Size, ReduceOnly: in.ReduceOnly}
	var err error
	switch {
	case triggerKind:
		trigger, err := perpDecimal(e, "trigger-price", m, false, true)
		if err != nil {
			return err
		}
		tpsl := "sl"
		if strings.HasPrefix(typ, "take_") {
			tpsl = "tp"
		}
		order.Execution, err = trading.NewTriggerExecution(marketKind, trigger, tpsl)
		if err != nil {
			return output.Wrap(output.CodeInvalidArgument, err, "trigger")
		}
		if marketKind {
			if str(e, "price") != "" {
				return output.Errorf(output.CodeInvalidArgument, "--price is not allowed for a market-style trigger")
			}
			order.Price, err = trading.AggressivePrice(trigger, m.SzDecimals, buy, slip)
		} else {
			order.Price, err = perpDecimal(e, "price", m, false, true)
		}
		if closePosition {
			plan.Grouping = "positionTpsl"
			order.Size = "0"
			order.ReduceOnly = true
		}
	case typ == "limit":
		order.Price, err = perpDecimal(e, "price", m, false, true)
		if err == nil {
			order.Execution, err = trading.NewLimitExecution(strings.ToUpper(str(e, "tif")))
		}
	case typ == "market":
		if str(e, "price") != "" || e.cmd.IsSet("tif") {
			return output.Errorf(output.CodeInvalidArgument, "market orders cannot set --price or --tif")
		}
		p, err := e.profile()
		if err != nil {
			return err
		}
		var q api.GetMarketPriceResponse
		if err := e.perpsRead(p, "GetMarketPrice", map[string]string{"coin": in.Market}, &q); err != nil {
			return err
		}
		ref, err := decimal.Canonical(q.MidPx)
		if err != nil {
			return output.Wrap(output.CodeUpstream, err, "market mid price")
		}
		in.ReferencePx = ref
		in.SlippageBps = slip
		in.WorstPx, err = trading.WorstPrice(ref, buy, slip, 6-m.SzDecimals)
		if err != nil {
			return output.Wrap(output.CodeInvalidArgument, err, "market bound")
		}
		order.Price = in.WorstPx
		order.Execution, err = trading.NewLimitExecution("IOC")
	}
	if err != nil {
		return err
	}
	in.Price = order.Price
	plan.Orders = append(plan.Orders, order)
	for _, child := range []struct{ trigger, limit, size, tpsl string }{{"take-profit-price", "tp-limit-price", "tp-size", "tp"}, {"stop-loss-price", "sl-limit-price", "sl-size", "sl"}} {
		trigger, err := perpDecimal(e, child.trigger, m, false, false)
		if err != nil {
			return err
		}
		if trigger == "" {
			if str(e, child.limit) != "" || str(e, child.size) != "" {
				return output.Errorf(output.CodeInvalidArgument, "--%s and --%s require --%s", child.limit, child.size, child.trigger)
			}
			continue
		}
		limit, err := perpDecimal(e, child.limit, m, false, false)
		if err != nil {
			return err
		}
		size, err := perpDecimal(e, child.size, m, true, false)
		if err != nil {
			return err
		}
		if size == "" {
			size = in.Size
		}
		market := limit == ""
		if market {
			limit, err = trading.AggressivePrice(trigger, m.SzDecimals, !buy, slip)
			if err != nil {
				return output.Wrap(output.CodeInvalidArgument, err, "attached trigger bound")
			}
		}
		execution, err := trading.NewTriggerExecution(market, trigger, child.tpsl)
		if err != nil {
			return output.Wrap(output.CodeInvalidArgument, err, "attached trigger")
		}
		plan.Grouping = "normalTpsl"
		plan.Orders = append(plan.Orders, trading.ApprovedOrder{AssetID: m.AssetID, IsBuy: !buy, Price: limit, Size: size, ReduceOnly: true, Execution: execution})
	}
	return setAdvanced(in, plan)
}

// advancedCreateRequest translates the already approved typed plan into the
// backend metadata. The signed venue action is checked independently against
// the plan by trading.ValidateAction.
func advancedCreateRequest(in *trading.Intent) api.CreateActionRequest {
	req := api.CreateActionRequest{Address: in.Account}
	side := func(buy bool) string {
		if buy {
			return "buy"
		}
		return "sell"
	}
	switch plan := in.Advanced.Payload().(type) {
	case trading.OrderPlan:
		main := plan.Orders[0]
		o := &api.OrderActionMetadata{Coin: in.Market, Side: side(main.IsBuy), Sz: in.Size, LimitPx: main.Price, ReduceOnly: main.ReduceOnly, Tif: strings.ToUpper(main.Execution.LimitTIF()), SlippageBps: plan.SlippageBps, ClosePosition: plan.ClosePosition}
		o.OrderType = "limit"
		market, trigger, tpsl, isTrigger := main.Execution.Trigger()
		if isTrigger {
			o.TriggerPx = trigger
			o.Tif = ""
			o.OrderType = "stop_limit"
			if tpsl == "tp" {
				o.OrderType = "take_limit"
			}
			if market {
				o.LimitPx = ""
				o.OrderType = "stop_market"
				if tpsl == "tp" {
					o.OrderType = "take_market"
				}
			}
		}
		for _, c := range plan.Orders[1:] {
			market, trigger, tpsl, _ := c.Execution.Trigger()
			if tpsl == "tp" {
				o.TakeProfitPx, o.TpSz = trigger, c.Size
				if !market {
					o.TpLimitPx = c.Price
				}
			} else {
				o.StopLossPx, o.SlSz = trigger, c.Size
				if !market {
					o.SlLimitPx = c.Price
				}
			}
		}
		req.Params.Order = o
	case trading.ModifyPlan:
		m := &api.ModifyActionMetadata{}
		for _, c := range plan.Modifies {
			o := c.Order
			item := api.ModifyOrderMetadata{Coin: in.Market, Oid: api.Int64(c.OrderID), Size: o.Size, LimitPx: o.Price, IsBuy: o.IsBuy, Tif: strings.ToUpper(o.Execution.LimitTIF()), ReduceOnly: o.ReduceOnly}
			market, trigger, tpsl, ok := o.Execution.Trigger()
			if ok {
				item.TriggerPx, item.IsMarket, item.Tif = trigger, market, ""
				if tpsl == "tp" {
					item.Tpsl = api.TriggerOrderTypeTakeProfit
				} else {
					item.Tpsl = api.TriggerOrderTypeStopLoss
				}
				if market {
					item.LimitPx = ""
				}
			}
			m.Modifies = append(m.Modifies, item)
		}
		req.Params.Modify = m
	case trading.TWAPPlan:
		o := &api.TWAPActionMetadata{Coin: in.Market, Side: side(plan.IsBuy), Sz: plan.Size, DurationMinutes: plan.Minutes, Randomize: plan.Randomize, ReduceOnly: plan.ReduceOnly}
		if plan.Conditions != nil {
			if plan.Conditions.Trigger != nil {
				o.TriggerPx = plan.Conditions.Trigger.Price
				o.TriggerActivateAbove = plan.Conditions.Trigger.ActivateAbove
			}
			if plan.Conditions.StopPrice != nil {
				o.StopPx = *plan.Conditions.StopPrice
			}
		}
		req.Params.Twap = o
	case trading.CancelTWAPPlan:
		req.Params.CancelTwap = &api.CancelTWAPActionMetadata{Coin: in.Market, TwapID: api.Int64(plan.TWAPID)}
	case trading.ScalePlan:
		o := plan.Orders[0]
		req.Params.Scale = &api.ScaleActionMetadata{Coin: in.Market, Side: side(o.IsBuy), Sz: in.Size, StartPx: plan.StartPx, EndPx: plan.EndPx, TotalOrders: plan.TotalOrders, SizeSkew: plan.SizeSkew, Tif: plan.TIF, ReduceOnly: o.ReduceOnly}
	case trading.TrailingStopPlan:
		o := &api.TrailingStopActionMetadata{Coin: in.Market, Side: side(plan.IsBuy), Sz: plan.Size, ReduceOnly: plan.ReduceOnly, RetracementPct: plan.Retracement.Percent(), RetracementPx: plan.Retracement.Price()}
		if plan.ActivationPrice != nil {
			o.ActivationPx = *plan.ActivationPrice
		}
		req.Params.TrailingStop = o
	case trading.CancelManyPlan:
		o := &api.CancelOrdersActionMetadata{}
		for _, c := range plan.Cancels {
			o.Orders = append(o.Orders, api.CancelOrderItem{Coin: in.Market, Oid: api.Int64(c.OrderID)})
		}
		req.Params.CancelOrders = o
	}
	return req
}
