package trading

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/decimal"
)

const KindChase = "chase"

// ChasePlan approves a client-side ALO policy, not a native exchange action.
// WorstPx is always finite. MaxDistance additionally limits absolute drift from
// ReferencePx in either direction. PriceTick records the reference-price grid.
type ChasePlan struct {
	AssetID      int    `json:"asset_id"`
	Side         string `json:"side"`
	TotalSize    string `json:"total_size"`
	ReduceOnly   bool   `json:"reduce_only"`
	ReferencePx  string `json:"reference_px"`
	WorstPx      string `json:"worst_px"`
	MaxDistance  string `json:"max_distance,omitempty"`
	PriceTick    string `json:"price_tick"`
	SizeDecimals int    `json:"size_decimals"`
}

// NewChase fills derived policy fields and validates the approved economics.
// An omitted leash permits the whole precision-valid positive price range,
// capped at the maximum exactly representable integer for buys.
func NewChase(p ChasePlan) (*ChasePlan, error) {
	tick, err := ChasePriceTick(p.ReferencePx, p.SizeDecimals)
	if err != nil {
		return nil, err
	}
	if p.PriceTick == "" {
		p.PriceTick = tick
	}
	if p.WorstPx == "" {
		p.WorstPx, err = ChaseWorstPrice(p.Side, p.ReferencePx, p.MaxDistance, p.SizeDecimals)
		if err != nil {
			return nil, err
		}
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// ChaseWorstPrice computes an adverse absolute-distance bound, rounded inward
// so price precision cannot widen the approved distance.
func ChaseWorstPrice(side, reference, distance string, sizeDecimals int) (string, error) {
	if side != "buy" && side != "sell" {
		return "", fmt.Errorf("invalid chase side")
	}
	if !positive(reference) || sizeDecimals < 0 || sizeDecimals > 6 {
		return "", fmt.Errorf("invalid chase reference or precision")
	}
	if distance == "" {
		if side == "buy" {
			return "9007199254740991", nil
		}
		return decimalStep(6 - sizeDecimals), nil
	}
	if !positive(distance) {
		return "", fmt.Errorf("chase distance must be positive and canonical")
	}
	ref, _ := decimal.Rat(reference)
	d, _ := decimal.Rat(distance)
	bound := new(big.Rat)
	if side == "buy" {
		bound.Add(ref, d)
	} else {
		bound.Sub(ref, d)
	}
	if bound.Sign() <= 0 {
		return "", fmt.Errorf("chase bound must be positive")
	}
	for places := 6 - sizeDecimals; places >= 0; places-- {
		px, err := roundToward(bound, places, side == "sell")
		if err == nil && decimal.ValidatePerpPrice(px, sizeDecimals) == nil {
			return px, nil
		}
	}
	return "", fmt.Errorf("chase bound is not representable")
}

func (p ChasePlan) Validate() error {
	if !validAsset(p.AssetID) || (p.Side != "buy" && p.Side != "sell") || p.SizeDecimals < 0 || p.SizeDecimals > 6 {
		return fmt.Errorf("invalid chase asset, side or precision")
	}
	if !positive(p.TotalSize) || decimal.ValidateSize(p.TotalSize, p.SizeDecimals) != nil {
		return fmt.Errorf("invalid chase total size")
	}
	if !positive(p.ReferencePx) || !positive(p.WorstPx) {
		return fmt.Errorf("chase requires positive canonical reference and worst prices")
	}
	if err := decimal.ValidatePerpPrice(p.ReferencePx, p.SizeDecimals); err != nil {
		return err
	}
	if err := decimal.ValidatePerpPrice(p.WorstPx, p.SizeDecimals); err != nil {
		return err
	}
	tick, err := ChasePriceTick(p.ReferencePx, p.SizeDecimals)
	if err != nil || tick != p.PriceTick {
		return fmt.Errorf("chase reference tick differs from precision")
	}
	ref, _ := decimal.Rat(p.ReferencePx)
	worst, _ := decimal.Rat(p.WorstPx)
	if (p.Side == "buy" && worst.Cmp(ref) < 0) || (p.Side == "sell" && worst.Cmp(ref) > 0) {
		return fmt.Errorf("chase worst price excludes reference")
	}
	if p.MaxDistance != "" {
		if !positive(p.MaxDistance) || ExceedsMaxDistance(p.ReferencePx, p.WorstPx, p.MaxDistance) {
			return fmt.Errorf("chase worst price exceeds distance")
		}
	}
	return nil
}

func (p ChasePlan) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type plain ChasePlan
	return json.Marshal(plain(p))
}
func (p *ChasePlan) UnmarshalJSON(raw []byte) error {
	type plain ChasePlan
	var next plain
	if err := decodePlan(raw, &next); err != nil {
		return err
	}
	if err := ChasePlan(next).Validate(); err != nil {
		return err
	}
	*p = ChasePlan(next)
	return nil
}

// ValidateChaseChild authorizes a native exact plan against one approved parent.
// ownedOID must come from reconciled chase state. Zero permits initial placement
// only, and then remaining must equal TotalSize. Ambiguous outcomes must be
// reconciled by the engine before it requests another placement.
// An exact owned-order cancellation may use a fresh expiry for crash recovery.
func ValidateChaseChild(parent, child *Intent, ownedOID int64, remaining string) error {
	if parent == nil || parent.Kind != KindChase || parent.Chase == nil || parent.Advanced != nil {
		return mismatch("missing chase parent policy")
	}
	p := parent.Chase
	if err := p.Validate(); err != nil {
		return mismatch("invalid chase policy: %v", err)
	}
	if child == nil || child.Chase != nil || child.Advanced == nil {
		return mismatch("chase child requires a native exact plan")
	}
	if child.Network != parent.Network || child.Account != parent.Account || child.APIURL != parent.APIURL || child.Market != parent.Market || child.AssetID != p.AssetID || parent.AssetID != p.AssetID || child.BuilderAddress != parent.BuilderAddress || child.MaxBuilderFee10 != parent.MaxBuilderFee10 {
		return mismatch("chase child routing or fee policy differs")
	}
	if !decimal.IsCanonical(remaining) || decimal.Decimals(remaining) > p.SizeDecimals {
		return mismatch("invalid chase remaining size")
	}
	rem, _ := decimal.Rat(remaining)
	total, _ := decimal.Rat(p.TotalSize)
	if rem.Cmp(total) > 0 || ownedOID < 0 || ownedOID > maxSafeID {
		return mismatch("chase remaining or owned order is out of range")
	}
	if err := child.Advanced.validate(); err != nil {
		return mismatch("invalid chase child: %v", err)
	}
	if child.Kind != child.Advanced.Kind() {
		return mismatch("chase child kind differs from native plan")
	}
	var order ApprovedOrder
	switch plan := child.Advanced.Payload().(type) {
	case OrderPlan:
		if ownedOID != 0 || remaining != p.TotalSize || plan.Grouping != "na" || len(plan.Orders) != 1 || plan.ClosePosition || plan.SlippageBps != 0 || plan.SizeDecimals != p.SizeDecimals {
			return mismatch("chase placement must be a single initial plain limit")
		}
		order = plan.Orders[0]
	case ModifyPlan:
		if ownedOID <= 0 || plan.AlwaysPlace || len(plan.Modifies) != 1 || plan.Modifies[0].OrderID != ownedOID || plan.SizeDecimals != p.SizeDecimals {
			return mismatch("chase modification must target its owned order only")
		}
		order = plan.Modifies[0].Order
	case CancelManyPlan:
		if ownedOID <= 0 || len(plan.Cancels) != 1 || plan.Cancels[0] != (CancelTarget{AssetID: p.AssetID, OrderID: ownedOID}) {
			return mismatch("chase cancellation must target its owned order only")
		}
		return nil
	default:
		return mismatch("unsupported chase child plan")
	}
	if child.ExpiresAt != parent.ExpiresAt {
		return mismatch("chase child expiry differs from parent")
	}
	if order.AssetID != p.AssetID || order.IsBuy != (p.Side == "buy") || order.ReduceOnly != p.ReduceOnly || order.Execution.IsTrigger() || order.Execution.LimitTIF() != "Alo" {
		return mismatch("chase child must match asset, side, reduce-only and ALO execution")
	}
	size, _ := decimal.Rat(order.Size)
	if rem.Sign() <= 0 || size.Cmp(rem) > 0 {
		return mismatch("chase child exceeds remaining size")
	}
	px, _ := decimal.Rat(order.Price)
	worst, _ := decimal.Rat(p.WorstPx)
	if (p.Side == "buy" && px.Cmp(worst) > 0) || (p.Side == "sell" && px.Cmp(worst) < 0) {
		return mismatch("chase child exceeds approved worst price")
	}
	if p.MaxDistance != "" && ExceedsMaxDistance(p.ReferencePx, order.Price, p.MaxDistance) {
		return mismatch("chase child exceeds absolute distance leash")
	}
	return nil
}

// ValidateChaseAction validates both delegated authority and the exact bytes
// passed to the native signing validator. The parent itself is never signable.
func ValidateChaseAction(parent, child *Intent, ownedOID int64, remaining, actionJSON string) (string, error) {
	if err := ValidateChaseChild(parent, child, ownedOID, remaining); err != nil {
		return "", err
	}
	return ValidateAction(child, actionJSON)
}

func decimalStep(places int) string {
	if places == 0 {
		return "1"
	}
	return "0." + strings.Repeat("0", places-1) + "1"
}

// ChasePriceTick matches the venue's magnitude-dependent five-significant-figure
// grid, with integer prices always allowed.
func ChasePriceTick(reference string, sizeDecimals int) (string, error) {
	if !positive(reference) || sizeDecimals < 0 || sizeDecimals > 6 {
		return "", fmt.Errorf("invalid chase price or precision")
	}
	whole, fraction, _ := strings.Cut(reference, ".")
	places := 6 - sizeDecimals
	if whole != "0" {
		places = min(places, max(0, 5-len(whole)))
	} else {
		leading := len(fraction) - len(strings.TrimLeft(fraction, "0"))
		places = min(places, leading+5)
	}
	return decimalStep(places), nil
}

// ComputeChaseTargetPx returns one tick inside the touch without crossing.
// Missing, locked, crossed or off-grid books cause the caller to hold.
func ComputeChaseTargetPx(side, bid, ask string, sizeDecimals int) (string, error) {
	if side != "buy" && side != "sell" {
		return "", fmt.Errorf("invalid chase side")
	}
	if !positive(bid) || !positive(ask) || sizeDecimals < 0 || sizeDecimals > 6 {
		return "", fmt.Errorf("book unavailable")
	}
	if decimal.ValidatePerpPrice(bid, sizeDecimals) != nil || decimal.ValidatePerpPrice(ask, sizeDecimals) != nil {
		return "", fmt.Errorf("book is off grid")
	}
	b, _ := decimal.Rat(bid)
	a, _ := decimal.Rat(ask)
	if a.Cmp(b) <= 0 {
		return "", fmt.Errorf("book is locked or crossed")
	}
	reference := bid
	if side == "sell" {
		reference = ask
	}
	tickString, _ := ChasePriceTick(reference, sizeDecimals)
	tick, _ := decimal.Rat(tickString)
	target, _ := decimal.Rat(reference)
	spread := new(big.Rat).Sub(a, b)
	if spread.Cmp(tick) > 0 {
		if side == "buy" {
			target.Add(target, tick)
		} else {
			target.Sub(target, tick)
		}
	}
	// Crossing a power-of-ten boundary changes the price grid. Round away from
	// the opposing touch, then check maker safety again.
	px, err := roundToward(target, 6-sizeDecimals, side == "sell")
	for places := 6 - sizeDecimals; err == nil && decimal.ValidatePerpPrice(px, sizeDecimals) != nil && places > 0; {
		places--
		px, err = roundToward(target, places, side == "sell")
	}
	if err != nil {
		return "", err
	}
	rounded, _ := decimal.Rat(px)
	if (side == "buy" && rounded.Cmp(a) >= 0) || (side == "sell" && rounded.Cmp(b) <= 0) {
		return reference, nil
	}
	return px, nil
}

func ShouldReprice(currentPx, targetPx string) bool {
	current, ok := decimal.Rat(currentPx)
	target, valid := decimal.Rat(targetPx)
	return !ok || !valid || current.Cmp(target) != 0
}

// ExceedsMaxDistance fails closed on malformed input and uses an absolute leash.
func ExceedsMaxDistance(entryPx, targetPx, maxDistance string) bool {
	if !positive(entryPx) || !positive(targetPx) || !positive(maxDistance) {
		return true
	}
	entry, _ := decimal.Rat(entryPx)
	target, _ := decimal.Rat(targetPx)
	distance, _ := decimal.Rat(maxDistance)
	return new(big.Rat).Abs(new(big.Rat).Sub(target, entry)).Cmp(distance) > 0
}

// ResolveChaseRepriceSize trusts only snapshots requested after the owned oid
// was minted. A missing oid is ambiguous, never evidence that nothing filled.
func ResolveChaseRepriceSize(trackedRemaining, liveSize string, snapshotRequestedAfterOID bool) (string, bool) {
	if !snapshotRequestedAfterOID || !positive(liveSize) || !positive(trackedRemaining) {
		return "", false
	}
	tracked, _ := decimal.Rat(trackedRemaining)
	live, _ := decimal.Rat(liveSize)
	if live.Cmp(tracked) < 0 {
		return liveSize, true
	}
	return trackedRemaining, true
}

func IsOrderGoneError(message string) bool {
	return strings.Contains(strings.ToLower(message), "canceled or filled")
}
func IsMinNotionalError(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "minimum value") || strings.Contains(message, "minimum notional")
}
func IsRetriableChaseError(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "post only") || strings.Contains(message, "immediately match") || strings.Contains(message, "not found")
}

