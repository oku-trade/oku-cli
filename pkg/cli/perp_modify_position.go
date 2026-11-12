package cli

import (
	"strings"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

type positionModifyTarget struct {
	buy    bool
	size   string
	tpsl   string
	market bool
}

// A batchModify retains the target's grouping. Hyperliquid ignores its size for
// position TP/SL, but the backend requires the positive live position size.
func (e *env) positionModifyTarget(in *trading.Intent, m *api.Market, oid int64) (*positionModifyTarget, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	var orders api.GetOpenOrdersResponse
	if err := e.perpsRead(p, "GetOpenOrders", map[string]any{"address": in.Account, "skipCache": true}, &orders); err != nil {
		return nil, err
	}
	order := findModifyOrder(listOrders(orders.OpenOrders), oid)
	if order == nil || order.Coin != in.Market {
		return nil, output.Errorf(output.CodeInvalidArgument, "order %d is not open on %s", oid, in.Market)
	}
	if !order.IsPositionTpsl {
		return nil, nil
	}
	label := strings.ToLower(strings.ReplaceAll(order.OrderType, "_", " "))
	market := strings.HasSuffix(label, "market")
	if !order.IsTrigger || !order.ReduceOnly || order.Tpsl == "" || (!market && !strings.HasSuffix(label, "limit")) || (order.Side != "A" && order.Side != "B") {
		return nil, output.Errorf(output.CodeUpstream, "unrecognized position TP/SL order %d", oid)
	}
	side := sideName(order.Side)
	if (e.cmd.IsSet("side") && str(e, "side") != side) || (e.cmd.IsSet("tpsl") && str(e, "tpsl") != order.Tpsl) || (e.cmd.IsSet("market-trigger") && e.cmd.Bool("market-trigger") != market) {
		return nil, output.Errorf(output.CodeInvalidArgument, "position TP/SL modify must retain its side, TP/SL kind and execution mode")
	}
	var positions api.GetPerpPositionsResponse
	// GetPerpPositions reads the venue directly and has no cache flag.
	if err := e.perpsRead(p, "GetPerpPositions", map[string]string{"address": in.Account}, &positions); err != nil {
		return nil, err
	}
	if positions.MarginSummary == nil {
		return nil, output.Errorf(output.CodeUpstream, "incomplete position data from Oku")
	}
	for _, position := range positions.Positions {
		if position.Coin != in.Market {
			continue
		}
		size, err := decimal.Canonical(strings.TrimPrefix(position.Szi, "-"))
		if err != nil {
			return nil, output.Wrap(output.CodeUpstream, err, "live position size")
		}
		if decimal.IsZero(size) {
			break
		}
		if (strings.HasPrefix(position.Szi, "-")) != (order.Side == "B") {
			return nil, output.Errorf(output.CodeInvalidArgument, "position TP/SL closing side does not match the live position")
		}
		if m.SzDecimals < 0 || m.SzDecimals > 6 {
			return nil, output.Errorf(output.CodeUpstream, "invalid market size precision")
		}
		whole, fraction, _ := strings.Cut(size, ".")
		if len(fraction) > m.SzDecimals {
			if m.SzDecimals == 0 {
				size = whole
			} else {
				size, err = decimal.Canonical(whole + "." + fraction[:m.SzDecimals])
				if err != nil {
					return nil, output.Wrap(output.CodeUpstream, err, "formatting live position size")
				}
			}
		}
		if decimal.IsZero(size) {
			return nil, output.Errorf(output.CodeInvalidArgument, "live position size is below market precision")
		}
		if e.cmd.IsSet("size") {
			requested, err := perpDecimal(e, "size", m, true, true)
			if err != nil {
				return nil, err
			}
			if requested != size {
				return nil, output.Errorf(output.CodeInvalidArgument, "--size conflicts with whole-position TP/SL size %s, omit --size to use the live position", size)
			}
		}
		return &positionModifyTarget{buy: order.Side == "B", size: size, tpsl: order.Tpsl, market: market}, nil
	}
	return nil, output.Errorf(output.CodeInvalidArgument, "no open position on %s for whole-position TP/SL modify", in.Market)
}

func findModifyOrder(orders []listedOrder, oid int64) *listedOrder {
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
