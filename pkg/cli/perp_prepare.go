package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/trading"
	urfave "github.com/urfave/cli/v3"
)

func advancedOrderFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "trigger-price", Usage: "trigger price for a stop, take profit, or conditional TWAP"},
		&urfave.BoolFlag{Name: "close-position", Usage: "attach a trigger to the whole position (positionTpsl, exchange size 0)"},
		&urfave.StringFlag{Name: "take-profit-price", Usage: "take-profit trigger attached to an entry"},
		&urfave.StringFlag{Name: "stop-loss-price", Usage: "stop-loss trigger attached to an entry"},
		&urfave.StringFlag{Name: "tp-limit-price", Usage: "attached take-profit limit price (otherwise market)"},
		&urfave.StringFlag{Name: "sl-limit-price", Usage: "attached stop-loss limit price (otherwise market)"},
		&urfave.StringFlag{Name: "tp-size", Usage: "attached take-profit size (default entry size)"},
		&urfave.StringFlag{Name: "sl-size", Usage: "attached stop-loss size (default entry size)"},
		&urfave.IntFlag{Name: "duration-minutes", Usage: "TWAP duration, 5 to 10080 minutes"},
		&urfave.BoolFlag{Name: "randomize", Usage: "randomize TWAP slicing"},
		&urfave.BoolFlag{Name: "trigger-activate-above", Usage: "activate conditional TWAP above the trigger price"},
		&urfave.StringFlag{Name: "stop-price", Usage: "TWAP stop price"},
		&urfave.StringFlag{Name: "start-price", Usage: "first scale child price"},
		&urfave.StringFlag{Name: "end-price", Usage: "last scale child price"},
		&urfave.IntFlag{Name: "total-orders", Usage: "scale child count, 2 to 100"},
		&urfave.StringFlag{Name: "size-skew", Value: "1", Usage: "scale last-child / first-child size ratio"},
		&urfave.StringFlag{Name: "activation-price", Usage: "trailing stop activation price (otherwise immediate)"},
		&urfave.StringFlag{Name: "retracement-pct", Usage: "trailing retracement percent, exclusive with retracement-price"},
		&urfave.StringFlag{Name: "retracement-price", Usage: "trailing fixed retracement distance, exclusive with retracement-pct"},
	}
}

func usesAdvancedOrder(e *env) bool {
	if t := str(e, "type"); t != "limit" && t != "market" {
		return true
	}
	for _, f := range advancedOrderFlags() {
		if e.cmd.IsSet(f.Names()[0]) {
			return true
		}
	}
	return false
}

func modifyOrderFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "market", Usage: "market (required)"},
		&urfave.Int64Flag{Name: "order-id", Usage: "resting order id to replace atomically (required)"},
		&urfave.StringFlag{Name: "side", Usage: "replacement side: buy or sell (inferred for position TP/SL)"},
		&urfave.StringFlag{Name: "size", Usage: "complete replacement size (live position size for position TP/SL)"},
		&urfave.StringFlag{Name: "price", Usage: "replacement limit price (required unless market trigger)"},
		&urfave.StringFlag{Name: "trigger-price", Usage: "replacement TP/SL trigger price"},
		&urfave.StringFlag{Name: "tpsl", Usage: "tp or sl, required with trigger-price unless position TP/SL"},
		&urfave.BoolFlag{Name: "market-trigger", Usage: "market-style trigger replacement with backend fixed 10% slippage"},
		&urfave.StringFlag{Name: "tif", Value: "gtc", Usage: "plain limit time in force: gtc, ioc, alo"},
		&urfave.BoolFlag{Name: "reduce-only", Usage: "plain limit replacement only reduces a position (trigger replacements always do)"},
		&urfave.StringFlag{Name: "intent-id", Usage: "caller-chosen idempotency id"},
		&urfave.DurationFlag{Name: "expires", Value: defaultIntentTTL, Usage: "intent lifetime (max 1h)"},
	}
}
func cancelTWAPFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "market", Usage: "TWAP market (required)"},
		&urfave.Int64Flag{Name: "twap-id", Usage: "active TWAP id (required)"},
		&urfave.StringFlag{Name: "intent-id", Usage: "caller-chosen idempotency id"},
		&urfave.DurationFlag{Name: "expires", Value: defaultIntentTTL, Usage: "intent lifetime (max 1h)"},
	}
}

