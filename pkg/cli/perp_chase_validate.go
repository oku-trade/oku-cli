package cli

import (
	"errors"
	"fmt"

	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

// validateChaseChild runs at the signing boundary as well as before unlock.
// Ordinary intents do not incur any extra network request.
func (e *env) validateChaseChild(j *journal.Journal, rec *journal.Record) error {
	c, err := j.ChaseForChild(rec.Intent.ID)
	if errors.Is(err, journal.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.PendingIntent != rec.Intent.ID || c.State == journal.ChaseStopped {
		return errors.New("chase child is no longer pending")
	}
	parent, err := j.Get(c.IntentID)
	if err != nil {
		return err
	}
	if parent.Digest != c.Digest || parent.Intent.Digest() != c.Digest {
		return errors.New("chase parent digest mismatch")
	}
	stored, err := j.Get(rec.Intent.ID)
	if err != nil {
		return err
	}
	if stored.Digest != rec.Digest || rec.Intent.Digest() != rec.Digest {
		return errors.New("chase child digest mismatch")
	}
	cancel := rec.Intent.Kind == trading.KindCancelMany
	if !cancel && (parent.State == journal.StateAbandoned || parent.State == journal.StateRejected || parent.Intent.Expired(e.app.Now())) {
		return errors.New("chase parent is abandoned, rejected or expired")
	}
	if err = trading.ValidateChaseChild(&parent.Intent, &rec.Intent, c.OrderID, c.Remaining); err != nil {
		return err
	}
	if cancel {
		return nil
	}
	remaining := c.Remaining
	if c.OrderID > 0 {
		ctx, stop := e.requestCtx()
		snapshot, err := e.chaseOrder(ctx, &parent.Intent, c.OrderID)
		stop()
		if err != nil {
			return err
		}
		if trading.ClassifyChaseOrderStatus(snapshot.Status) != trading.ChaseOrderOpen {
			return errors.New("chase owned order is not open at signing")
		}
		var ok bool
		remaining, ok = trading.ResolveChaseRepriceSize(remaining, snapshot.Remaining, true)
		if !ok {
			return errors.New("chase remaining size unavailable at signing")
		}
		ctx, stop = e.requestCtx()
		filled, _, err := e.chaseFills(ctx, &parent.Intent, c)
		stop()
		if err != nil {
			return err
		}
		budget, err := chaseSubtract(parent.Intent.Size, filled)
		if err != nil {
			return err
		}
		br, _ := decimal.Rat(budget)
		rr, _ := decimal.Rat(remaining)
		if br.Cmp(rr) < 0 {
			remaining = budget
		}
	}
	if err = trading.ValidateChaseChild(&parent.Intent, &rec.Intent, c.OrderID, remaining); err != nil {
		return fmt.Errorf("chase changed before signing: %w", err)
	}
	return nil
}