type ChaseOrderOutcome string

const (
	ChaseOrderFilled  ChaseOrderOutcome = "filled"
	ChaseOrderGone    ChaseOrderOutcome = "gone"
	ChaseOrderOpen    ChaseOrderOutcome = "open"
	ChaseOrderUnknown ChaseOrderOutcome = "unknown"
)

// ClassifyChaseOrderStatus only marks a fill when the venue explicitly reports
// filled. Unknown statuses require reconciliation rather than a guessed fill.
func ClassifyChaseOrderStatus(status string) ChaseOrderOutcome {
	status = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.ToLower(status))
	if status == "filled" {
		return ChaseOrderFilled
	}
	if status == "open" || status == "triggered" {
		return ChaseOrderOpen
	}
	if status == "scheduledcancel" || strings.HasSuffix(status, "canceled") || strings.HasSuffix(status, "cancelled") || strings.HasSuffix(status, "rejected") {
		return ChaseOrderGone
	}
	return ChaseOrderUnknown
}

type ChaseDecisionInput struct {
	Side         string
	BestBid      string
	BestAsk      string
	SizeDecimals int
	CurrentPx    string
	ReferencePx  string
	MaxDistance  string
	WorstPx      string
}
type ChaseDecision struct {
	Kind     string `json:"kind"`
	TargetPx string `json:"target_px,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func DecideChase(in ChaseDecisionInput) ChaseDecision {
	target, err := ComputeChaseTargetPx(in.Side, in.BestBid, in.BestAsk, in.SizeDecimals)
	if err != nil {
		return ChaseDecision{Kind: "hold", Reason: "book-unavailable"}
	}
	if in.MaxDistance != "" && ExceedsMaxDistance(in.ReferencePx, target, in.MaxDistance) {
		return ChaseDecision{Kind: "stop", Reason: "max-distance"}
	}
	if in.WorstPx != "" {
		worst, ok := decimal.Rat(in.WorstPx)
		px, _ := decimal.Rat(target)
		if !ok || worst.Sign() <= 0 || (in.Side == "buy" && px.Cmp(worst) > 0) || (in.Side == "sell" && px.Cmp(worst) < 0) {
			return ChaseDecision{Kind: "stop", Reason: "worst-price"}
		}
	}
	if in.CurrentPx == "" {
		return ChaseDecision{Kind: "place", TargetPx: target}
	}
	if ShouldReprice(in.CurrentPx, target) {
		return ChaseDecision{Kind: "reprice", TargetPx: target}
	}
	return ChaseDecision{Kind: "hold", TargetPx: target}
}