// preparePerp resolves external metadata once and persists the approved plan.
// It never unlocks credentials or sends an action to the exchange.
func (e *env) preparePerp(fill func(*trading.Intent, *api.Market) error) (res *result, err error) {
	if len(e.args) != 0 {
		return nil, output.Errorf(output.CodeUsage, "unexpected arguments %v", e.args)
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	acct, err := e.tradeAccount(p)
	if err != nil {
		return nil, err
	}
	coin, err := normCoin(str(e, "market"))
	if err != nil {
		return nil, err
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	if !trading.ValidIntentID(id) {
		return nil, output.Errorf(output.CodeInvalidArgument, "invalid --intent-id")
	}
	ttl := defaultIntentTTL
	if e.cmd.IsSet("expires") {
		ttl = e.cmd.Duration("expires")
	}
	if ttl <= 0 || ttl > maxIntentTTL {
		return nil, output.Errorf(output.CodeInvalidArgument, "--expires must be between 1s and 1h")
	}
	m, err := e.market(p, coin)
	if err != nil {
		return nil, err
	}
	now := e.app.Now().UTC()
	in := trading.Intent{Version: 1, ID: id, Network: p.Network, Account: acct, APIURL: p.APIURL, Market: m.Coin, AssetID: m.AssetID, CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(ttl).Format(time.RFC3339)}
	if err := fill(&in, m); err != nil {
		return nil, err
	}
	var fee api.GetBuilderFeeStatusResponse
	if in.Kind == trading.KindOrder || in.Kind == trading.KindScale {
		if err := e.perpsRead(p, "GetBuilderFeeStatus", map[string]string{"address": acct}, &fee); err != nil {
			return nil, err
		}
		in.BuilderAddress = strings.ToLower(fee.BuilderAddress)
		in.MaxBuilderFee10 = fee.BuilderFeeChargedPpm / 10
	}
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := j.Close(); closeErr != nil {
			err = errors.Join(err, output.Wrap(output.CodeInternal, closeErr, "closing perp intent journal"))
		}
	}()
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	defer unlock()
	rec, _, err := j.Create(in)
	if errors.Is(err, journal.ErrConflict) {
		return nil, output.Errorf(output.CodeIntentConflict, "intent id %q already exists with a different payload", id)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	next := fmt.Sprintf("oku orders submit --intent %s --digest %s", rec.Intent.ID, rec.Digest)
	notes := []string{"preparing sends nothing to the exchange", "review every approved price, size, and condition before submitting"}
	if (in.Kind == trading.KindOrder || in.Kind == trading.KindScale) && !fee.BuilderFeeApproved {
		notes = append(notes, "Oku builder fee is not approved for this account")
	}
	raw, err := rec.Intent.Advanced.MarshalJSON()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "rendering approved order plan")
	}
	return &result{data: map[string]any{"intent_id": rec.Intent.ID, "digest": rec.Digest, "state": rec.State, "intent": rec.Intent, "notes": notes, "next": next}, human: func(w io.Writer) {
		fmt.Fprintf(w, "prepared %s on %s (%s), expires %s\n", in.Kind, in.Market, in.ID, in.ExpiresAt)
		fmt.Fprintf(w, "  account %s on %s, builder %s fee up to %d tenths bps\n", in.Account, in.Network, orDash(in.BuilderAddress), in.MaxBuilderFee10)
		fmt.Fprintf(w, "  approved plan: %s\n", raw)
		for _, note := range notes {
			fmt.Fprintf(w, "  note: %s\n", note)
		}
		fmt.Fprintf(w, "submit with: %s\n", next)
	}}, nil
}

func setAdvanced(in *trading.Intent, p trading.AdvancedPayload) error {
	a, err := trading.NewAdvanced(p)
	if err != nil {
		return output.Wrap(output.CodeInvalidArgument, err, "order parameters")
	}
	in.Advanced, in.Kind = a, a.Kind()
	return nil
}

func perpDecimal(e *env, name string, m *api.Market, size, required bool) (string, error) {
	raw := str(e, name)
	if raw == "" && !required {
		return "", nil
	}
	v, err := decimal.Canonical(raw)
	if err == nil && decimal.IsZero(v) {
		err = fmt.Errorf("must be greater than zero")
	}
	if err == nil {
		if size {
			err = decimal.ValidateSize(v, m.SzDecimals)
		} else {
			err = decimal.ValidatePerpPrice(v, m.SzDecimals)
		}
	}
	if err != nil {
		return "", output.Wrap(output.CodeInvalidDecimal, err, "--"+name)
	}
	return v, nil
}

func perpSide(e *env) (bool, error) {
	switch str(e, "side") {
	case "buy":
		return true, nil
	case "sell":
		return false, nil
	}
	return false, output.Errorf(output.CodeInvalidArgument, "--side must be buy or sell")
}

