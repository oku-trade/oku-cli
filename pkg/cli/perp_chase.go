package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/trading"
	urfave "github.com/urfave/cli/v3"
)

func chaseFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "market", Usage: "market to chase (prepare only)"},
		&urfave.StringFlag{Name: "side", Usage: "buy or sell (prepare only)"},
		&urfave.StringFlag{Name: "size", Usage: "total base size (prepare only)"},
		&urfave.BoolFlag{Name: "reduce-only", Usage: "only reduce a position"},
		&urfave.StringFlag{Name: "max-distance", Usage: "absolute price-distance leash from the prepared reference"},
		&urfave.StringFlag{Name: "intent-id", Usage: "idempotency id for preparation"},
		&urfave.DurationFlag{Name: "expires", Value: defaultIntentTTL, Usage: "approved chase lifetime (maximum 1h)"},
		&urfave.StringFlag{Name: "intent", Usage: "prepared chase intent to approve and run"},
		&urfave.StringFlag{Name: "digest", Usage: "exact prepared digest to approve"},
		&urfave.StringFlag{Name: "resume", Usage: "recover and resume a previously approved chase"},
	}
}

func (a *App) cmdOrdersChase(e *env) (_ *result, err error) {
	if len(e.args) != 0 {
		return nil, output.Errorf(output.CodeUsage, "unexpected chase arguments")
	}
	if e.g.inProcess {
		return nil, output.Errorf(output.CodeUsage, "chase requires a foreground CLI process")
	}
	id, resume := str(e, "intent"), str(e, "resume")
	if id != "" && resume != "" {
		return nil, output.Errorf(output.CodeUsage, "--intent and --resume are exclusive")
	}
	if id == "" && resume == "" {
		if str(e, "digest") != "" {
			return nil, output.Errorf(output.CodeUsage, "--digest requires --intent")
		}
		return e.prepareChase()
	}
	for _, name := range []string{"market", "side", "size", "reduce-only", "max-distance", "intent-id", "expires"} {
		if e.cmd.IsSet(name) {
			return nil, output.Errorf(output.CodeUsage, "--%s is prepare-only", name)
		}
	}
	if resume != "" {
		if str(e, "digest") != "" {
			return nil, output.Errorf(output.CodeUsage, "--resume does not accept --digest")
		}
		id = resume
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if _, err = e.tradeAccount(p); err != nil {
		return nil, err
	}
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer closeChaseJournal(j, &err)
	unlock, err := j.LockChase(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	parent, err := j.Get(id)
	if err != nil {
		return nil, err
	}
	if err = chaseParentBinding(parent, p); err != nil {
		return nil, err
	}
	if parent.State == journal.StateAbandoned || parent.State == journal.StateRejected {
		return nil, output.Errorf(output.CodeIntentState, "chase parent is %s", parent.State)
	}
	var c *journal.ChaseRecord
	if resume != "" {
		c, err = j.GetChase(id)
	} else {
		if str(e, "digest") == "" || parent.Digest != str(e, "digest") {
			return nil, output.Errorf(output.CodePayloadMismatch, "exact chase --digest is required")
		}
		c, err = j.CreateChase(parent)
	}
	if err != nil {
		return nil, err
	}
	if c.Digest != parent.Digest {
		return nil, journal.ErrConflict
	}
	runner := chaseRunner{e: e, p: p, j: j, parent: parent, c: c}
	ctx, stop := signal.NotifyContext(e.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	childEnv := *e
	childEnv.ctx = ctx
	runner.e = &childEnv
	err = runner.run(ctx)
	// Cleanup has its own deadline. The interrupted request context must never
	// suppress the cancellation that removes the resting order.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cleanupEnv := *e
	cleanupEnv.ctx = cleanupCtx
	runner.e = &cleanupEnv
	cleanupErr := runner.stop("interrupted")
	if cleanupErr == nil && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		err = nil
	}
	if err != nil || cleanupErr != nil {
		return nil, output.As(errors.Join(err, cleanupErr)).WithDetail("chase", c)
	}
	return chaseResult(c), nil
}

func chaseParentBinding(r *journal.Record, p *resolved) error {
	in := &r.Intent
	if in.Digest() != r.Digest {
		return output.Errorf(output.CodePayloadMismatch, "chase parent digest mismatch")
	}
	if in.Kind != trading.KindChase || in.Chase == nil || in.Advanced != nil {
		return output.Errorf(output.CodeIntentState, "intent is not a chase")
	}
	if in.Network != p.Network || !strings.EqualFold(in.Account, p.Account) || in.APIURL != p.APIURL {
		return output.Errorf(output.CodeConfigError, "chase belongs to a different profile binding")
	}
	if in.Size != in.Chase.TotalSize || in.Side != in.Chase.Side || in.ReduceOnly != in.Chase.ReduceOnly || in.AssetID != in.Chase.AssetID {
		return output.Errorf(output.CodePayloadMismatch, "chase parent economics differ from policy")
	}
	return in.Chase.Validate()
}

func (e *env) prepareChase() (_ *result, err error) {
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	if !trading.ValidIntentID(id) {
		return nil, output.Errorf(output.CodeInvalidArgument, "invalid --intent-id")
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	account, err := e.tradeAccount(p)
	if err != nil {
		return nil, err
	}
	coin, err := normCoin(str(e, "market"))
	if err != nil {
		return nil, err
	}
	buy, err := perpSide(e)
	if err != nil {
		return nil, err
	}
	m, err := e.market(p, coin)
	if err != nil {
		return nil, err
	}
	size, err := perpDecimal(e, "size", m, true, true)
	if err != nil {
		return nil, err
	}
	distance := str(e, "max-distance")
	if distance != "" {
		distance, err = chaseDecimal(distance, false)
		if err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "--max-distance")
		}
	}
	ttl := e.cmd.Duration("expires")
	if ttl <= 0 || ttl > maxIntentTTL {
		return nil, output.Errorf(output.CodeInvalidArgument, "--expires must be positive and at most 1h")
	}
	ctx, cancel := e.requestCtx()
	bid, ask, err := e.chaseBook(ctx, m.Coin)
	cancel()
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "chase book")
	}
	side := "sell"
	if buy {
		side = "buy"
	}
	reference, err := trading.ComputeChaseTargetPx(side, bid, ask, m.SzDecimals)
	if err != nil {
		return nil, err
	}
	plan, err := trading.NewChase(trading.ChasePlan{AssetID: m.AssetID, Side: side, TotalSize: size, ReduceOnly: e.cmd.Bool("reduce-only"), ReferencePx: reference, MaxDistance: distance, SizeDecimals: m.SzDecimals})
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "chase policy")
	}
	var fee api.GetBuilderFeeStatusResponse
	if err = e.perpsRead(p, "GetBuilderFeeStatus", map[string]string{"address": account}, &fee); err != nil {
		return nil, err
	}
	now := e.app.Now().UTC()
	in := trading.Intent{Version: 1, ID: id, Kind: trading.KindChase, Network: p.Network, Account: account, APIURL: p.APIURL, Market: m.Coin, AssetID: m.AssetID, Side: side, Size: size, ReduceOnly: plan.ReduceOnly, OrderType: "chase", ReferencePx: reference, WorstPx: plan.WorstPx, Chase: plan, BuilderAddress: strings.ToLower(fee.BuilderAddress), MaxBuilderFee10: fee.BuilderFeeChargedPpm / 10, CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(ttl).Format(time.RFC3339)}
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer closeChaseJournal(j, &err)
	rec, _, err := j.Create(in)
	if err != nil {
		return nil, err
	}
	next := fmt.Sprintf("oku orders chase --intent %s --digest %s", id, rec.Digest)
	notes := []string{"preparing signs and submits nothing", "keep this process running while chasing; interrupting cancels its resting order", "BBO source: Hyperliquid HTTP info l2Book"}
	if distance == "" {
		notes = append(notes, "no distance leash: tracking is limited only by the representable worst price and expiry, not a user price tolerance")
	}
	return &result{data: map[string]any{"intent_id": id, "digest": rec.Digest, "intent": in, "state": rec.State, "next": next, "notes": notes}, human: func(w io.Writer) {
		fmt.Fprintf(w, "prepared chase %s %s %s, worst price %s, expires %s\nrun: %s\n", side, size, m.Coin, plan.WorstPx, in.ExpiresAt, next)
		fmt.Fprintf(w, "  account: %s on %s, reduce-only: %t\n  reference: %s, max distance: %s\n  builder: %s, fee cap: %d tenths bps\n", account, p.Network, in.ReduceOnly, reference, orDash(distance), orDash(in.BuilderAddress), in.MaxBuilderFee10)
		for _, note := range notes {
			fmt.Fprintln(w, "  "+note)
		}
	}}, nil
}

