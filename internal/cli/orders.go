package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/internal/api"
	"github.com/oku-trade/oku-cli/internal/decimal"
	"github.com/oku-trade/oku-cli/internal/journal"
	"github.com/oku-trade/oku-cli/internal/output"
	"github.com/oku-trade/oku-cli/internal/signing"
	"github.com/oku-trade/oku-cli/internal/trading"
)

const (
	defaultIntentTTL = 5 * time.Minute
	maxIntentTTL     = time.Hour
	// pendingGrace is how long after a submit attempt a still-"pending"
	// server action is treated as possibly in flight.
	pendingGrace = 2 * time.Minute
)

func prepareFlags(fs *flag.FlagSet) {
	fs.String("market", "", "market, e.g. BTC (required)")
	fs.String("side", "", "buy or sell (required)")
	fs.String("type", "", "limit or market (required)")
	fs.String("size", "", "base size as a decimal string (required)")
	fs.String("price", "", "limit price (limit orders)")
	fs.String("tif", "gtc", "time in force for limit orders: gtc, ioc, alo")
	fs.Int("slippage-bps", 0, "max slippage in basis points from the current mid (market orders, required)")
	fs.Bool("reduce-only", false, "only reduce an existing position")
	fs.String("intent-id", "", "caller-chosen idempotency id (default: random)")
	fs.Duration("expires", defaultIntentTTL, "intent lifetime (max 1h)")
}

func submitFlags(fs *flag.FlagSet) {
	fs.String("intent", "", "intent id from 'orders prepare' (required)")
	fs.String("digest", "", "intent digest from 'orders prepare' (required)")
}

func cancelFlags(fs *flag.FlagSet) {
	fs.String("market", "", "market of the order (required)")
	fs.Int64("order-id", 0, "exchange order id (oid) to cancel (required)")
	fs.String("intent-id", "", "idempotency id (default: random)")
}

func statusFlags(fs *flag.FlagSet) {
	fs.String("intent", "", "intent id (required)")
}

func str(e *env, name string) string { return e.fs.Lookup(name).Value.String() }

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "oku-" + hex.EncodeToString(b[:])
}

// tradeAccount returns the profile account for mutations. --account may only
// repeat it; trading always uses the enrolled profile account.
func (e *env) tradeAccount(p *resolved) (string, error) {
	if p.Name == "" || p.Account == "" {
		return "", output.Errorf(output.CodeProfileNotFound, "trading requires a profile with an account")
	}
	if e.g.account != "" && !strings.EqualFold(e.g.account, p.Account) {
		return "", output.Errorf(output.CodeInvalidArgument, "--account %s differs from profile account %s; trading only uses the enrolled profile account", e.g.account, p.Account)
	}
	return strings.ToLower(p.Account), nil
}

func (e *env) market(p *resolved, coin string) (*api.Market, error) {
	var resp api.GetMarketsResponse
	if err := e.perpsRead(p, "GetMarkets", map[string]string{"coin": coin}, &resp); err != nil {
		return nil, err
	}
	for i := range resp.Markets {
		if resp.Markets[i].Coin == coin {
			return &resp.Markets[i], nil
		}
	}
	return nil, output.Errorf(output.CodeNotFound, "market %s not found", coin)
}