// Only fields meaningful for the selected variant may enter the intent.
func rejectPerpFlags(e *env, allowed ...string) error {
	allow := map[string]bool{}
	for _, name := range allowed {
		allow[name] = true
	}
	for _, f := range append(advancedOrderFlags(), &urfave.StringFlag{Name: "price"}, &urfave.StringFlag{Name: "tif"}, &urfave.IntFlag{Name: "slippage-bps"}) {
		name := f.Names()[0]
		if e.cmd.IsSet(name) && !allow[name] {
			return output.Errorf(output.CodeInvalidArgument, "--%s is not valid for --type %s", name, str(e, "type"))
		}
	}
	return nil
}

func (a *App) cmdAdvancedOrderPrepare(e *env) (*result, error) {
	return e.preparePerp(func(in *trading.Intent, m *api.Market) error {
		buy, err := perpSide(e)
		if err != nil {
			return err
		}
		size := "0"
		if !e.cmd.Bool("close-position") || (str(e, "size") != "" && str(e, "size") != "0") {
			size, err = perpDecimal(e, "size", m, true, true)
			if err != nil {
				return err
			}
		}
		if e.cmd.Bool("close-position") {
			size = "0"
		}
		in.Side, in.Size, in.OrderType, in.ReduceOnly = str(e, "side"), size, str(e, "type"), e.cmd.Bool("reduce-only")
		switch in.OrderType {
		case "limit", "market", "stop_market", "stop_limit", "take_market", "take_limit":
			return e.prepareOrderPlan(in, m, buy)
		case "twap":
			if err := rejectPerpFlags(e, "duration-minutes", "randomize", "trigger-price", "trigger-activate-above", "stop-price"); err != nil {
				return err
			}
			trigger, err := perpDecimal(e, "trigger-price", m, false, false)
			if err != nil {
				return err
			}
			stop, err := perpDecimal(e, "stop-price", m, false, false)
			if err != nil {
				return err
			}
			if trigger == "" && e.cmd.IsSet("trigger-activate-above") {
				return output.Errorf(output.CodeInvalidArgument, "--trigger-activate-above requires --trigger-price")
			}
			plan := trading.TWAPPlan{AssetID: m.AssetID, IsBuy: buy, Size: size, ReduceOnly: in.ReduceOnly, Minutes: e.cmd.Int("duration-minutes"), Randomize: e.cmd.Bool("randomize")}
			if trigger != "" || stop != "" {
				plan.Conditions = &trading.TWAPConditions{}
				if trigger != "" {
					plan.Conditions.Trigger = &trading.TWAPTrigger{Price: trigger, ActivateAbove: e.cmd.Bool("trigger-activate-above")}
				}
				if stop != "" {
					plan.Conditions.StopPrice = &stop
				}
			}
			return setAdvanced(in, plan)
		case "scale":
			if err := rejectPerpFlags(e, "start-price", "end-price", "total-orders", "size-skew", "tif"); err != nil {
				return err
			}
			start, err := perpDecimal(e, "start-price", m, false, true)
			if err != nil {
				return err
			}
			end, err := perpDecimal(e, "end-price", m, false, true)
			if err != nil {
				return err
			}
			skew, err := decimal.Canonical(str(e, "size-skew"))
			if err != nil {
				return output.Wrap(output.CodeInvalidDecimal, err, "--size-skew")
			}
			count, tif := e.cmd.Int("total-orders"), strings.ToUpper(str(e, "tif"))
			orders, err := trading.ScaleOrders(m.AssetID, buy, size, start, end, skew, count, m.SzDecimals, tif, in.ReduceOnly)
			if err != nil {
				return output.Wrap(output.CodeInvalidArgument, err, "scale ladder")
			}
			return setAdvanced(in, trading.ScalePlan{Orders: orders, StartPx: start, EndPx: end, TotalOrders: count, SizeSkew: skew, TIF: tif, SizeDecimals: m.SzDecimals})
		case "trailing_stop":
			if err := rejectPerpFlags(e, "activation-price", "retracement-pct", "retracement-price"); err != nil {
				return err
			}
			activation, err := perpDecimal(e, "activation-price", m, false, false)
			if err != nil {
				return err
			}
			pct, px := str(e, "retracement-pct"), str(e, "retracement-price")
			if (pct == "") == (px == "") {
				return output.Errorf(output.CodeInvalidArgument, "provide exactly one of --retracement-pct or --retracement-price")
			}
			var retracement trading.Retracement
			if pct != "" {
				pct, err = decimal.Canonical(pct)
				if err == nil {
					retracement, err = trading.NewPercentRetracement(pct)
				}
			} else {
				px, err = perpDecimal(e, "retracement-price", m, false, true)
				if err == nil {
					retracement, err = trading.NewPriceRetracement(px)
				}
			}
			if err != nil {
				return output.Wrap(output.CodeInvalidArgument, err, "retracement")
			}
			plan := trading.TrailingStopPlan{AssetID: m.AssetID, IsBuy: buy, Size: size, ReduceOnly: in.ReduceOnly, Retracement: retracement}
			if activation != "" {
				plan.ActivationPrice = &activation
			}
			return setAdvanced(in, plan)
		}
		return output.Errorf(output.CodeInvalidArgument, "unsupported --type %q", in.OrderType)
	})
}

