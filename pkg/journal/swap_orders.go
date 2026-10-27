package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

// Swap order states: active -> triggered -> executing -> filled|failed. A
// triggered order returns to active only when preparing the sell failed
// before anything was journaled or signed. Once executing (a sell intent is
// bound) it never sells again. Only an active order can be cancelled.
const (
	SwapOrderActive    = "active"
	SwapOrderTriggered = "triggered"
	SwapOrderExecuting = "executing"
	SwapOrderFilled    = "filled"
	SwapOrderFailed    = "failed"
	SwapOrderCancelled = "cancelled"
)

// SwapOrderStates lists every swap order state in lifecycle order.
var SwapOrderStates = []string{SwapOrderActive, SwapOrderTriggered, SwapOrderExecuting, SwapOrderFilled, SwapOrderFailed, SwapOrderCancelled}

// SwapOrder is a client-side exit (token -> native) for an EVM swap position.
// Values are native-denominated decimals; percentages are decimal strings.
type SwapOrder struct {
	ID              string `json:"order_id"`
	Agent           string `json:"agent"`
	ChainID         uint64 `json:"chain_id"`
	Token           string `json:"token"`
	Decimals        int    `json:"decimals"`
	Amount          string `json:"amount"`
	Size            string `json:"size"`
	TakeProfitPct   string `json:"take_profit_pct,omitempty"`
	StopLossPct     string `json:"stop_loss_pct,omitempty"`
	TrailingStopPct string `json:"trailing_stop_pct,omitempty"`
	SlippageBps     int    `json:"slippage_bps"`
	Basis           string `json:"basis,omitempty"`
	BasisSource     string `json:"basis_source"`
	State           string `json:"state"`
	PeakValue       string `json:"peak_value,omitempty"`
	LastPrice       string `json:"last_price,omitempty"`
	LastValue       string `json:"last_value,omitempty"`
	LastQuoteAt     string `json:"last_quote_at,omitempty"`
	Trigger         string `json:"trigger,omitempty"`
	SellIntentID    string `json:"sell_intent_id,omitempty"`
	NeedsReconcile  bool   `json:"needs_reconcile"`
	LastError       string `json:"last_error,omitempty"`
	Attempts        int    `json:"attempts"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// SwapOrderPatch sets optional fields during a state transition.
type SwapOrderPatch struct {
	Trigger        *string
	SellIntentID   *string
	LastError      *string
	NeedsReconcile *bool
	// CountAttempt increments the failed-sell-attempt counter.
	CountAttempt bool
}

const swapOrderColumns = `order_id,agent,chain_id,token,decimals,amount,size,take_profit_pct,stop_loss_pct,trailing_stop_pct,slippage_bps,basis,basis_source,state,peak_value,last_price,last_value,last_quote_at,trigger,sell_intent_id,needs_reconcile,last_error,attempts,created_at,updated_at`

func scanSwapOrder(row interface{ Scan(...any) error }) (*SwapOrder, error) {
	o := new(SwapOrder)
	var reconcile int
	err := row.Scan(&o.ID, &o.Agent, &o.ChainID, &o.Token, &o.Decimals, &o.Amount, &o.Size, &o.TakeProfitPct, &o.StopLossPct, &o.TrailingStopPct, &o.SlippageBps, &o.Basis, &o.BasisSource, &o.State, &o.PeakValue, &o.LastPrice, &o.LastValue, &o.LastQuoteAt, &o.Trigger, &o.SellIntentID, &reconcile, &o.LastError, &o.Attempts, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	o.NeedsReconcile = reconcile != 0
	return o, nil
}

// CreateSwapOrder stores a new active order.
func (j *Journal) CreateSwapOrder(o SwapOrder) (*SwapOrder, error) {
	if !trading.ValidIntentID(o.ID) || o.Agent == "" || o.ChainID == 0 || o.Token == "" || o.Amount == "" || o.Size == "" {
		return nil, errors.New("invalid swap order")
	}
	if o.TakeProfitPct == "" && o.StopLossPct == "" && o.TrailingStopPct == "" {
		return nil, errors.New("swap order needs a take-profit, stop-loss or trailing stop")
	}
	now := j.now().UTC().Format(time.RFC3339Nano)
	o.State, o.CreatedAt, o.UpdatedAt = SwapOrderActive, now, now
	o.Agent = strings.ToLower(o.Agent)
	o.Trigger, o.SellIntentID, o.NeedsReconcile, o.LastError, o.Attempts = "", "", false, "", 0
	_, err := j.db.Exec(`INSERT INTO swap_orders(profile,`+swapOrderColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.binding.Profile, o.ID, o.Agent, o.ChainID, o.Token, o.Decimals, o.Amount, o.Size, o.TakeProfitPct, o.StopLossPct, o.TrailingStopPct, o.SlippageBps, o.Basis, o.BasisSource, o.State, o.PeakValue, o.LastPrice, o.LastValue, o.LastQuoteAt, o.Trigger, o.SellIntentID, 0, o.LastError, 0, o.CreatedAt, o.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrConflict
		}
		return nil, err
	}
	return &o, nil
}

// GetSwapOrder loads one order of this profile.
func (j *Journal) GetSwapOrder(id string) (*SwapOrder, error) {
	o, err := scanSwapOrder(j.db.QueryRow(`SELECT `+swapOrderColumns+` FROM swap_orders WHERE profile=? AND order_id=?`, j.binding.Profile, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// ListSwapOrders returns this profile's orders, oldest first, optionally
// restricted to the given states.
func (j *Journal) ListSwapOrders(states ...string) ([]*SwapOrder, error) {
	q := `SELECT ` + swapOrderColumns + ` FROM swap_orders WHERE profile=?`
	args := []any{j.binding.Profile}
	if len(states) > 0 {
		q += ` AND state IN (?` + strings.Repeat(",?", len(states)-1) + `)`
		for _, s := range states {
			args = append(args, s)
		}
	}
	rows, err := j.db.Query(q+` ORDER BY created_at,order_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SwapOrder{}
	for rows.Next() {
		o, err := scanSwapOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// TransitionSwapOrder moves an order from one state to another only if it is
// still in from (compare-and-set). It returns ErrConflict otherwise, which is
// how a second watcher or a cancel racing a trigger loses.
func (j *Journal) TransitionSwapOrder(id, from, to string, p SwapOrderPatch) error {
	if !validSwapOrderTransition(from, to) {
		return fmt.Errorf("%w: swap order %s -> %s", ErrConflict, from, to)
	}
	tx, err := j.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := scanSwapOrder(tx.QueryRow(`SELECT `+swapOrderColumns+` FROM swap_orders WHERE profile=? AND order_id=?`, j.binding.Profile, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if o.State != from {
		return fmt.Errorf("%w: swap order %s is %s, not %s", ErrConflict, id, o.State, from)
	}
	if p.Trigger != nil {
		o.Trigger = *p.Trigger
	}
	if p.SellIntentID != nil {
		o.SellIntentID = *p.SellIntentID
	}
	if p.LastError != nil {
		o.LastError = *p.LastError
	}
	if p.NeedsReconcile != nil {
		o.NeedsReconcile = *p.NeedsReconcile
	}
	if p.CountAttempt {
		o.Attempts++
	}
	reconcile := 0
	if o.NeedsReconcile {
		reconcile = 1
	}
	res, err := tx.Exec(`UPDATE swap_orders SET state=?,trigger=?,sell_intent_id=?,last_error=?,needs_reconcile=?,attempts=?,updated_at=? WHERE profile=? AND order_id=? AND state=?`, to, o.Trigger, o.SellIntentID, o.LastError, reconcile, o.Attempts, j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id, from)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func validSwapOrderTransition(from, to string) bool {
	switch from {
	case SwapOrderActive:
		return to == SwapOrderTriggered || to == SwapOrderCancelled
	case SwapOrderTriggered:
		return to == SwapOrderExecuting || to == SwapOrderFailed || to == SwapOrderActive
	case SwapOrderExecuting:
		return to == SwapOrderFilled || to == SwapOrderFailed
	}
	return false
}

// RecordSwapOrderQuote stores the latest price observation of an active order
// and ratchets the peak value: it only ever increases. It returns the stored
// peak. A non-active order is left unchanged (ErrConflict).
func (j *Journal) RecordSwapOrderQuote(id, price, value, at string) (string, error) {
	v, ok := new(big.Rat).SetString(value)
	if !ok || v.Sign() < 0 {
		return "", fmt.Errorf("invalid value %q", value)
	}
	tx, err := j.begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var state, peak string
	err = tx.QueryRow(`SELECT state,peak_value FROM swap_orders WHERE profile=? AND order_id=?`, j.binding.Profile, id).Scan(&state, &peak)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if state != SwapOrderActive {
		return "", ErrConflict
	}
	if old, ok := new(big.Rat).SetString(peak); !ok || v.Cmp(old) > 0 {
		peak = value
	}
	if _, err = tx.Exec(`UPDATE swap_orders SET peak_value=?,last_price=?,last_value=?,last_quote_at=?,updated_at=? WHERE profile=? AND order_id=? AND state='active'`, peak, price, value, at, j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id); err != nil {
		return "", err
	}
	return peak, tx.Commit()
}

// LockSwapOrderWatch takes the per-profile watcher lock without waiting, so
// at most one 'swap orders watch' evaluates and sells this profile's orders.
func (j *Journal) LockSwapOrderWatch() (func(), error) {
	return config.TryLockPath(filepath.Join(j.base, "swap-orders-"+j.binding.Profile+".lock"))
}

// ListOnchainSwaps returns confirmed swap records of an agent on one chain.
func (j *Journal) ListOnchainSwaps(agent string, chain uint64) ([]*OnchainRecord, error) {
	rows, err := j.db.Query(`SELECT intent_json FROM onchain_intents WHERE profile=? AND kind='swap' AND state='confirmed' AND agent=? AND chain_id=? ORDER BY created_at,intent_id`, j.binding.Profile, strings.ToLower(agent), chain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OnchainRecord{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		r := &OnchainRecord{State: "confirmed"}
		if err = json.Unmarshal([]byte(raw), &r.OnchainIntent); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
