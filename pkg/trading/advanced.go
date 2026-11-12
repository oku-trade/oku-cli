package trading

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/decimal"
)

const (
	KindModify       = "modify"
	KindTWAP         = "twap"
	KindCancelTWAP   = "cancel_twap"
	KindScale        = "scale"
	KindTrailingStop = "trailing_stop"
	KindCancelMany   = "cancel_many"
)

// AdvancedPayload is a closed union of approved perpetual action plans.
// Values enter through NewAdvanced or JSON decoding, both of which validate.
type AdvancedPayload interface{ advancedKind() string }

// Advanced persists a tagged, validated action plan without interface decoding ambiguity.
type Advanced struct{ payload AdvancedPayload }

func (a *Advanced) Payload() AdvancedPayload { return a.payload }
func (a *Advanced) Kind() string             { return a.payload.advancedKind() }

// NewAdvanced validates and snapshots the payload so caller-owned slices cannot
// mutate the approved economics after construction.
func NewAdvanced(payload AdvancedPayload) (*Advanced, error) {
	a := &Advanced{payload: payload}
	if err := a.validate(); err != nil {
		return nil, err
	}
	raw, err := a.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var snapshot Advanced
	if err := snapshot.UnmarshalJSON(raw); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (a Advanced) MarshalJSON() ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type    string          `json:"type"`
		Payload AdvancedPayload `json:"payload"`
	}{a.Kind(), a.payload})
}

func (a *Advanced) UnmarshalJSON(raw []byte) error {
	var envelope struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := decodePlan(raw, &envelope); err != nil {
		return err
	}
	var payload AdvancedPayload
	switch envelope.Type {
	case KindOrder:
		var p OrderPlan
		if err := decodePlan(envelope.Payload, &p); err != nil {
			return err
		}
		payload = p
	case KindModify:
		var p ModifyPlan
		if err := decodePlan(envelope.Payload, &p); err != nil {
			return err
		}
		payload = p
	case KindTWAP:
		var p TWAPPlan
		if err := decodePlan(envelope.Payload, &p); err != nil {
			return err
		}
		payload = p
	case KindCancelTWAP:
		var p CancelTWAPPlan
		if err := decodePlan(envelope.Payload, &p); err != nil {
			return err
		}
		payload = p
	case KindScale:
		var p ScalePlan
		if err := decodePlan(envelope.Payload, &p); err != nil {
			return err
		}
		payload = p
	case KindTrailingStop:
		var p TrailingStopPlan
		if err := decodePlan(envelope.Payload, &p); err != nil {
			return err
		}
		payload = p
	case KindCancelMany:
		var p CancelManyPlan
		if err := decodePlan(envelope.Payload, &p); err != nil {
			return err
		}
		payload = p
	default:
		return fmt.Errorf("unsupported advanced intent %q", envelope.Type)
	}
	next := Advanced{payload: payload}
	if err := next.validate(); err != nil {
		return err
	}
	*a = next
	return nil
}

func decodePlan(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid approved plan: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing approved plan data")
	}
	return nil
}

// Execution has exactly one private variant. Its zero value is invalid.
type Execution struct {
	limit   *limitExecution
	trigger *triggerExecution
}
type limitExecution struct {
	TIF string `json:"tif"`
}
type triggerExecution struct {
	IsMarket  bool   `json:"isMarket"`
	TriggerPx string `json:"triggerPx"`
	TPSL      string `json:"tpsl"`
}

func NewLimitExecution(tif string) (Execution, error) {
	switch tif {
	case "GTC":
		tif = "Gtc"
	case "IOC":
		tif = "Ioc"
	case "ALO":
		tif = "Alo"
	}
	e := Execution{limit: &limitExecution{TIF: tif}}
	return e, e.validate()
}
func NewTriggerExecution(isMarket bool, triggerPx, tpsl string) (Execution, error) {
	e := Execution{trigger: &triggerExecution{IsMarket: isMarket, TriggerPx: triggerPx, TPSL: tpsl}}
	return e, e.validate()
}
func (e Execution) IsTrigger() bool { return e.trigger != nil }
func (e Execution) LimitTIF() string {
	if e.limit == nil {
		return ""
	}
	return e.limit.TIF
}
func (e Execution) Trigger() (isMarket bool, triggerPx, tpsl string, ok bool) {
	if e.trigger == nil {
		return false, "", "", false
	}
	return e.trigger.IsMarket, e.trigger.TriggerPx, e.trigger.TPSL, true
}
func (e Execution) MarshalJSON() ([]byte, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	if e.limit != nil {
		return json.Marshal(struct {
			Limit *limitExecution `json:"limit"`
		}{e.limit})
	}
	return json.Marshal(struct {
		Trigger *triggerExecution `json:"trigger"`
	}{e.trigger})
}
func (e *Execution) UnmarshalJSON(raw []byte) error {
	var v struct {
		Limit   *limitExecution   `json:"limit"`
		Trigger *triggerExecution `json:"trigger"`
	}
	if err := decodePlan(raw, &v); err != nil {
		return err
	}
	next := Execution{limit: v.Limit, trigger: v.Trigger}
	if err := next.validate(); err != nil {
		return err
	}
	*e = next
	return nil
}
func (e Execution) validate() error {
	if (e.limit == nil) == (e.trigger == nil) {
		return fmt.Errorf("execution requires exactly one limit or trigger variant")
	}
	if e.limit != nil {
		switch e.limit.TIF {
		case "Gtc", "Ioc", "Alo":
			return nil
		}
		return fmt.Errorf("invalid time in force")
	}
	if !positive(e.trigger.TriggerPx) || (e.trigger.TPSL != "tp" && e.trigger.TPSL != "sl") {
		return fmt.Errorf("invalid trigger execution")
	}
	return nil
}