func (a *App) cmdOrdersPrepare(e *env) (*result, error) {
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
	side := str(e, "side")
	if side != "buy" && side != "sell" {
		return nil, output.Errorf(output.CodeInvalidArgument, "--side must be buy or sell")
	}
	typ := str(e, "type")
	if typ != "limit" && typ != "market" {
		return nil, output.Errorf(output.CodeInvalidArgument, "--type must be limit or market")
	}
	size, err := decimal.Canonical(str(e, "size"))
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidDecimal, err, "--size")
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	if !trading.ValidIntentID(id) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--intent-id must be 3-64 characters of [A-Za-z0-9_-]")
	}
	ttl := e.fs.Lookup("expires").Value.(flag.Getter).Get().(time.Duration)
	if ttl <= 0 || ttl > maxIntentTTL {
		return nil, output.Errorf(output.CodeInvalidArgument, "--expires must be between 1s and 1h")
	}
	slip := e.fs.Lookup("slippage-bps").Value.(flag.Getter).Get().(int)
	in := trading.Intent{
		Version: 1, ID: id, Kind: trading.KindOrder, Network: p.Network, Account: acct, APIURL: p.APIURL,
		Market: coin, Side: side, OrderType: typ, Size: size,
		ReduceOnly: str(e, "reduce-only") == "true",
	}
	m, err := e.market(p, coin)
	if err != nil {
		return nil, err
	}
	in.AssetID = m.AssetID
	if err := decimal.ValidateSize(size, m.SzDecimals); err != nil {
		return nil, output.Wrap(output.CodeInvalidDecimal, err, "--size")
	}
	switch typ {
	case "limit":
		if slip != 0 {
			return nil, output.Errorf(output.CodeInvalidArgument, "--slippage-bps applies to market orders only")
		}
		if in.Price, err = decimal.Canonical(str(e, "price")); err != nil {
			return nil, output.Wrap(output.CodeInvalidDecimal, err, "--price")
		}
		if decimal.IsZero(in.Price) {
			return nil, output.Errorf(output.CodeInvalidDecimal, "--price must be greater than zero")
		}
		if err := decimal.ValidatePerpPrice(in.Price, m.SzDecimals); err != nil {
			return nil, output.Wrap(output.CodeInvalidDecimal, err, "--price")
		}
		tif := strings.ToUpper(str(e, "tif"))
		if tif != "GTC" && tif != "IOC" && tif != "ALO" {
			return nil, output.Errorf(output.CodeInvalidArgument, "--tif must be gtc, ioc or alo")
		}
		in.TIF = tif
	case "market":
		if str(e, "price") != "" {
			return nil, output.Errorf(output.CodeInvalidArgument, "--price is not allowed for market orders; use --slippage-bps")
		}
		if slip <= 0 || slip > 1000 {
			return nil, output.Errorf(output.CodeInvalidArgument, "--slippage-bps is required for market orders (1-1000)")
		}
		var q api.GetMarketPriceResponse
		if err := e.perpsRead(p, "GetMarketPrice", map[string]string{"coin": coin}, &q); err != nil {
			return nil, err
		}
		if q.MidPx == "" {
			return nil, output.Errorf(output.CodeUpstream, "no mid price for %s; cannot bound a market order", coin).WithRetry()
		}
		ref, err := decimal.Canonical(q.MidPx)
		if err != nil {
			return nil, output.Wrap(output.CodeUpstream, err, "mid price")
		}
		in.SlippageBps, in.ReferencePx = slip, ref
		if in.WorstPx, err = trading.WorstPrice(ref, side == "buy", slip, 6-m.SzDecimals); err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "slippage bound")
		}
	}
	var fee api.GetBuilderFeeStatusResponse
	if err := e.perpsRead(p, "GetBuilderFeeStatus", map[string]string{"address": acct}, &fee); err != nil {
		return nil, err
	}
	in.BuilderAddress = strings.ToLower(fee.BuilderAddress)
	in.MaxBuilderFee10 = fee.BuilderFeeChargedPpm / 10
	now := e.app.Now().UTC()
	in.CreatedAt = now.Format(time.RFC3339)
	in.ExpiresAt = now.Add(ttl).Format(time.RFC3339)
	j, err := journal.Open(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "opening journal")
	}
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	defer unlock()
	rec, _, err := j.Create(in)
	if errors.Is(err, journal.ErrConflict) {
		return nil, output.Errorf(output.CodeIntentConflict, "intent id %q already exists with a different payload", id).
			WithDetail("existing_digest", rec.Digest)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	notes := []string{
		"preparing sends nothing to the exchange",
		"fills are not guaranteed; market orders are IOC limit orders bounded by worst_px",
	}
	if !fee.BuilderFeeApproved {
		notes = append(notes, "Oku builder fee is not approved for this account; the submit will be rejected until it is approved on the Oku website")
	}
	data := map[string]any{
		"intent_id": rec.Intent.ID, "digest": rec.Digest, "state": rec.State, "intent": rec.Intent,
		"builder_fee": map[string]any{"address": fee.BuilderAddress, "charged_ppm": fee.BuilderFeeChargedPpm, "approved": fee.BuilderFeeApproved},
		"notes":       notes,
		"next":        fmt.Sprintf("oku orders submit --intent %s --digest %s", rec.Intent.ID, rec.Digest),
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "prepared %s %s %s %s on %s for %s (%s)\n", in.Side, in.Size, in.Market, in.OrderType, p.Network, acct, rec.Intent.ID)
		if in.OrderType == "limit" {
			fmt.Fprintf(w, "  price %s tif %s reduce-only %v\n", in.Price, in.TIF, in.ReduceOnly)
		} else {
			fmt.Fprintf(w, "  mid %s, worst price %s (%d bps) reduce-only %v\n", in.ReferencePx, in.WorstPx, in.SlippageBps, in.ReduceOnly)
		}
		fmt.Fprintf(w, "  builder fee up to %d ppm, expires %s\n", fee.BuilderFeeChargedPpm, in.ExpiresAt)
		for _, n := range notes {
			fmt.Fprintf(w, "  note: %s\n", n)
		}
		fmt.Fprintf(w, "submit with: %s\n", data["next"])
	}}, nil
}

