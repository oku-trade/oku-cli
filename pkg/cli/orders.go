package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	urfave "github.com/urfave/cli/v3"
	"io"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/signing"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

const (
	defaultIntentTTL = 5 * time.Minute
	maxIntentTTL     = time.Hour
)

func prepareFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "market", Usage: "market, e.g. BTC (required)"},
		&urfave.StringFlag{Name: "side", Usage: "buy or sell (required)"},
		&urfave.StringFlag{Name: "type", Usage: "limit or market (required)"},
		&urfave.StringFlag{Name: "size", Usage: "base size as a decimal string (required)"},
		&urfave.StringFlag{Name: "price", Usage: "limit price (limit orders)"},
		&urfave.StringFlag{Name: "tif", Value: "gtc", Usage: "time in force for limit orders: gtc, ioc, alo"},
		&urfave.IntFlag{Name: "slippage-bps", Usage: "max slippage in basis points from the current mid (market orders, required)"},
		&urfave.BoolFlag{Name: "reduce-only", Usage: "only reduce an existing position"},
		&urfave.StringFlag{Name: "intent-id", Usage: "caller-chosen idempotency id (default: random)"},
		&urfave.DurationFlag{Name: "expires", Value: defaultIntentTTL, Usage: "intent lifetime (max 1h)"},
	}
}

func submitFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "intent", Usage: "intent id from 'orders prepare' (required)"},
		&urfave.StringFlag{Name: "digest", Usage: "intent digest from 'orders prepare' (required)"},
	}
}

func cancelFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "market", Usage: "market of the order (required)"},
		&urfave.Int64Flag{Name: "order-id", Usage: "exchange order id (oid) to cancel (required)"},
		&urfave.StringFlag{Name: "intent-id", Usage: "idempotency id (default: random)"},
	}
}

func statusFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "intent", Usage: "intent id (required)"}}
}

func str(e *env, name string) string { return e.cmd.String(name) }

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
	ttl := e.cmd.Duration("expires")
	if ttl <= 0 || ttl > maxIntentTTL {
		return nil, output.Errorf(output.CodeInvalidArgument, "--expires must be between 1s and 1h")
	}
	slip := e.cmd.Int("slippage-bps")
	in := trading.Intent{
		Version: 1, ID: id, Kind: trading.KindOrder, Network: p.Network, Account: acct, APIURL: p.APIURL,
		Market: coin, Side: side, OrderType: typ, Size: size,
		ReduceOnly: e.cmd.Bool("reduce-only"),
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
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer j.Close()
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
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer j.Close()
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
	if in.Kind != trading.KindCancel {
		// Fast pre-check (the authoritative gate is BeginSubmission): do not
		// add exposure while any submission for this account, from any
		// profile or origin, is unresolved.
		un, err := j.Blocking(in.Network, in.Account)
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
	s, err := e.unlock(p, requiredScope(in.Kind))
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
			jerr := j.Put(rec, "create_failed")
			oe := apiError(err, "CreateAction")
			oe.(*output.Error).ExecutionState = output.ExecNone
			return nil, withJournal(oe, jerr)
		}
		cloid, verr := trading.ValidateAction(in, pay.ActionJSON)
		if verr != nil || pay.ActionID == "" {
			if verr == nil {
				verr = errors.New("missing action id")
			}
			rec.State, rec.ActionID, rec.LastError = journal.StateRejected, pay.ActionID, verr.Error()
			jerr := j.Put(rec, "server_payload_mismatch")
			e.abandonServerAction(c, pay.ActionID)
			return nil, withJournal(output.Errorf(output.CodePayloadMismatch, "%v; nothing was signed", verr).WithExec(output.ExecNone), jerr)
		}
		rec.State, rec.ActionID, rec.ActionJSON, rec.Cloid = journal.StateCreated, pay.ActionID, pay.ActionJSON, cloid
		if err := j.Put(rec, ""); err != nil {
			return nil, output.Wrap(output.CodeInternal, err, "journal")
		}
	} else if _, verr := trading.ValidateAction(in, rec.ActionJSON); verr != nil {
		return nil, output.Errorf(output.CodePayloadMismatch, "%v", verr).WithExec(output.ExecNone)
	}

	// Recheck expiry at the signing boundary: unlock prompts and
	// CreateAction can take arbitrarily long after the first check. The
	// action is still unsigned here, so it can be abandoned safely.
	if in.Expired(e.app.Now()) {
		rec.State, rec.LastError = journal.StateAbandoned, "intent expired before signing"
		jerr := j.Put(rec, "expired_before_sign")
		e.abandonServerAction(c, rec.ActionID)
		return nil, withJournal(output.Errorf(output.CodeIntentState, "intent %q expired at %s before signing; nothing was signed, prepare a new one", id, in.ExpiresAt).WithExec(output.ExecNone), jerr)
	}

	// Nonce allocation, the account-wide unresolved gate and the
	// "submitting" state commit in one transaction before anything is signed.
	nonce, err := j.BeginSubmission(rec, s.md.AgentAddress)
	switch {
	case errors.Is(err, journal.ErrUnresolved):
		return nil, output.Errorf(output.CodeOutcomeUnknown, "this account has an unresolved submission; run 'oku orders reconcile' before submitting").WithExec(output.ExecNone)
	case errors.Is(err, journal.ErrConflict):
		return nil, output.Errorf(output.CodeIntentState, "intent %q changed concurrently; re-run 'oku orders status'", id).WithExec(output.ExecNone)
	case err != nil:
		return nil, output.Wrap(output.CodeInternal, err, "journal (nothing sent)")
	}
	signed, err := signing.SignAction(s.signer, rec.ActionJSON, uint64(nonce))
	if err != nil {
		// Recorded as submitting (fail-safe unknown); nothing was sent.
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "signing (nothing sent; reconcile to clear)")
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
			// On disk the record is already "submitting", which is treated
			// as unknown, so a failed write here stays fail-safe.
			rec.State, rec.LastError = journal.StateUnknown, err.Error()
			return nil, withJournal(unknownErr(rec), j.Put(rec, "confirm_ambiguous"))
		}
		// Definite (non-ambiguous) Connect rejection: the handler refused
		// the request, so it was not executed. Persist that explicitly so
		// durable state matches the REJECTED result.
		jerr := j.RejectDefinite(rec, err.Error())
		oe := apiError(err, "ConfirmAction").(*output.Error)
		oe.Code, oe.ExecutionState = output.CodeRejected, output.ExecRejected
		return nil, withJournal(oe, jerr)
	}
	applyServerState(rec, conf.Status, int64(conf.HlOid), conf.ErrorMessage)
	if err := j.Put(rec, "confirm"); err != nil {
		// Disk still says "submitting" (unknown). Report that, not success.
		return nil, withJournal(unknownErr(rec), err)
	}
	switch rec.State {
	case journal.StateRejected:
		return nil, output.Errorf(output.CodeRejected, "exchange rejected the action: %s", api.Redact(conf.ErrorMessage)).
			WithExec(output.ExecRejected).WithDetail("record", recordView(rec))
	case journal.StateSubmitted:
		return recordResult(rec), nil
	}
	return nil, unknownErr(rec)
}

