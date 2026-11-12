package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

type chaseRunner struct {
	e      *env
	p      *resolved
	j      *journal.Journal
	parent *journal.Record
	c      *journal.ChaseRecord
}

func (r *chaseRunner) unknown(err error) error {
	r.c.State = journal.ChaseUnknown
	r.c.Reason = "reconciliation-required"
	return errors.Join(err, r.j.PutChase(r.c))
}

// recoverChild only adopts a durable authoritative result. It never retries an
// action whose signature may have left the process.
func (r *chaseRunner) recoverChild() error {
	if r.c.PendingIntent == "" {
		return nil
	}
	child, err := r.j.Get(r.c.PendingIntent)
	if err != nil {
		return r.unknown(err)
	}
	switch child.State {
	case journal.StateUnknown, journal.StateSubmitting:
		if child.ActionID == "" {
			return r.unknown(errors.New("chase pending action has no action id"))
		}
		s, err := r.e.unlock(r.p, "read")
		if err != nil {
			return r.unknown(err)
		}
		client := r.e.client(r.p)
		s.apply(client)
		ctx, cancel := r.e.requestCtx()
		var response api.ReconcileActionsResponse
		err = client.Perps(ctx, "ReconcileActions", map[string]any{"actionIds": []string{child.ActionID}}, &response, true)
		cancel()
		if err != nil {
			return r.unknown(err)
		}
		found := false
		for _, a := range response.Actions {
			if a.ActionID == child.ActionID {
				found = true
				applyServerState(child, a.State, int64(a.HlOid), a.ErrorMessage)
				break
			}
		}
		if !found {
			return r.unknown(errors.New("chase pending action absent from reconciliation"))
		}
		if err = r.j.Put(child, "chase reconcile"); err != nil {
			return r.unknown(err)
		}
	case journal.StateSigning:
		// Another signing attempt may still be running. Standard intent reconcile
		// handles the age-gated reset of an interrupted signer.
		return r.unknown(errors.New("chase child may still be signing; run orders reconcile after five minutes"))
	}
	switch child.State {
	case journal.StatePrepared, journal.StateCreating, journal.StateCreated:
		child.State = journal.StateAbandoned
		if err = r.j.Put(child, "chase recovery abandoned unsigned child"); err != nil {
			return r.unknown(err)
		}
	case journal.StateRejected, journal.StateAbandoned:
	case journal.StateSubmitted:
		if child.Intent.Kind != trading.KindCancelMany {
			if child.OrderID <= 0 {
				return r.unknown(errors.New("successful chase action has no authoritative order id"))
			}
			r.c.OrderID = child.OrderID
			seen := false
			for _, oid := range r.c.OrderIDs {
				if oid == child.OrderID {
					seen = true
				}
			}
			if !seen {
				r.c.OrderIDs = append(r.c.OrderIDs, child.OrderID)
			}
		}
	default:
		return r.unknown(errors.New("chase child outcome remains unknown"))
	}
	r.c.PendingIntent = ""
	r.c.State = journal.ChaseReady
	if r.c.OrderID > 0 {
		r.c.State = journal.ChaseResting
	}
	return r.j.PutChase(r.c)
}

// refresh reconciles the owned oid and computes a remaining budget from both
// live size and authoritative fills across every oid this chase has owned.
func (r *chaseRunner) refresh() error {
	if r.c.OrderID == 0 {
		return nil
	}
	ctx, cancel := r.e.requestCtx()
	snapshot, err := r.e.chaseOrder(ctx, &r.parent.Intent, r.c.OrderID)
	cancel()
	if err != nil {
		return r.unknown(err)
	}
	outcome := trading.ClassifyChaseOrderStatus(snapshot.Status)
	if outcome == trading.ChaseOrderUnknown {
		return r.unknown(fmt.Errorf("unknown chase order status %q", snapshot.Status))
	}
	ctx, cancel = r.e.requestCtx()
	filled, average, err := r.e.chaseFills(ctx, &r.parent.Intent, r.c)
	cancel()
	if err != nil {
		return r.unknown(err)
	}
	budget, err := chaseSubtract(r.parent.Intent.Size, filled)
	if err != nil {
		return r.unknown(err)
	}
	r.c.FilledSize, r.c.AveragePrice = &filled, average
	if outcome == trading.ChaseOrderFilled {
		// Fill indexing can lag orderStatus. Do not finalize until the complete
		// approved quantity is accounted for by owned fills.
		if !decimal.IsZero(budget) {
			return r.unknown(errors.New("filled status and chase fill quantity disagree"))
		}
		r.c.Remaining = "0"
		r.c.CancelledRemainder = &r.c.Remaining
		r.c.State = journal.ChaseStopped
		r.c.Reason = "filled"
	} else if outcome == trading.ChaseOrderGone {
		r.c.Remaining = snapshot.Remaining
		r.c.CancelledRemainder = &snapshot.Remaining
		if budget != snapshot.Remaining {
			// Cancellation is authoritative even while fills are still
			// indexing. Omit incomplete execution statistics rather than
			// reporting a guessed fill quantity or average.
			r.c.FilledSize, r.c.AveragePrice = nil, nil
		}
		r.c.State = journal.ChaseStopped
		r.c.Reason = "cancelled"
	} else {
		remaining, ok := trading.ResolveChaseRepriceSize(r.c.Remaining, snapshot.Remaining, true)
		if !ok {
			return r.unknown(errors.New("open chase order has no authoritative remaining size"))
		}
		rr, _ := decimal.Rat(remaining)
		br, _ := decimal.Rat(budget)
		if br.Cmp(rr) != 0 {
			r.c.FilledSize, r.c.AveragePrice = nil, nil
		}
		if br.Cmp(rr) < 0 {
			remaining = budget
		}
		if decimal.IsZero(remaining) {
			return r.unknown(errors.New("open order exceeds remaining chase budget"))
		}
		r.c.Remaining, r.c.Price, r.c.State = remaining, snapshot.Price, journal.ChaseResting
	}
	return r.j.PutChase(r.c)
}