func (a *App) cmdOrdersSubmit(e *env) (*result, error) {
	id, digest := str(e, "intent"), str(e, "digest")
	if id == "" || digest == "" {
		return nil, output.Errorf(output.CodeUsage, "usage: oku orders submit --intent ID --digest SHA")
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if _, err := e.tradeAccount(p); err != nil {
		return nil, err
	}
	return e.submit(p, id, digest)
}

// submit runs the state machine for one intent. Every state transition is
// persisted before the corresponding network call.
func (e *env) submit(p *resolved, id, digest string) (*result, error) {
	j, err := journal.Open(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "opening journal")
	}
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	defer unlock()
	rec, err := j.Get(id)
	if errors.Is(err, journal.ErrNotFound) {
		return nil, output.Errorf(output.CodeNotFound, "intent %q not found", id)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	if rec.Digest != digest {
		return nil, output.Errorf(output.CodePayloadMismatch, "digest does not match intent %q; review the intent and pass its exact digest", id)
	}
	in := &rec.Intent
	if in.Network != p.Network || !strings.EqualFold(in.Account, p.Account) || in.APIURL != p.APIURL {
		return nil, output.Errorf(output.CodeConfigError, "intent was prepared for %s/%s/%s, not the current profile", in.Network, in.Account, in.APIURL)
	}
	switch rec.State {
	case journal.StatePrepared, journal.StateCreated:
	case journal.StateSubmitting, journal.StateUnknown:
		return nil, unknownErr(rec)
	case journal.StateCreating:
		// CreateAction outcome unknown, but an unsigned action cannot
		// execute. Continue with a fresh CreateAction.
	default:
		return nil, output.Errorf(output.CodeIntentState, "intent %q is already %s; it will not be sent again", id, rec.State).
			WithDetail("record", rec)
	}
	if in.Expired(e.app.Now()) {
		return nil, output.Errorf(output.CodeIntentState, "intent %q expired at %s; prepare a new one", id, in.ExpiresAt)
	}
	if in.Kind == trading.KindOrder {
		// Do not add exposure while any earlier submission is unresolved.
		un, err := j.Unresolved()
		if err != nil {
			return nil, output.Wrap(output.CodeInternal, err, "journal")
		}
		for _, u := range un {
			if u.Intent.ID != id && (u.State == journal.StateUnknown || u.State == journal.StateSubmitting) {
				return nil, output.Errorf(output.CodeOutcomeUnknown, "intent %s has an unknown outcome; run 'oku orders reconcile' before submitting new orders", u.Intent.ID).
					WithDetail("blocking_intent", u.Intent.ID)
			}
		}
	}
	s, err := e.unlock(p, "trade")
	if err != nil {
		return nil, err
	}
	c := e.client(p)
	c.Token = s.token

	if rec.State != journal.StateCreated {
		rec.State = journal.StateCreating
		if err := j.Put(rec, ""); err != nil {
			return nil, output.Wrap(output.CodeInternal, err, "journal")
		}
		ctx, cancel := e.requestCtx()
		var pay api.ActionPayloadResponse
		err := c.Perps(ctx, "CreateAction", createRequest(in), &pay, true)
		cancel()
		if err != nil {
			rec.State, rec.LastError = journal.StatePrepared, "create failed: "+err.Error()
			_ = j.Put(rec, "create_failed")
			oe := apiError(err, "CreateAction")
			oe.(*output.Error).ExecutionState = output.ExecNone
			return nil, oe
		}
		cloid, verr := trading.ValidateAction(in, pay.ActionJSON)
		if verr != nil || pay.ActionID == "" {
			if verr == nil {
				verr = errors.New("missing action id")
			}
			rec.State, rec.ActionID, rec.LastError = journal.StateRejected, pay.ActionID, verr.Error()
			_ = j.Put(rec, "server_payload_mismatch")
			e.abandonServerAction(c, pay.ActionID)
			return nil, output.Errorf(output.CodePayloadMismatch, "%v; nothing was signed", verr).WithExec(output.ExecNone)
		}
		rec.State, rec.ActionID, rec.ActionJSON, rec.Cloid = journal.StateCreated, pay.ActionID, pay.ActionJSON, cloid
		if err := j.Put(rec, ""); err != nil {
			return nil, output.Wrap(output.CodeInternal, err, "journal")
		}
	} else if _, verr := trading.ValidateAction(in, rec.ActionJSON); verr != nil {
		return nil, output.Errorf(output.CodePayloadMismatch, "%v", verr).WithExec(output.ExecNone)
	}

	nonce, err := j.NextNonce(s.md.AgentAddress)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "allocating nonce")
	}
	signed, err := signing.SignAction(s.signer, rec.ActionJSON, uint64(nonce))
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "signing")
	}
	rec.State, rec.Nonce = journal.StateSubmitting, nonce
	if err := j.Put(rec, ""); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal (nothing sent)")
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	var conf api.ConfirmActionResponse
	err = c.Perps(ctx, "ConfirmAction", api.ConfirmActionRequest{
		ActionID: rec.ActionID,
		SignedAction: api.SignedAction{
			ActionJSON: signed.ActionJSON, Nonce: api.Int64(nonce),
			Signature: api.EVMSignature{R: signed.Signature.R, S: signed.Signature.S, V: api.Int64(signed.Signature.V)},
		},
	}, &conf, true)
	if err != nil {
		ae, _ := api.AsError(err)
		if ae == nil || ae.Ambiguous {
			rec.State, rec.LastError = journal.StateUnknown, err.Error()
			_ = j.Put(rec, "confirm_ambiguous")
			return nil, unknownErr(rec)
		}
		rec.State, rec.LastError = journal.StateRejected, err.Error()
		_ = j.Put(rec, "confirm_rejected")
		oe := apiError(err, "ConfirmAction").(*output.Error)
		oe.Code, oe.ExecutionState = output.CodeRejected, output.ExecRejected
		return nil, oe
	}
	rec.State, rec.ServerState, rec.OrderID = journal.StateSubmitted, conf.Status, int64(conf.HlOid)
	rec.LastError = conf.ErrorMessage
	if err := j.Put(rec, ""); err != nil {
		e.diag("warning: submitted but journal update failed: %v", err)
	}
	if conf.Status == "error" {
		return nil, output.Errorf(output.CodeRejected, "exchange rejected the action: %s", api.Redact(conf.ErrorMessage)).
			WithExec(output.ExecRejected).WithDetail("record", recordView(rec))
	}
	return recordResult(rec), nil
}

