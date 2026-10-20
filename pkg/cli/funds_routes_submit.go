package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

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
		// Re-read the selected quote through GetQuotes. The backend's single-router
		// GetQuote decodes the Temporal query result into the wrong type and 500s.
		var fetched v2.SwapGetQuotesResponse
		if err = c.Swap(ctx, "GetQuotes", &v2.SwapGetQuotesRequest{OrderId: r.OrderID}, &fetched, true); err != nil {
			return nil, apiError(err, "GetQuotes")
		}
		var quote *v2.SimpleSwapQuote
		for _, entry := range fetched.Quotes {
			if entry != nil && entry.QuoteId == r.QuoteID && entry.Router == r.Route {
				quote = entry.Quote
			}
		}
		if quote == nil || quote.IntentBased || quote.Simulation == nil || quote.Simulation.Error != "" || len(quote.SigningRequest.GetTypedData()) > 0 {
			return nil, output.Errorf(output.CodePayloadMismatch, "unsupported changed quote")
		}
		if _, e := positiveRaw(quote.Simulation.AmountOutRaw); e != nil {
			return nil, output.Errorf(output.CodePayloadMismatch, "swap quote simulation has no successful output")
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
		var fetched v2.BridgeGetQuotesResponse
		if err = c.Bridge(ctx, "GetQuotes", &v2.BridgeGetQuotesRequest{OrderId: r.OrderID, Generation: r.Generation}, &fetched, true); err != nil {
			return nil, apiError(err, "GetQuotes")
		}
		var quote *v2.BridgeQuote
		for _, entry := range fetched.Quotes {
			if entry != nil && entry.QuoteId == r.QuoteID && entry.Bridge == r.Route {
				quote = entry.Quote
			}
		}
		if quote == nil || quote.IntentBased || len(quote.SigningRequest.GetTypedData()) > 0 {
			return nil, output.Errorf(output.CodePayloadMismatch, "selected bridge quote is unavailable or requires typed-data signing")
		}
		approvals = quote.Approvals
		err = c.Bridge(ctx, "GetExecutionInfo", &v2.GetExecutionInfoRequest{OrderId: r.OrderID, QuoteId: r.QuoteID, Request: &v2.ExecutionRequest{}}, &info, true)
	}
	if err != nil {
		return nil, apiError(err, "GetExecutionInfo")
	}
	var templates []journal.OnchainTemplate
	if kind == "bridge" {
		templates, err = backendBridgeExecution(r.ChainID, info.Info, approvals)
	} else {
		templates, err = verifyExecution(r.OnchainIntent, min, info.Info, approvals)
	}
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing unverified execution")
	}
	if kind == "swap" {
		// Swaps retain their independent deployed OkuRouter check.
		if hasCode, cerr := (onchain.HTTPRPC{Getenv: a.Getenv}).HasCode(ctx, r.ChainID, r.Target); cerr != nil {
			return nil, output.Wrap(output.CodeUpstream, cerr, "route target code check")
		} else if !hasCode {
			return nil, output.Errorf(output.CodePayloadMismatch, "no OkuRouter contract at %s on chain %d", r.Target, r.ChainID)
		}
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
	finalDigest, err := j.BindOnchainExecution(r.ID, r.Digest, templates)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "binding verified execution")
	}
	if err = j.BeginOnchain(r.ID, finalDigest, w.agent); err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "starting onchain intent")
	}
	executing := r.OnchainIntent
	executing.Templates = templates
	// Broadcasting and confirming several transactions outlives the per-request
	// timeout; individual RPC and Privy calls keep their own HTTP timeouts.
	execCtx, execCancel := context.WithTimeout(e.ctx, onchainExecBudget)
	defer execCancel()
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	executor := onchain.Executor{Getenv: a.Getenv, RPC: rpc, Sender: ps, Journal: j, WalletID: s.md.PrivyWalletID}
	// Multi-step routes (approve + swap) run as one atomic EIP-7702 batch, so
	// an approval can never confirm without its swap.
	if len(executing.Templates) > 1 {
		err = executor.ExecuteBatch(execCtx, executing, ps, rpc)
	} else {
		err = executor.Execute(execCtx, executing)
	}
	if err != nil {
		return nil, executionError(err, kind)
	}
	r, err = j.GetOnchain(r.ID)
	if err != nil {
		return nil, err
	}
	if len(r.Hashes) > 0 {
		req := &v2.FinishRequest{OrderId: r.OrderID, TxHash: r.Hashes[len(r.Hashes)-1]}
		var finished v2.FinishResponse
		if kind == "swap" {
			err = c.Swap(execCtx, "Finish", req, &finished, true)
		} else {
			err = c.Bridge(execCtx, "Finish", req, &finished, true)
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
	if kind == "swap" {
		return &result{data: map[string]any{"source": record, "backend": &backend, "destination_settled": false, "settlement_note": "backend order status is informational; verify destination-chain receipt or balance independently"}, human: func(out io.Writer) {
			fmt.Fprintf(out, "%s source: %s; backend: %s (%s); order: %s\n", kind, record.State, backend.Status, backend.Stage, record.OrderID)
		}}, nil
	}
	fill := acrossFill{Status: "unknown"}
	claimRequired := false
	var history v1.OrderHistoryResponse
	if session, unlockErr := e.unlock(w.profile, ""); unlockErr == nil {
		session.apply(client)
	}
	if client.History(ctx, &v1.OrderHistoryRequest{WalletAddr: w.agent, Types: []string{"bridge"}, MaxEntries: 1000}, &history) == nil {
		if trade := bridgeTrade(&history, record.OrderID); trade != nil {
			claimRequired = trade.Status == v1.OrderStatus_ORDER_STATUS_AWAITING_CLAIM
		}
	}
	claimHint := ""
	if claimRequired {
		claimHint = "run oku bridge claim --intent " + record.ID
	}
	if strings.EqualFold(record.Route, "across") && len(record.Hashes) > 0 {
		fill = a.acrossFillStatus(ctx, record.ChainID, record.Hashes[len(record.Hashes)-1])
	}
	return &result{data: map[string]any{"source": record, "backend": &backend, "destination_fill": fill, "destination_settled": false, "settlement_note": "destination fill status is informational; independently verify destination receipt or balance", "claim_required": claimRequired, "claim_hint": claimHint}, human: func(out io.Writer) {
		fmt.Fprintf(out, "%s source: %s; backend: %s (%s); order: %s; destination fill: %s %s\n", kind, record.State, backend.Status, backend.Stage, record.OrderID, fill.Status, fill.FillTx)
		if claimRequired {
			fmt.Fprintln(out, claimHint)
		}
	}}, nil
}
func (a *App) cmdSwapStatus(e *env) (*result, error)   { return a.cmdRouteStatus(e, "swap") }
func (a *App) cmdBridgeStatus(e *env) (*result, error) { return a.cmdRouteStatus(e, "bridge") }

type acrossFill struct {
	Status string `json:"status"`
	FillTx string `json:"fill_tx,omitempty"`
}

func (a *App) acrossFillStatus(ctx context.Context, chain uint64, hash string) acrossFill {
	unknown := acrossFill{Status: "unknown"}
	base := a.Getenv("OKU_ACROSS_API_URL")
	if base == "" {
		base = "https://app.across.to/api/deposit/status"
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return unknown
	}
	query := u.Query()
	query.Set("originChainId", strconv.FormatUint(chain, 10))
	query.Set("depositTxHash", hash)
	u.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return unknown
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return unknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unknown
	}
	var body struct {
		Status string `json:"status"`
		FillTx string `json:"fillTx"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return unknown
	}
	switch strings.ToLower(body.Status) {
	case "filled", "pending", "expired", "refunded":
		return acrossFill{Status: strings.ToLower(body.Status), FillTx: body.FillTx}
	default:
		return unknown
	}
}