func (a *App) cmdOrdersModify(e *env) (*result, error) {
	return e.preparePerp(func(in *trading.Intent, m *api.Market) error {
		oid := e.cmd.Int64("order-id")
		if oid <= 0 {
			return output.Errorf(output.CodeInvalidArgument, "--order-id must be positive")
		}
		target, err := e.positionModifyTarget(in, m, oid)
		if err != nil {
			return err
		}
		var buy bool
		var size string
		tpsl, market := str(e, "tpsl"), e.cmd.Bool("market-trigger")
		if target != nil {
			buy, size, tpsl, market = target.buy, target.size, target.tpsl, target.market
		} else {
			buy, err = perpSide(e)
			if err != nil {
				return err
			}
			size, err = perpDecimal(e, "size", m, true, true)
			if err != nil {
				return err
			}
		}
		trigger, err := perpDecimal(e, "trigger-price", m, false, target != nil)
		if err != nil {
			return err
		}
		var execution trading.Execution
		var price string
		reduce := e.cmd.Bool("reduce-only")
		if trigger != "" {
			if e.cmd.IsSet("reduce-only") && !reduce {
				return output.Errorf(output.CodeInvalidArgument, "trigger replacements are always reduce-only")
			}
			if e.cmd.IsSet("tif") {
				return output.Errorf(output.CodeInvalidArgument, "--tif applies only to plain limit replacements")
			}
			execution, err = trading.NewTriggerExecution(market, trigger, tpsl)
			if err != nil {
				return output.Wrap(output.CodeInvalidArgument, err, "trigger replacement requires --tpsl tp or sl")
			}
			reduce = true
			if market {
				if str(e, "price") != "" {
					return output.Errorf(output.CodeInvalidArgument, "--price is not allowed with --market-trigger")
				}
				price, err = trading.AggressivePrice(trigger, m.SzDecimals, buy, 1000)
			} else {
				price, err = perpDecimal(e, "price", m, false, true)
			}
		} else {
			if market || str(e, "tpsl") != "" {
				return output.Errorf(output.CodeInvalidArgument, "--market-trigger and --tpsl require --trigger-price")
			}
			execution, err = trading.NewLimitExecution(strings.ToUpper(str(e, "tif")))
			if err != nil {
				return output.Wrap(output.CodeInvalidArgument, err, "--tif")
			}
			price, err = perpDecimal(e, "price", m, false, true)
		}
		if err != nil {
			return err
		}
		in.Side = "sell"
		if buy {
			in.Side = "buy"
		}
		in.OrderID, in.Size, in.Price, in.ReduceOnly = oid, size, price, reduce
		return setAdvanced(in, trading.ModifyPlan{SizeDecimals: m.SzDecimals, PositionTpsl: target != nil, Modifies: []trading.ApprovedModify{{OrderID: oid, Order: trading.ApprovedOrder{AssetID: m.AssetID, IsBuy: buy, Price: price, Size: size, ReduceOnly: reduce, Execution: execution}}}})
	})
}

func (a *App) cmdOrdersCancelTWAP(e *env) (*result, error) {
	return e.preparePerp(func(in *trading.Intent, m *api.Market) error {
		return setAdvanced(in, trading.CancelTWAPPlan{AssetID: m.AssetID, TWAPID: e.cmd.Int64("twap-id")})
	})
}

func (a *App) cmdOrdersCancelMany(e *env) (*result, error) {
	r, err := e.preparePerp(func(in *trading.Intent, m *api.Market) error {
		ids := str(e, "order-ids")
		if ids != "" && e.cmd.Int64("order-id") != 0 {
			return output.Errorf(output.CodeInvalidArgument, "--order-id and --order-ids are exclusive")
		}
		if ids == "" {
			ids = strconv.FormatInt(e.cmd.Int64("order-id"), 10)
		}
		targets := []trading.CancelTarget{}
		seen := map[int64]bool{}
		for _, s := range strings.Split(ids, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			if err != nil || id <= 0 || seen[id] {
				return output.Errorf(output.CodeInvalidArgument, "--order-ids must contain distinct positive integer ids")
			}
			seen[id] = true
			targets = append(targets, trading.CancelTarget{AssetID: m.AssetID, OrderID: id})
		}
		return setAdvanced(in, trading.CancelManyPlan{Cancels: targets})
	})
	if err != nil || e.cmd.Bool("prepare") {
		return r, err
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	data := r.data.(map[string]any)
	return e.submit(p, data["intent_id"].(string), data["digest"].(string))
}