func createRequest(in *trading.Intent) api.CreateActionRequest {
	req := api.CreateActionRequest{Address: in.Account}
	switch in.Kind {
	case trading.KindOrder:
		req.Params.Order = &api.OrderActionMetadata{
			Coin: in.Market, Side: in.Side, OrderType: in.OrderType, Sz: in.Size,
			LimitPx: in.Price, Tif: in.TIF, ReduceOnly: in.ReduceOnly, SlippageBps: in.SlippageBps,
		}
	case trading.KindCancel:
		req.Params.Cancel = &api.CancelActionMetadata{Coin: in.Market, Oid: api.Int64(in.OrderID)}
	}
	return req
}

// abandonServerAction cancels an unsigned pending server action. Best effort:
// an unsigned action cannot execute regardless.
func (e *env) abandonServerAction(c *api.Client, actionID string) {
	if actionID == "" {
		return
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	if err := c.Perps(ctx, "CancelAction", map[string]string{"actionId": actionID}, nil, true); err != nil {
		e.diag("note: could not cancel unsigned server action %s: %v", actionID, err)
	}
}

func unknownErr(rec *journal.Record) error {
	return output.Errorf(output.CodeOutcomeUnknown,
		"submission of intent %s has an unknown outcome; it will not be resent. Run 'oku orders reconcile'", rec.Intent.ID).
		WithExec(output.ExecUnknown).WithRetry().WithDetail("record", recordView(rec)).
		WithDetail("hint", "oku orders reconcile")
}

func recordView(r *journal.Record) map[string]any {
	return map[string]any{
		"intent_id":    r.Intent.ID,
		"digest":       r.Digest,
		"state":        r.State,
		"server_state": r.ServerState,
		"action_id":    r.ActionID,
		"cloid":        r.Cloid,
		"order_id":     r.OrderID,
		"nonce":        r.Nonce,
		"last_error":   api.Redact(r.LastError),
		"intent":       r.Intent,
		"updated_at":   r.UpdatedAt,
		"history":      r.History,
	}
}

func recordResult(r *journal.Record) *result {
	return &result{data: recordView(r), human: func(w io.Writer) {
		fmt.Fprintf(w, "intent %s: %s", r.Intent.ID, r.State)
		if r.ServerState != "" {
			fmt.Fprintf(w, " (server: %s)", r.ServerState)
		}
		if r.OrderID != 0 {
			fmt.Fprintf(w, " oid %d", r.OrderID)
		}
		fmt.Fprintln(w)
		if r.LastError != "" {
			fmt.Fprintf(w, "  last error: %s\n", api.Redact(r.LastError))
		}
	}}
}

func (a *App) cmdOrdersCancel(e *env) (*result, error) {
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
	oid, _ := strconv.ParseInt(str(e, "order-id"), 10, 64)
	if oid <= 0 {
		return nil, output.Errorf(output.CodeInvalidArgument, "--order-id is required")
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	if !trading.ValidIntentID(id) {
		return nil, output.Errorf(output.CodeInvalidArgument, "invalid --intent-id")
	}
	m, err := e.market(p, coin)
	if err != nil {
		return nil, err
	}
	now := e.app.Now().UTC()
	in := trading.Intent{
		Version: 1, ID: id, Kind: trading.KindCancel, Network: p.Network, Account: acct, APIURL: p.APIURL,
		Market: coin, AssetID: m.AssetID, OrderID: oid,
		CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(defaultIntentTTL).Format(time.RFC3339),
	}
	j, err := journal.Open(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "opening journal")
	}
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	rec, _, err := j.Create(in)
	unlock()
	if errors.Is(err, journal.ErrConflict) {
		return nil, output.Errorf(output.CodeIntentConflict, "intent id %q already exists with a different payload", id)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	return e.submit(p, rec.Intent.ID, rec.Digest)
}

func (a *App) cmdOrdersStatus(e *env) (*result, error) {
	_, rec, _, err := e.loadRecord()
	if err != nil {
		return nil, err
	}
	return recordResult(rec), nil
}

func (e *env) loadRecord() (*resolved, *journal.Record, *journal.Journal, error) {
	id := str(e, "intent")
	if id == "" {
		return nil, nil, nil, output.Errorf(output.CodeUsage, "--intent is required")
	}
	p, err := e.profile()
	if err != nil {
		return nil, nil, nil, err
	}
	if p.Name == "" {
		return nil, nil, nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	j, err := journal.Open(p.Name)
	if err != nil {
		return nil, nil, nil, output.Wrap(output.CodeInternal, err, "opening journal")
	}
	rec, err := j.Get(id)
	if errors.Is(err, journal.ErrNotFound) {
		return nil, nil, nil, output.Errorf(output.CodeNotFound, "intent %q not found", id)
	}
	if err != nil {
		return nil, nil, nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	return p, rec, j, nil
}

func (a *App) cmdOrdersReconcile(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	j, err := journal.Open(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "opening journal")
	}
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	defer unlock()
	un, err := j.Unresolved()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	ids := []string{}
	byAction := map[string]*journal.Record{}
	for _, r := range un {
		if r.State == journal.StateCreating {
			// Never signed; safe to return to prepared.
			r.State = journal.StatePrepared
			_ = j.Put(r, "reconcile: create outcome irrelevant, unsigned")
			continue
		}
		if r.ActionID != "" && len(ids) < 50 {
			ids = append(ids, r.ActionID)
			byAction[r.ActionID] = r
		}
	}
	if len(ids) > 0 {
		s, err := e.unlock(p, "read")
		if err != nil {
			return nil, err
		}
		c := e.client(p)
		c.Token = s.token
		ctx, cancel := e.requestCtx()
		defer cancel()
		var resp api.ReconcileActionsResponse
		if err := c.Perps(ctx, "ReconcileActions", map[string]any{"actionIds": ids}, &resp, true); err != nil {
			return nil, apiError(err, "ReconcileActions")
		}
		for _, act := range resp.Actions {
			r := byAction[act.ActionID]
			if r == nil {
				continue
			}
			applyServerState(r, &act)
			_ = j.Put(r, "reconcile")
		}
	}
	remaining, _ := j.Unresolved()
	views := []map[string]any{}
	for _, r := range un {
		views = append(views, recordView(r))
	}
	unknown := 0
	for _, r := range remaining {
		if r.State == journal.StateUnknown || r.State == journal.StateSubmitting {
			unknown++
		}
	}
	data := map[string]any{"checked": views, "still_unknown": unknown}
	return &result{data: data, human: func(w io.Writer) {
		if len(un) == 0 {
			fmt.Fprintln(w, "nothing to reconcile")
			return
		}
		for _, r := range un {
			recordResult(r).human(w)
		}
		fmt.Fprintf(w, "%d intent(s) still unknown\n", unknown)
	}}, nil
}

// applyServerState maps authoritative backend action state onto a record.
func applyServerState(r *journal.Record, act *api.GetActionResponse) {
	r.ServerState = act.State
	if act.HlOid != 0 {
		r.OrderID = int64(act.HlOid)
	}
	if act.ErrorMessage != "" {
		r.LastError = act.ErrorMessage
	}
	switch act.State {
	case "submitted", "completed", "filled", "cancelled", "canceled", "error":
		r.State = journal.StateSubmitted
	case "pending":
		// Server has not recorded a confirmation. A confirm could still be
		// in flight shortly after a timeout, so stay unknown; the user can
		// explicitly abandon it after the grace period.
		if r.State == journal.StateSubmitting {
			r.State = journal.StateUnknown
		}
	}
}

func (a *App) cmdOrdersAbandon(e *env) (*result, error) {
	p, rec, j, err := e.loadRecord()
	if err != nil {
		return nil, err
	}
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	defer unlock()
	if rec, err = j.Get(rec.Intent.ID); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	switch rec.State {
	case journal.StatePrepared, journal.StateCreating:
		rec.State = journal.StateAbandoned
		_ = j.Put(rec, "abandoned locally, never signed")
		return recordResult(rec), nil
	case journal.StateCreated, journal.StateUnknown, journal.StateSubmitting:
	default:
		return nil, output.Errorf(output.CodeIntentState, "intent is %s and cannot be abandoned", rec.State)
	}
	if rec.State != journal.StateCreated {
		if t, err := time.Parse(time.RFC3339Nano, rec.UpdatedAt); err == nil && e.app.Now().Sub(t) < pendingGrace {
			return nil, output.Errorf(output.CodeOutcomeUnknown, "last submit attempt was under %s ago; wait and reconcile first", pendingGrace).WithExec(output.ExecUnknown)
		}
	}
	s, err := e.unlock(p, "trade")
	if err != nil {
		return nil, err
	}
	c := e.client(p)
	c.Token = s.token
	ctx, cancel := e.requestCtx()
	defer cancel()
	if err := c.Perps(ctx, "CancelAction", map[string]string{"actionId": rec.ActionID}, nil, true); err != nil {
		return nil, apiError(err, "CancelAction")
	}
	var act api.GetActionResponse
	if err := c.Perps(ctx, "GetAction", map[string]string{"actionId": rec.ActionID}, &act, true); err != nil {
		return nil, apiError(err, "GetAction")
	}
	if act.State != "cancelled" && act.State != "canceled" {
		applyServerState(rec, &act)
		_ = j.Put(rec, "abandon: server state "+act.State)
		return nil, output.Errorf(output.CodeIntentState, "server reports action state %q; not abandoned", act.State).WithDetail("record", recordView(rec))
	}
	rec.State, rec.ServerState = journal.StateAbandoned, act.State
	_ = j.Put(rec, "abandoned: server action cancelled before execution")
	return recordResult(rec), nil
}