// ApprovedOrder binds the exact venue price, including market-style trigger
// slippage, rather than allowing the backend to choose an unapproved bound.
type ApprovedOrder struct {
	AssetID    int       `json:"a"`
	IsBuy      bool      `json:"b"`
	Price      string    `json:"p"`
	Size       string    `json:"s"`
	ReduceOnly bool      `json:"r"`
	Execution  Execution `json:"t"`
	Cloid      string    `json:"c,omitempty"`
}

type OrderPlan struct {
	Grouping      string          `json:"grouping"`
	Orders        []ApprovedOrder `json:"orders"`
	SlippageBps   int             `json:"slippage_bps,omitempty"`
	ClosePosition bool            `json:"close_position,omitempty"`
	SizeDecimals  int             `json:"size_decimals"`
}

func (OrderPlan) advancedKind() string { return KindOrder }

type ApprovedModify struct {
	OrderID int64         `json:"oid"`
	Order   ApprovedOrder `json:"order"`
}
type ModifyPlan struct {
	Modifies     []ApprovedModify `json:"modifies"`
	AlwaysPlace  bool             `json:"always_place,omitempty"`
	SizeDecimals int              `json:"size_decimals"`
	PositionTpsl bool             `json:"position_tpsl,omitempty"`
}

func (ModifyPlan) advancedKind() string { return KindModify }

type TWAPTrigger struct {
	Price         string `json:"p"`
	ActivateAbove bool   `json:"a"`
}
type TWAPConditions struct {
	Trigger   *TWAPTrigger `json:"t"`
	StopPrice *string      `json:"s"`
}
type TWAPPlan struct {
	AssetID    int             `json:"a"`
	IsBuy      bool            `json:"b"`
	Size       string          `json:"s"`
	ReduceOnly bool            `json:"r"`
	Minutes    int             `json:"m"`
	Randomize  bool            `json:"t"`
	Conditions *TWAPConditions `json:"conditions,omitempty"`
}

func (TWAPPlan) advancedKind() string { return KindTWAP }

type CancelTWAPPlan struct {
	AssetID int   `json:"a"`
	TWAPID  int64 `json:"t"`
}

func (CancelTWAPPlan) advancedKind() string { return KindCancelTWAP }

type ScalePlan struct {
	Orders       []ApprovedOrder `json:"orders"`
	StartPx      string          `json:"start_px"`
	EndPx        string          `json:"end_px"`
	TotalOrders  int             `json:"total_orders"`
	SizeSkew     string          `json:"size_skew"`
	TIF          string          `json:"tif"`
	SizeDecimals int             `json:"size_decimals"`
}

func (ScalePlan) advancedKind() string { return KindScale }

// Retracement is an exclusive percent or price-distance variant.
type Retracement struct {
	pct string
	px  string
}

func NewPercentRetracement(pct string) (Retracement, error) {
	r, ok := decimal.Rat(pct)
	if !ok || !positive(pct) || r.Cmp(big.NewRat(100, 1)) >= 0 || decimal.Decimals(pct) > 4 {
		return Retracement{}, fmt.Errorf("retracement percent must be canonical, between 0 and 100, at most four decimals")
	}
	return Retracement{pct: r.FloatString(4) + "%"}, nil
}
func NewPriceRetracement(px string) (Retracement, error) {
	if !positive(px) {
		return Retracement{}, fmt.Errorf("invalid retracement price")
	}
	return Retracement{px: px}, nil
}
func (r Retracement) Percent() string {
	if r.pct == "" {
		return ""
	}
	p, err := decimal.Canonical(strings.TrimSuffix(r.pct, "%"))
	if err != nil {
		panic(err)
	}
	return p
}
func (r Retracement) Price() string { return r.px }
func (r Retracement) MarshalJSON() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if r.pct != "" {
		return json.Marshal(struct {
			Pct string `json:"pct"`
		}{r.pct})
	}
	return json.Marshal(struct {
		Px string `json:"px"`
	}{r.px})
}
func (r *Retracement) UnmarshalJSON(raw []byte) error {
	var v struct {
		Pct string `json:"pct"`
		Px  string `json:"px"`
	}
	if err := decodePlan(raw, &v); err != nil {
		return err
	}
	next := Retracement{pct: v.Pct, px: v.Px}
	if err := next.validate(); err != nil {
		return err
	}
	*r = next
	return nil
}

type TrailingStopPlan struct {
	AssetID         int         `json:"asset"`
	IsBuy           bool        `json:"isBuy"`
	Size            string      `json:"sz"`
	ReduceOnly      bool        `json:"reduceOnly"`
	Retracement     Retracement `json:"retracement"`
	ActivationPrice *string     `json:"activationPx"`
}

func (TrailingStopPlan) advancedKind() string { return KindTrailingStop }

type CancelTarget struct {
	AssetID int   `json:"a"`
	OrderID int64 `json:"o"`
}
type CancelManyPlan struct {
	Cancels []CancelTarget `json:"cancels"`
}

func (CancelManyPlan) advancedKind() string { return KindCancelMany }

func positive(s string) bool { return decimal.IsCanonical(s) && !decimal.IsZero(s) }