// withJournal attaches a journal write failure to an error that is already
// being returned, so the caller learns local state may be stale.
func withJournal(err error, jerr error) error {
	if jerr == nil {
		return err
	}
	oe := output.As(err)
	oe.Details = mergeDetail(oe.Details, "journal_error", jerr.Error())
	return oe
}

func mergeDetail(m map[string]any, k string, v any) map[string]any {
	if m == nil {
		m = map[string]any{}
	}
	m[k] = v
	return m
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
	case trading.KindLeverage:
		req.Params.Leverage = &api.LeverageActionMetadata{Coin: in.Market, Leverage: in.Leverage, IsCross: in.IsCross}
	case trading.KindMargin:
		req.Params.Margin = &api.MarginActionMetadata{Coin: in.Market, Amount: in.MarginAmount}
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
	oid := e.cmd.Int64("order-id")
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
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer j.Close()
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
	_, rec, j, err := e.loadRecord()
	if j != nil {
		defer j.Close()
	}
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
	j, err := openJournal(p)
	if err != nil {
		return nil, nil, nil, err
	}
	closeOnErr := true
	defer func() {
		if closeOnErr {
			j.Close()
		}
	}()
	rec, err := j.Get(id)
	if errors.Is(err, journal.ErrNotFound) {
		return nil, nil, nil, output.Errorf(output.CodeNotFound, "intent %q not found", id)
	}
	if err != nil {
		return nil, nil, nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	closeOnErr = false
	return p, rec, j, nil
}

// reconcileBatch bounds the action ids sent per ReconcileActions call.
const reconcileBatch = 50

func (a *App) cmdOrdersReconcile(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	defer unlock()
	un, err := j.Unresolved()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	// Every unresolved action is queried, in bounded batches, with the
	// account-blocking submitting/unknown records first. A fixed cap on
	// the oldest records starved everything beyond it.
	var blockers, others []string
	byAction := map[string]*journal.Record{}
	for _, r := range un {
		if r.State == journal.StateCreating {
			// Never signed; safe to return to prepared.
			r.State = journal.StatePrepared
			if err := j.Put(r, "reconcile: create outcome irrelevant, unsigned"); err != nil {
				return nil, output.Wrap(output.CodeInternal, err, "journal")
			}
			continue
		}
		if r.ActionID == "" || byAction[r.ActionID] != nil {
			continue
		}
		byAction[r.ActionID] = r
		if r.State == journal.StateUnknown || r.State == journal.StateSubmitting {
			blockers = append(blockers, r.ActionID)
		} else {
			others = append(others, r.ActionID)
		}
	}
	ids := append(blockers, others...)
	if len(ids) > 0 {
		s, err := e.unlock(p, "read")
		if err != nil {
			return nil, err
		}
		c := e.client(p)
		c.Token = s.token
		for start := 0; start < len(ids); start += reconcileBatch {
			batch := ids[start:min(start+reconcileBatch, len(ids))]
			ctx, cancel := e.requestCtx()
			var resp api.ReconcileActionsResponse
			err := c.Perps(ctx, "ReconcileActions", map[string]any{"actionIds": batch}, &resp, true)
			cancel()
			if err != nil {
				return nil, apiError(err, "ReconcileActions")
			}
			for _, act := range resp.Actions {
				r := byAction[act.ActionID]
				if r == nil {
					continue
				}
				applyServerState(r, act.State, int64(act.HlOid), act.ErrorMessage)
				if err := j.Put(r, "reconcile"); err != nil {
					return nil, output.Wrap(output.CodeInternal, err, "journal")
				}
			}
		}
	}
	remaining, err := j.Unresolved()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
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
//
// The backend reports "submitted" with no order id when the exchange outcome
// is unknown (account workflow_main.go handleUnknownSubmission), so only a
// positive order id or a terminal state counts as acknowledged. Anything else
// keeps the record unknown, which blocks resends and new exposure.
func applyServerState(r *journal.Record, state string, oid int64, errMsg string) {
	r.ServerState = state
	if r.Intent.Kind == trading.KindLeverage || r.Intent.Kind == trading.KindMargin {
		// Risk actions carry no OID; only "completed" is success and only
		// "error" is an authoritative rejection. Everything else is unknown.
		if errMsg != "" {
			r.LastError = errMsg
		}
		// A signature may have reached the venue once the record left the
		// unsigned states; then "error" is not proof of rejection.
		sent := r.State != journal.StatePrepared && r.State != journal.StateCreating && r.State != journal.StateCreated
		switch trading.RiskOutcome(state, sent) {
		case trading.OutcomeSuccess:
			r.State = journal.StateSubmitted
		case trading.OutcomeRejected:
			r.State = journal.StateRejected
		default:
			if !(state == "pending" && r.State == journal.StateCreated) {
				r.State = journal.StateUnknown
			}
		}
		return
	}
	if oid > 0 {
		r.OrderID = oid
	}
	if errMsg != "" {
		r.LastError = errMsg
	}
	switch {
	case state == "error":
		r.State = journal.StateRejected
	case state == "completed", state == "filled", state == "cancelled", state == "canceled":
		r.State = journal.StateSubmitted
	case state == "submitted" && r.OrderID > 0:
		r.State = journal.StateSubmitted
	case state == "pending" && r.State == journal.StateCreated:
		// Never confirmed by us; still unsigned from the server's view.
	default:
		// "submitted" without an order id, "pending" after a confirm
		// attempt, or an unrecognized state.
		r.State = journal.StateUnknown
	}
}

func (a *App) cmdOrdersAbandon(e *env) (*result, error) {
	p, rec, j, err := e.loadRecord()
	if err != nil {
		return nil, err
	}
	defer j.Close()
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	defer unlock()
	if rec, err = j.Get(rec.Intent.ID); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	// Only never-signed intents can be abandoned. Once a signature has been
	// produced the outcome must be resolved with 'orders reconcile'.
	switch rec.State {
	case journal.StatePrepared, journal.StateCreating:
		rec.State = journal.StateAbandoned
		if err := j.Put(rec, "abandoned locally, never signed"); err != nil {
			return nil, output.Wrap(output.CodeInternal, err, "journal")
		}
		return recordResult(rec), nil
	case journal.StateCreated:
	case journal.StateSubmitting, journal.StateUnknown:
		return nil, output.Errorf(output.CodeOutcomeUnknown, "intent %s was signed and its outcome is unknown; it cannot be abandoned. Run 'oku orders reconcile'", rec.Intent.ID).
			WithExec(output.ExecUnknown)
	default:
		return nil, output.Errorf(output.CodeIntentState, "intent is %s and cannot be abandoned", rec.State)
	}
	// StateCreated: the server holds an unsigned pending action. It cannot
	// execute without our signature, and the signature is never produced
	// for an abandoned intent. Server cleanup (which deletes the pending
	// row) is best effort.
	cleanup := "not_attempted"
	s, uerr := e.unlock(p, requiredScope(rec.Intent.Kind))
	if uerr != nil {
		cleanup = "not_attempted: " + output.As(uerr).Message
	}
	if uerr == nil {
		c := e.client(p)
		c.Token = s.token
		ctx, cancel := e.requestCtx()
		err = c.Perps(ctx, "CancelAction", map[string]string{"actionId": rec.ActionID}, nil, true)
		cancel()
		cleanup = "requested"
		if err != nil {
			cleanup = "failed: " + api.Redact(err.Error())
		}
	}
	rec.State = journal.StateAbandoned
	if err := j.Put(rec, "abandoned locally, never signed; server cleanup "+cleanup); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	res := recordResult(rec)
	if m, ok := res.data.(map[string]any); ok {
		m["server_cleanup"] = cleanup
	}
	return res, nil
}
