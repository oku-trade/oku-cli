package cli

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// verifyExecution is the final authorization boundary. Until router calldata
// can be decoded by an audited selector-specific adapter, execution fails closed.
func verifyExecution(in journal.OnchainIntent, quoteMin string, info *v2.ExecutionInformation, approvals []*v2.ApprovalPayloadV2) ([]journal.OnchainTemplate, error) {
	fail := func(reason string) ([]journal.OnchainTemplate, error) {
		return nil, fmt.Errorf("execution does not match prepared intent: %s", reason)
	}
	if !safeInfo(info) || !onchain.Allowed(in.ChainID) || !onchain.Allowed(in.OutChain) || !onchain.ValidAddress(in.Recipient) || !onchain.ValidAddress(in.Target) || !onchain.ValidAddress(in.Token) || !onchain.ValidAddress(in.Spender) {
		return fail("unsupported route or missing bound addresses")
	}
	min, err := positiveRaw(in.MinOut)
	if err != nil {
		return fail("invalid minimum output")
	}
	got, err := positiveRaw(quoteMin)
	if err != nil || got.Cmp(min) < 0 {
		return fail("JIT quote worsens minimum output")
	}
	if _, err := positiveRaw(in.Amount); err != nil {
		return fail("invalid input amount")
	}
	trade := info.Trade
	if trade.ChainId != int64(in.ChainID) || !strings.EqualFold(trade.To, in.Target) {
		return fail("trade chain or router differs")
	}
	if len(info.Transactions) > 0 {
		tx := info.Transactions[0]
		if tx == nil || tx.ChainId != trade.ChainId || !strings.EqualFold(tx.To, trade.To) || tx.Data != trade.Data || tx.Value != trade.Value {
			return fail("extra or divergent backend transaction")
		}
	}
	if trade.Value != "" && trade.Value != "0" && trade.Value != "0x0" {
		return fail("ERC-20 route sends native value")
	}
	raw := strings.TrimPrefix(trade.Data, "0x")
	data, err := hex.DecodeString(raw)
	if err != nil || len(data) < 4 {
		return fail("invalid trade calldata")
	}
	// A matching 32-byte word in arbitrary calldata does not establish ABI semantics.
	// Until each supported router selector has a decoded and audited recipient and
	// minimum-output field, no swap or bridge transaction may be broadcast.
	return fail("router calldata semantics are not verified; submission disabled")
}
func (a *App) cmdRouteSubmit(e *env, kind string) (*result, error) {
	if str(e, "intent") == "" || str(e, "digest") == "" {
		return nil, output.Errorf(output.CodeUsage, "--intent and --digest required")
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	r, err := j.GetOnchain(str(e, "intent"))
	if err != nil {
		return nil, output.Wrap(output.CodeNotFound, err, "intent")
	}
	if r.Kind != kind || !strings.EqualFold(r.Sender, w.agent) || r.Digest != str(e, "digest") || r.State != "prepared" {
		return nil, output.Errorf(output.CodeIntentState, "intent, agent, digest or prepared state mismatch; never retry an unresolved submission")
	}
	expires, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	if err != nil || !a.Now().Before(expires) {
		return nil, output.Errorf(output.CodeIntentState, "quote expired; prepare a new intent")
	}
	unlock, err := j.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	s, err := e.unlock(w.profile, "")
	if err != nil {
		return nil, err
	}
	c := e.client(w.profile)
	s.apply(c)
	ctx, cancel := e.requestCtx()
	defer cancel()
	var info v2.GetExecutionInfoResponse
	var approvals []*v2.ApprovalPayloadV2
	var min string
	if kind == "swap" {
		var selected v2.SwapUpdateQuoteSelectResponse
		if err = c.Swap(ctx, "UpdateQuoteSelect", &v2.SwapUpdateQuoteSelectRequest{OrderId: r.OrderID, Router: r.Route, QuoteId: r.QuoteID}, &selected, true); err != nil {
			return nil, apiError(err, "UpdateQuoteSelect")
		}
		if selected.QuoteId != r.QuoteID || selected.Router != r.Route {
			return nil, output.Errorf(output.CodePayloadMismatch, "selected swap quote changed")
		}
		var confirmed v2.SwapConfirmQuoteSelectResponse
		if err = c.Swap(ctx, "ConfirmQuoteSelect", &v2.SwapConfirmQuoteSelectRequest{OrderId: r.OrderID, Router: r.Route, QuoteId: r.QuoteID}, &confirmed, true); err != nil {
			return nil, apiError(err, "ConfirmQuoteSelect")
		}
		var fetched v2.SwapGetQuoteResponse
		if err = c.Swap(ctx, "GetQuote", &v2.SwapGetQuoteRequest{OrderId: r.OrderID, Router: r.Route}, &fetched, true); err != nil {
			return nil, apiError(err, "GetQuote")
		}
		var quote *v2.SimpleSwapQuote
		for _, entry := range fetched.Quotes {
			if entry != nil && entry.QuoteId == r.QuoteID && entry.Router == r.Route {
				quote = entry.Quote
			}
		}
		if quote == nil || quote.IntentBased || len(quote.SigningRequest.GetTypedData()) > 0 {
			return nil, output.Errorf(output.CodePayloadMismatch, "unsupported changed quote")
		}
		min = quote.MinAmountOutRaw
		approvals = quote.ApprovalsV2
		err = c.Swap(ctx, "GetExecutionInfo", &v2.GetExecutionInfoRequest{OrderId: r.OrderID, QuoteId: r.QuoteID, Request: &v2.ExecutionRequest{SigningRequest: quote.SigningRequest}}, &info, true)
	} else {
		var selected v2.BridgeUpdateQuoteSelectResponse
		if err = c.Bridge(ctx, "UpdateQuoteSelect", &v2.BridgeUpdateQuoteSelectRequest{OrderId: r.OrderID, Bridge: r.Route, QuoteId: r.QuoteID}, &selected, true); err != nil {
			return nil, apiError(err, "UpdateQuoteSelect")
		}
		if selected.QuoteId != r.QuoteID || selected.Bridge != r.Route {
			return nil, output.Errorf(output.CodePayloadMismatch, "selected bridge quote changed")
		}
		var confirmed v2.BridgeConfirmQuoteSelectResponse
		if err = c.Bridge(ctx, "ConfirmQuoteSelect", &v2.BridgeConfirmQuoteSelectRequest{OrderId: r.OrderID, Bridge: r.Route, QuoteId: r.QuoteID}, &confirmed, true); err != nil {
			return nil, apiError(err, "ConfirmQuoteSelect")
		}
		var fetched v2.BridgeGetQuoteResponse
		if err = c.Bridge(ctx, "GetQuote", &v2.BridgeGetQuoteRequest{OrderId: r.OrderID, Bridge: r.Route}, &fetched, true); err != nil {
			return nil, apiError(err, "GetQuote")
		}
		var quote *v2.BridgeQuote
		for _, entry := range fetched.Quotes {
			if entry != nil && entry.QuoteId == r.QuoteID && entry.Bridge == r.Route {
				quote = entry.Quote
			}
		}
		if quote == nil || quote.IntentBased || quote.InChainId != int32(r.ChainID) || quote.OutChainId != int32(r.OutChain) || len(quote.SigningRequest.GetTypedData()) > 0 {
			return nil, output.Errorf(output.CodePayloadMismatch, "unsupported changed bridge quote")
		}
		min = quote.MinAmountOut
		approvals = quote.Approvals
		err = c.Bridge(ctx, "GetExecutionInfo", &v2.GetExecutionInfoRequest{OrderId: r.OrderID, QuoteId: r.QuoteID, Request: &v2.ExecutionRequest{SigningRequest: quote.SigningRequest}}, &info, true)
	}
	if err != nil {
		return nil, apiError(err, "GetExecutionInfo")
	}
	templates, err := verifyExecution(r.OnchainIntent, min, info.Info, approvals)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing unverified execution")
	}
	pc, err := e.privyClient(w.profile)
	if err != nil {
		return nil, err
	}
	_, sec, _, err := e.unlockCredential(w.profile.Name)
	if err != nil {
		return nil, err
	}
	ps, err := pc.Authenticate(ctx, sec.AccessToken)
	if err != nil {
		return nil, output.Wrap(output.CodeAuthRequired, err, "Privy authentication")
	}
	bound := false
	for _, wallet := range ps.Wallets() {
		if wallet.ID == s.md.PrivyWalletID && strings.EqualFold(wallet.Address, w.agent) && wallet.ChainType == "ethereum" {
			bound = true
		}
	}
	if !bound {
		return nil, output.Errorf(output.CodeSignerUnavailable, "authenticated Privy wallet differs from agent")
	}
	if err = j.BeginOnchain(r.ID, r.Digest, w.agent); err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "starting onchain intent")
	}
	executing := r.OnchainIntent
	executing.Templates = templates
	executor := onchain.Executor{RPC: onchain.HTTPRPC{Getenv: a.Getenv}, Sender: ps, Journal: j, WalletID: s.md.PrivyWalletID}
	if err = executor.Execute(ctx, executing); err != nil {
		if errors.Is(err, onchain.ErrGas) || errors.Is(err, onchain.ErrBalance) {
			current, readErr := j.GetOnchain(r.ID)
			if readErr != nil || len(current.Hashes) > 0 {
				return nil, output.Wrap(output.CodeOutcomeUnknown, err, "a prior approval may already have broadcast; reconcile without retrying")
			}
			if jerr := j.AdvanceOnchain(r.ID, "sending", "failed", ""); jerr != nil {
				return nil, output.Wrap(output.CodeOutcomeUnknown, jerr, "journal transition failed")
			}
			return nil, output.Wrap(output.CodeInsufficientBalance, err, "onchain preflight")
		}
		return nil, output.Wrap(output.CodeOutcomeUnknown, err, "never resubmit; inspect status and receipt")
	}
	r, err = j.GetOnchain(r.ID)
	if err != nil {
		return nil, err
	}
	if len(r.Hashes) > 0 {
		req := &v2.FinishRequest{OrderId: r.OrderID, TxHash: r.Hashes[len(r.Hashes)-1]}
		var finished v2.FinishResponse
		if kind == "swap" {
			err = c.Swap(ctx, "Finish", req, &finished, true)
		} else {
			err = c.Bridge(ctx, "Finish", req, &finished, true)
		}
		if err != nil {
			return routeRecord(r), nil
		} // confirmed source hash is durable; status may reconcile backend separately.
	}
	return routeRecord(r), nil
}
func (a *App) cmdSwapSubmit(e *env) (*result, error)   { return a.cmdRouteSubmit(e, "swap") }
func (a *App) cmdBridgeSubmit(e *env) (*result, error) { return a.cmdRouteSubmit(e, "bridge") }
func (a *App) cmdRouteStatus(e *env, kind string) (*result, error) {
	status, err := a.cmdFundsStatus(e, kind)
	if err != nil {
		return nil, err
	}
	record, ok := status.data.(*journal.OnchainRecord)
	if !ok || record.OrderID == "" {
		return nil, output.Errorf(output.CodeIntentConflict, "missing backend order")
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	client := e.client(w.profile)
	var backend v2.PingResponse
	if kind == "swap" {
		err = client.Swap(ctx, "Ping", &v2.PingRequest{OrderId: record.OrderID}, &backend, false)
	} else {
		err = client.Bridge(ctx, "Ping", &v2.PingRequest{OrderId: record.OrderID}, &backend, false)
	}
	if err != nil {
		return nil, apiError(err, "order status")
	}
	if backend.OrderId != record.OrderID {
		return nil, output.Errorf(output.CodePayloadMismatch, "backend order ID differs")
	}
	return &result{data: map[string]any{"source": record, "backend": &backend, "destination_settled": false, "settlement_note": "backend order status is informational; verify destination-chain receipt or balance independently"}, human: func(out io.Writer) {
		fmt.Fprintf(out, "%s source: %s; backend: %s (%s); order: %s\n", kind, record.State, backend.Status, backend.Stage, record.OrderID)
	}}, nil
}
func (a *App) cmdSwapStatus(e *env) (*result, error)   { return a.cmdRouteStatus(e, "swap") }
func (a *App) cmdBridgeStatus(e *env) (*result, error) { return a.cmdRouteStatus(e, "bridge") }
