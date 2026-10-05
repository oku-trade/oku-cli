// Package trading defines order intents and validates server-built
// Hyperliquid actions against them before anything is signed.
package trading

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/signing"
)

// Kinds of intents.
const (
	KindOrder  = "order"
	KindCancel = "cancel"
	// KindLeverage changes a market's leverage and cross/isolated mode
	// (Hyperliquid updateLeverage). It opens no position by itself.
	KindLeverage = "leverage"
	// KindMargin adds (positive) or removes (negative) isolated margin on an
	// existing isolated position (Hyperliquid updateIsolatedMargin).
	KindMargin = "margin"
)

// MarginDecimals is the fixed-point scale of updateIsolatedMargin ntli.
const MarginDecimals = 6

var intentIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{2,63}$`)

// ValidIntentID reports whether id is an acceptable intent identifier.
func ValidIntentID(id string) bool { return intentIDRE.MatchString(id) }

// Intent is the exact economic request the user/agent approved. Its digest
// binds every field; any change requires a new intent.
type Intent struct {
	Version   int    `json:"version"`
	ID        string `json:"intent_id"`
	Kind      string `json:"kind"`
	Network   string `json:"network"`
	Account   string `json:"account"`
	APIURL    string `json:"api_url"`
	Market    string `json:"market"`
	AssetID   int    `json:"asset_id"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`

	// Order fields.
	Side        string `json:"side,omitempty"`       // buy | sell
	OrderType   string `json:"order_type,omitempty"` // limit | market
	Size        string `json:"size,omitempty"`
	Price       string `json:"price,omitempty"` // limit price
	TIF         string `json:"tif,omitempty"`   // GTC | IOC | ALO
	ReduceOnly  bool   `json:"reduce_only,omitempty"`
	SlippageBps int    `json:"slippage_bps,omitempty"`
	// ReferencePx and WorstPx bound market orders. The server computes its
	// own aggressive price from the live mid; the signed price must not be
	// worse than WorstPx.
	ReferencePx string `json:"reference_px,omitempty"`
	WorstPx     string `json:"worst_px,omitempty"`
	// Builder fee policy observed at prepare time.
	BuilderAddress  string `json:"builder_address,omitempty"`
	MaxBuilderFee10 int    `json:"max_builder_fee_tenths_bps"`

	// Cancel fields.
	OrderID int64 `json:"order_id,omitempty"`

	// Leverage fields.
	Leverage int  `json:"leverage,omitempty"`
	IsCross  bool `json:"is_cross,omitempty"`

	// Margin fields: canonical signed USDC decimal, at most 6 decimals.
	MarginAmount string `json:"margin_amount,omitempty"`
}

// Digest is sha256 over the canonical JSON encoding of the intent.
func (in *Intent) Digest() string {
	raw, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Expired reports whether the intent is past its expiry.
func (in *Intent) Expired(now time.Time) bool {
	t, err := time.Parse(time.RFC3339, in.ExpiresAt)
	return err != nil || !now.Before(t)
}

// IsBuy reports order direction.
func (in *Intent) IsBuy() bool { return in.Side == "buy" }

// hlTIF maps CLI TIF to Hyperliquid wire TIF (activity_build.go buildOrderTypeSpec).
func (in *Intent) hlTIF() string {
	if in.OrderType == "market" {
		return "Ioc"
	}
	switch in.TIF {
	case "IOC":
		return "Ioc"
	case "ALO":
		return "Alo"
	}
	return "Gtc"
}

// WorstPrice returns reference*(1±bps/10000) truncated toward the safe side
// to at most maxDecimals decimals. It is a bound, not the order price.
func WorstPrice(reference string, isBuy bool, bps int, maxDecimals int) (string, error) {
	if bps <= 0 || bps >= 10000 {
		return "", errors.New("slippage bps must be between 1 and 9999")
	}
	ref, ok := decimal.Rat(reference)
	if !ok || ref.Sign() <= 0 {
		return "", fmt.Errorf("invalid reference price %q", reference)
	}
	mult := int64(10000 - bps)
	if isBuy {
		mult = int64(10000 + bps)
	}
	v := new(big.Rat).Mul(ref, big.NewRat(mult, 10000))
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(maxDecimals)), nil)
	num := new(big.Int).Mul(v.Num(), scale)
	q, r := new(big.Int).QuoRem(num, v.Denom(), new(big.Int))
	// Round toward the reference (buy down, sell up) so the bound is never
	// looser than the user's slippage tolerance.
	if !isBuy && r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.Sign() <= 0 {
		return "", errors.New("slippage bound is not representable at market precision")
	}
	s := new(big.Rat).SetFrac(q, scale).FloatString(maxDecimals)
	return decimal.Canonical(s)
}

// wire types for validation; unknown fields are rejected.
type wireOrderAction struct {
	Type     string      `json:"type"`
	Orders   []wireOrder `json:"orders"`
	Grouping string      `json:"grouping"`
	Builder  *struct {
		B string `json:"b"`
		F int    `json:"f"`
	} `json:"builder,omitempty"`
}

type wireOrder struct {
	A int    `json:"a"`
	B bool   `json:"b"`
	P string `json:"p"`
	S string `json:"s"`
	R bool   `json:"r"`
	T struct {
		Limit *struct {
			TIF string `json:"tif"`
		} `json:"limit,omitempty"`
		Trigger json.RawMessage `json:"trigger,omitempty"`
	} `json:"t"`
	C string `json:"c,omitempty"`
}

type wireCancelAction struct {
	Type    string `json:"type"`
	Cancels []struct {
		A int   `json:"a"`
		O int64 `json:"o"`
	} `json:"cancels"`
}

var cloidRE = regexp.MustCompile(`^0x[0-9a-f]{32}$`)

// Mismatch is returned when the server action does not match the intent.
type Mismatch struct{ Reason string }

func (m *Mismatch) Error() string { return "server action does not match intent: " + m.Reason }

func mismatch(format string, a ...any) error { return &Mismatch{Reason: fmt.Sprintf(format, a...)} }

func strictUnmarshal(actionJSON string, v any) error {
	dec := json.NewDecoder(strings.NewReader(actionJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return mismatch("unexpected action shape: %v", err)
	}
	if dec.More() {
		return mismatch("trailing data in action")
	}
	return nil
}

// ValidateAction checks a server-built actionJson against the intent. Only
// the order, cancel, updateLeverage and updateIsolatedMargin families are
// accepted, each only for the matching intent kind; anything else
// (transfers, approvals, vault/subaccount routing) is rejected.
// It returns the client order id when present.
func ValidateAction(in *Intent, actionJSON string) (string, error) {
	// The exact bytes will be msgpack-encoded for signing. Reject anything
	// the encoder would treat ambiguously (duplicate keys, floats, unsafe
	// integers) before interpreting the economics.
	if _, err := signing.EncodeActionJSON(actionJSON); err != nil {
		return "", mismatch("action json rejected: %v", err)
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(actionJSON), &head); err != nil {
		return "", mismatch("action is not a JSON object")
	}
	// Exact, case-sensitive keys on the very bytes that get signed, before
	// any case-insensitive struct decoding below.
	if err := checkExactShape(head.Type, actionJSON); err != nil {
		return "", err
	}
	switch in.Kind {
	case KindOrder:
		if head.Type != "order" {
			return "", mismatch("action type %q, want order", head.Type)
		}
		return validateOrder(in, actionJSON)
	case KindCancel:
		if head.Type != "cancel" {
			return "", mismatch("action type %q, want cancel", head.Type)
		}
		var a wireCancelAction
		if err := strictUnmarshal(actionJSON, &a); err != nil {
			return "", err
		}
		if len(a.Cancels) != 1 || a.Cancels[0].A != in.AssetID || a.Cancels[0].O != in.OrderID {
			return "", mismatch("cancel target differs from intent")
		}
		return "", nil
	case KindLeverage:
		if head.Type != "updateLeverage" {
			return "", mismatch("action type %q, want updateLeverage", head.Type)
		}
		var a wireLeverageAction
		if err := strictUnmarshal(actionJSON, &a); err != nil {
			return "", err
		}
		switch {
		case a.Asset == nil || *a.Asset != in.AssetID:
			return "", mismatch("leverage asset differs from intent")
		case a.IsCross == nil || *a.IsCross != in.IsCross:
			return "", mismatch("cross/isolated mode differs from intent")
		case a.Leverage == nil || *a.Leverage != in.Leverage || in.Leverage < 1:
			return "", mismatch("leverage differs from intent")
		}
		return "", nil
	case KindMargin:
		if head.Type != "updateIsolatedMargin" {
			return "", mismatch("action type %q, want updateIsolatedMargin", head.Type)
		}
		var a wireMarginAction
		if err := strictUnmarshal(actionJSON, &a); err != nil {
			return "", err
		}
		want, err := MarginUnits(in.MarginAmount)
		if err != nil {
			return "", mismatch("intent margin amount: %v", err)
		}
		switch {
		case a.Asset == nil || *a.Asset != in.AssetID:
			return "", mismatch("margin asset differs from intent")
		case a.IsBuy == nil || !*a.IsBuy:
			// The backend always builds isBuy=true; Hyperliquid ignores it
			// for isolated margin, but anything else is unexpected.
			return "", mismatch("unexpected isBuy in margin action")
		case a.Ntli == nil || *a.Ntli != want:
			return "", mismatch("margin amount differs from intent")
		}
		return "", nil
	}
	return "", mismatch("unsupported intent kind %q", in.Kind)
}

// wire shapes; pointer fields make missing keys detectable.
type wireLeverageAction struct {
	Type     string `json:"type"`
	Asset    *int   `json:"asset"`
	IsCross  *bool  `json:"isCross"`
	Leverage *int   `json:"leverage"`
}

type wireMarginAction struct {
	Type  string `json:"type"`
	Asset *int   `json:"asset"`
	IsBuy *bool  `json:"isBuy"`
	Ntli  *int64 `json:"ntli"`
}

// MarginUnits converts a canonical signed decimal USDC amount to exact
// 6-decimal integer units. It refuses zero, non-canonical input, more than
// six decimals (no rounding) and values outside the JS-safe integer range.
func MarginUnits(amount string) (int64, error) {
	neg := strings.HasPrefix(amount, "-")
	abs := strings.TrimPrefix(amount, "-")
	if !decimal.IsCanonical(abs) || (neg && abs == "0") {
		return 0, fmt.Errorf("amount %q is not a canonical decimal", amount)
	}
	if decimal.IsZero(abs) {
		return 0, errors.New("amount must be non-zero")
	}
	if decimal.Decimals(abs) > MarginDecimals {
		return 0, fmt.Errorf("amount %q has more than %d decimals", amount, MarginDecimals)
	}
	r, _ := decimal.Rat(abs)
	r.Mul(r, big.NewRat(1_000_000, 1))
	if !r.IsInt() || !r.Num().IsInt64() || r.Num().Int64() > 1<<53-1 {
		return 0, fmt.Errorf("amount %q out of range", amount)
	}
	v := r.Num().Int64()
	if neg {
		v = -v
	}
	return v, nil
}

// ValidateLeverage checks a leverage request against market metadata
// before anything is sent: integer in [1, maxLeverage], and cross mode is
// refused for isolated-only markets.
func ValidateLeverage(leverage, maxLeverage int, isCross, onlyIsolated bool) error {
	if maxLeverage < 1 {
		return errors.New("market has no leverage limit; refusing")
	}
	if leverage < 1 || leverage > maxLeverage {
		return fmt.Errorf("leverage %d outside [1, %d]", leverage, maxLeverage)
	}
	if isCross && onlyIsolated {
		return errors.New("market is isolated-only; cross margin not allowed")
	}
	return nil
}

func validateOrder(in *Intent, actionJSON string) (string, error) {
	var a wireOrderAction
	if err := strictUnmarshal(actionJSON, &a); err != nil {
		return "", err
	}
	if a.Grouping != "na" {
		return "", mismatch("grouping %q, want na", a.Grouping)
	}
	if len(a.Orders) != 1 {
		return "", mismatch("expected exactly one order, got %d", len(a.Orders))
	}
	o := a.Orders[0]
	switch {
	case o.A != in.AssetID:
		return "", mismatch("asset %d, want %d", o.A, in.AssetID)
	case o.B != in.IsBuy():
		return "", mismatch("side differs")
	case o.S != in.Size:
		return "", mismatch("size %q, want %q", o.S, in.Size)
	case o.R != in.ReduceOnly:
		return "", mismatch("reduce_only differs")
	case o.T.Trigger != nil || o.T.Limit == nil:
		return "", mismatch("order type is not a plain limit order")
	case o.T.Limit.TIF != in.hlTIF():
		return "", mismatch("tif %q, want %q", o.T.Limit.TIF, in.hlTIF())
	case !decimal.IsCanonical(o.P) || !decimal.IsCanonical(o.S):
		return "", mismatch("non-canonical decimal in action")
	}
	if o.C != "" && !cloidRE.MatchString(o.C) {
		return "", mismatch("invalid client order id %q", o.C)
	}
	if err := checkPrice(in, o.P); err != nil {
		return "", err
	}
	if a.Builder != nil {
		if !strings.EqualFold(a.Builder.B, in.BuilderAddress) || in.BuilderAddress == "" {
			return "", mismatch("builder %q is not the builder observed at prepare", a.Builder.B)
		}
		if a.Builder.F < 0 || a.Builder.F > in.MaxBuilderFee10 {
			return "", mismatch("builder fee %d exceeds prepared maximum %d", a.Builder.F, in.MaxBuilderFee10)
		}
	}
	return o.C, nil
}

func checkPrice(in *Intent, p string) error {
	if in.OrderType == "limit" {
		if p != in.Price {
			return mismatch("price %q, want %q", p, in.Price)
		}
		return nil
	}
	px, ok1 := decimal.Rat(p)
	worst, ok2 := decimal.Rat(in.WorstPx)
	if !ok1 || !ok2 || px.Sign() <= 0 {
		return mismatch("invalid market price bound")
	}
	if in.IsBuy() && px.Cmp(worst) > 0 {
		return mismatch("buy price %s above worst allowed %s (market moved; prepare again)", p, in.WorstPx)
	}
	if !in.IsBuy() && px.Cmp(worst) < 0 {
		return mismatch("sell price %s below worst allowed %s (market moved; prepare again)", p, in.WorstPx)
	}
	return nil
}
