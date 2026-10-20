package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	v1 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

func bridgeTrade(history *v1.OrderHistoryResponse, order string) *v1.OrderTrade {
	for _, trade := range history.History {
		if trade.OrderId == order {
			return trade
		}
	}
	return nil
}
func claimResult(message string, record *journal.OnchainRecord, warning string) *result {
	data := map[string]any{"status": message}
	if record != nil {
		data["claim"] = record
	}
	if warning != "" {
		data["warning"] = warning
	}
	return &result{data: data, human: func(out io.Writer) {
		fmt.Fprintln(out, message)
		if record != nil {
			fmt.Fprintf(out, "Claim intent: %s (%s)\n", record.ID, record.State)
		}
		if warning != "" {
			fmt.Fprintln(out, "Warning:", warning)
		}
	}}
}
func (a *App) cmdBridgeClaim(e *env) (*result, error) {
	id := str(e, "intent")
	if id == "" {
		return nil, output.Errorf(output.CodeUsage, "--intent required")
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
	source, err := j.GetOnchain(id)
	if err != nil {
		return nil, output.Wrap(output.CodeNotFound, err, "bridge intent")
	}
	if source.Kind != "bridge" || !strings.EqualFold(source.Sender, w.agent) || source.OrderID == "" {
		return nil, output.Errorf(output.CodeIntentConflict, "intent is not an agent bridge order")
	}
	if source.State == "sent" || source.State == "unknown" {
		_, _ = a.cmdFundsStatus(e, "bridge")
	}
	unlock, err := j.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	source, err = j.GetOnchain(id)
	if err != nil {
		return nil, err
	}
	if source.State != "confirmed" {
		return claimResult("bridge source is "+source.State+"; run oku bridge status --intent "+id, nil, ""), nil
	}
	existing, err := j.ClaimForBridge(id)
	if err == nil {
		return claimResult("claim already recorded; never resubmit", existing, ""), nil
	}
	if !errors.Is(err, journal.ErrNotFound) {
		return nil, err
	}
	c := e.client(w.profile)
	s, err := e.unlock(w.profile, "")
	if err != nil {
		return nil, err
	}
	s.apply(c)
	ctx, cancel := e.requestCtx()
	defer cancel()
	var history v1.OrderHistoryResponse
	if err = c.History(ctx, &v1.OrderHistoryRequest{WalletAddr: w.agent, Types: []string{"bridge"}, MaxEntries: 1000}, &history); err != nil {
		return nil, apiError(err, "History")
	}
	trade := bridgeTrade(&history, source.OrderID)
	if trade == nil {
		return nil, output.Errorf(output.CodeNotFound, "bridge order %s not found in history", source.OrderID)
	}
	if trade.Status != v1.OrderStatus_ORDER_STATUS_AWAITING_CLAIM {
		return claimResult("no claim needed: bridge order is "+trade.Status.String(), nil, ""), nil
	}
	to, data := trade.Metadata["claimTo"], trade.Metadata["claimCalldata"]
	if to == "" || data == "" {
		return claimResult("bridge awaiting claim but claim metadata is not yet available", nil, ""), nil
	}
	chainText := strings.TrimPrefix(trade.DstChain, "eip155:")
	if !strings.HasPrefix(trade.DstChain, "eip155:") {
		return nil, output.Errorf(output.CodePayloadMismatch, "unsupported destination chain %q", trade.DstChain)
	}
	chain, parseErr := strconv.ParseUint(chainText, 10, 64)
	if parseErr != nil || chain != source.OutChain {
		return nil, output.Errorf(output.CodePayloadMismatch, "destination chain does not match bridge intent")
	}
	if _, err = onchain.RPCURL(a.Getenv, chain); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "destination chain RPC")
	}
	if !onchain.ValidAddress(to) || !strings.HasPrefix(data, "0x") {
		return nil, output.Errorf(output.CodePayloadMismatch, "invalid claim transaction shape")
	}
	if _, err = hex.DecodeString(data[2:]); err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "claim calldata must be hex")
	}
	value := trade.Metadata["claimValue"]
	if value == "" {
		value = "0"
	}
	amount, err := backendValue(value)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "claim value")
	}
	template := journal.OnchainTemplate{ChainID: chain, To: to, Data: data, Value: onchain.Hex(amount)}
	in := journal.OnchainIntent{ID: randomID(), Kind: "claim", ParentIntent: id, OrderID: source.OrderID, ChainID: chain, OutChain: chain, Sender: w.agent, Recipient: to, Token: "native", Amount: value, Templates: []journal.OnchainTemplate{template}, CreatedAt: a.Now().UTC().Format(time.RFC3339Nano)}
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
	record, err := j.CreateOnchain(in)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentConflict, err, "creating claim intent")
	}
	if err = j.BeginOnchain(record.ID, record.Digest, w.agent); err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "starting claim")
	}
	execCtx, execCancel := context.WithTimeout(e.ctx, onchainExecBudget)
	defer execCancel()
	executor := onchain.Executor{Getenv: a.Getenv, RPC: onchain.HTTPRPC{Getenv: a.Getenv}, Sender: ps, Journal: j, WalletID: s.md.PrivyWalletID}
	err = executor.Execute(execCtx, in)
	if err != nil {
		record, _ = j.GetOnchain(record.ID)
		if errors.Is(err, onchain.ErrNotBroadcast) && (strings.Contains(strings.ToLower(err.Error()), "nonce already used") || strings.Contains(strings.ToLower(err.Error()), "message already received")) {
			if closeErr := j.AdvanceOnchain(record.ID, "failed", "confirmed", ""); closeErr != nil {
				return nil, closeErr
			}
			record, _ = j.GetOnchain(record.ID)
			return claimResult("already claimed", record, ""), nil
		}
		if errors.Is(err, onchain.ErrGas) {
			return nil, output.Wrap(output.CodeInsufficientGas, err, fmt.Sprintf("destination chain %d needs native gas; nothing broadcast", chain))
		}
		return nil, executionError(err, "claim")
	}
	record, err = j.GetOnchain(record.ID)
	if err != nil {
		return nil, err
	}
	warning := ""
	if len(record.Hashes) > 0 {
		var finished v2.FinishClaimResponse
		if err = c.Bridge(execCtx, "FinishClaim", &v2.FinishClaimRequest{OrderId: source.OrderID, TxHash: record.Hashes[len(record.Hashes)-1]}, &finished, true); err != nil {
			warning = "FinishClaim failed: " + err.Error()
		}
	}
	return claimResult("claim confirmed", record, warning), nil
}