func chaseResult(c *journal.ChaseRecord) *result {
	return &result{data: c, human: func(w io.Writer) {
		fmt.Fprintf(w, "chase %s: %s (%s)\n", c.IntentID, c.State, c.Reason)
		if c.FilledSize != nil {
			fmt.Fprintf(w, "  filled size: %s\n", *c.FilledSize)
		} else {
			fmt.Fprintln(w, "  filled size: unavailable")
		}
		if c.AveragePrice != nil {
			fmt.Fprintf(w, "  average price: %s\n", *c.AveragePrice)
		} else {
			fmt.Fprintln(w, "  average price: unavailable")
		}
		if c.CancelledRemainder != nil {
			fmt.Fprintf(w, "  cancelled remainder: %s\n", *c.CancelledRemainder)
		} else {
			fmt.Fprintln(w, "  cancelled remainder: unavailable")
		}
	}}
}

func (e *env) chaseEvent(c *journal.ChaseRecord) error {
	if !e.stream {
		return nil
	}
	if err := output.WriteLine(e.app.Stdout, output.Envelope{RequestID: e.reqID, Command: e.name, Event: "progress", OK: true, Data: c}); err != nil {
		return fmt.Errorf("writing chase progress: %w", err)
	}
	return nil
}

// reconcileChases cancels recovered resting orders instead of starting a new
// foreground owner. Active foreground owners keep their own exclusive lock.
// Chases sent through a different API than the profile's are returned as
// skipped, like ordinary intents, instead of failing the whole reconcile.
func (e *env) reconcileChases(p *resolved) (_ []*journal.ChaseRecord, skipped []map[string]string, err error) {
	j, err := openJournal(p)
	if err != nil {
		return nil, nil, err
	}
	defer closeChaseJournal(j, &err)
	chases, err := j.ListChases()
	if err != nil {
		return nil, nil, err
	}
	var failures []error
	for i, c := range chases {
		if c.State == journal.ChaseStopped {
			continue
		}
		unlock, lockErr := j.LockChase(c.IntentID)
		if errors.Is(lockErr, config.ErrLocked) {
			continue
		}
		if lockErr != nil {
			failures = append(failures, lockErr)
			continue
		}
		// The lock may have changed hands after the list query.
		fresh, getErr := j.GetChase(c.IntentID)
		if getErr != nil {
			unlock()
			failures = append(failures, getErr)
			continue
		}
		chases[i] = fresh
		parent, getErr := j.Get(c.IntentID)
		if getErr == nil && parent.Intent.APIURL != p.APIURL {
			skipped = append(skipped, map[string]string{"intent_id": c.IntentID, "api_url": parent.Intent.APIURL})
			chases[i] = nil
			unlock()
			continue
		}
		if getErr == nil {
			getErr = chaseParentBinding(parent, p)
		}
		if getErr == nil {
			r := chaseRunner{e: e, p: p, j: j, parent: parent, c: fresh}
			getErr = r.stop("reconciled")
		}
		unlock()
		if getErr != nil {
			failures = append(failures, fmt.Errorf("chase %s: %w", c.IntentID, getErr))
		}
	}
	active := chases[:0]
	for _, c := range chases {
		if c != nil {
			active = append(active, c)
		}
	}
	return active, skipped, errors.Join(failures...)
}

func closeChaseJournal(j *journal.Journal, err *error) {
	if closeErr := j.Close(); closeErr != nil {
		*err = errors.Join(*err, fmt.Errorf("closing chase journal: %w", closeErr))
	}
}