func (r *chaseRunner) child(plan trading.AdvancedPayload, cancel bool) (trading.Intent, error) {
	parent := r.parent.Intent
	now := r.e.app.Now().UTC()
	in := trading.Intent{Version: 1, ID: randomID(), Network: parent.Network, Account: parent.Account, APIURL: parent.APIURL, Market: parent.Market, AssetID: parent.AssetID, Side: parent.Side, Size: r.c.Remaining, ReduceOnly: parent.ReduceOnly, BuilderAddress: parent.BuilderAddress, MaxBuilderFee10: parent.MaxBuilderFee10, CreatedAt: now.Format(time.RFC3339), ExpiresAt: parent.ExpiresAt}
	if cancel {
		in.ExpiresAt = now.Add(defaultIntentTTL).Format(time.RFC3339)
	}
	if err := setAdvanced(&in, plan); err != nil {
		return in, err
	}
	if err := trading.ValidateChaseChild(&parent, &in, r.c.OrderID, r.c.Remaining); err != nil {
		return in, err
	}
	return in, nil
}

func (r *chaseRunner) act(plan trading.AdvancedPayload, cancel bool) error {
	in, err := r.child(plan, cancel)
	if err != nil {
		return err
	}
	rec, err := r.j.CreateChaseChild(r.c, in)
	if err != nil {
		return err
	}
	_, submitErr := r.e.submit(r.p, in.ID, rec.Digest)
	recoveryErr := r.recoverChild()
	if recoveryErr != nil {
		return errors.Join(submitErr, recoveryErr)
	}
	return submitErr
}

func (r *chaseRunner) stop(reason string) error {
	if r.c.State == journal.ChaseStopped {
		return nil
	}
	if err := r.recoverChild(); err != nil {
		return err
	}
	if r.c.OrderID == 0 {
		r.c.State = journal.ChaseStopped
		r.c.Reason = reason
		return r.j.PutChase(r.c)
	}
	refreshErr := r.refresh()
	if r.c.State == journal.ChaseStopped {
		return nil
	}
	plan := trading.CancelManyPlan{Cancels: []trading.CancelTarget{{AssetID: r.parent.Intent.AssetID, OrderID: r.c.OrderID}}}
	if err := r.act(plan, true); err != nil {
		return errors.Join(refreshErr, err)
	}
	if err := r.refresh(); err != nil {
		return err
	}
	if r.c.State != journal.ChaseStopped {
		return r.unknown(errors.New("chase cancellation not yet authoritative"))
	}
	if r.c.Reason != "filled" {
		r.c.Reason = reason
		return r.j.PutChase(r.c)
	}
	return nil
}

func (r *chaseRunner) run(ctx context.Context) error {
	if r.c.State == journal.ChaseStopped {
		return nil
	}
	if err := r.recoverChild(); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if r.parent.Intent.Expired(r.e.app.Now()) {
			return r.stop("expired")
		}
		if err := r.refresh(); err != nil {
			return err
		}
		if r.c.State == journal.ChaseStopped {
			return nil
		}
		requestCtx, cancel := r.e.requestCtx()
		bid, ask, err := r.e.chaseBook(requestCtx, r.parent.Intent.Market)
		cancel()
		if err != nil && !errors.Is(err, errChaseBookUnavailable) {
			return err
		}
		p := r.parent.Intent.Chase
		decision := trading.DecideChase(trading.ChaseDecisionInput{Side: p.Side, BestBid: bid, BestAsk: ask, SizeDecimals: p.SizeDecimals, CurrentPx: r.c.Price, ReferencePx: p.ReferencePx, MaxDistance: p.MaxDistance, WorstPx: p.WorstPx})
		r.c.Reason = decision.Reason
		switch decision.Kind {
		case "stop":
			return r.stop(decision.Reason)
		case "place", "reprice":
			execution, err := trading.NewLimitExecution("ALO")
			if err != nil {
				return err
			}
			order := trading.ApprovedOrder{AssetID: p.AssetID, IsBuy: p.Side == "buy", Price: decision.TargetPx, Size: r.c.Remaining, ReduceOnly: p.ReduceOnly, Execution: execution}
			var plan trading.AdvancedPayload = trading.OrderPlan{Grouping: "na", Orders: []trading.ApprovedOrder{order}, SizeDecimals: p.SizeDecimals}
			if r.c.OrderID > 0 {
				plan = trading.ModifyPlan{Modifies: []trading.ApprovedModify{{OrderID: r.c.OrderID, Order: order}}, SizeDecimals: p.SizeDecimals}
			}
			if err = r.act(plan, false); err != nil {
				// Only a definite venue rejection is retryable. Transport and signing
				// failures remain errors even if their message mentions a maker race.
				if r.c.PendingIntent != "" {
					return err
				}
				oe := output.As(err)
				if oe.Code != output.CodeRejected {
					return err
				}
				if trading.IsOrderGoneError(err.Error()) {
					if err = r.refresh(); err != nil {
						return err
					}
					if r.c.State == journal.ChaseStopped {
						return nil
					}
				} else if trading.IsMinNotionalError(err.Error()) {
					return r.stop("minimum-notional")
				} else if !trading.IsRetriableChaseError(err.Error()) {
					return err
				}
			}
		}
		if err = r.e.chaseEvent(r.c); err != nil {
			return err
		}
		if err = r.e.app.Sleep(ctx, time.Second); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}
