package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

const arbitrumUSDC = "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"
const hyperliquidBridge2 = "0x2Df1c51E09aECF9cacB7bc98cB1742757f163dF7"

func forbiddenSendRecipient(recipient, agent string) bool {
	return strings.EqualFold(recipient, hyperliquidBridge2) || strings.EqualFold(recipient, agent)
}

const depositNext = "After this transfer confirms, the master must deposit to Hyperliquid from its own wallet at https://oku.trade/perps. For HyperUnit use 'oku deposit address'. Never send agent USDC to the Hyperliquid bridge."

// executionError maps executor outcomes to CLI errors. Only ErrNotBroadcast
// proves nothing reached the chain on that attempt; everything else after a
// handoff stays OUTCOME_UNKNOWN until status or reconcile resolves it.
func executionError(err error, kind string) error {
	switch {
	case errors.Is(err, onchain.ErrNotBroadcast) && errors.Is(err, onchain.ErrGas):
		return output.Wrap(output.CodeInsufficientGas, err, "nothing broadcast; fund the agent with native gas and prepare a new "+kind)
	case errors.Is(err, onchain.ErrNotBroadcast) && errors.Is(err, onchain.ErrBalance):
		return output.Wrap(output.CodeInsufficientBalance, err, "nothing broadcast; prepare a new "+kind)
	case errors.Is(err, onchain.ErrNotBroadcast):
		return output.Wrap(output.CodeRejected, err, "preflight failed; check status (earlier steps may have confirmed) and prepare a new "+kind)
	case errors.Is(err, onchain.ErrReceiptUpstream):
		return output.Wrap(output.CodeUpstream, err, "transaction hash persisted; run status to retry receipt lookup; never resubmit")
	case errors.Is(err, onchain.ErrReverted):
		return output.Wrap(output.CodeRejected, err, "onchain transaction reverted")
	}
	return output.Wrap(output.CodeOutcomeUnknown, err, "never resubmit; run status, or reconcile with the transaction hash")
}

// onchainExecBudget bounds broadcasting plus confirming every step of an
// onchain intent, independent of the short per-request --timeout.
const onchainExecBudget = 10 * time.Minute

func sendPrepareFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID, e.g. 42161 (needs an RPC: built-in or OKU_RPC_URL_<id>)"}, &urfave.StringFlag{Name: "token", Usage: "ERC-20 contract address or native"}, &urfave.StringFlag{Name: "amount", Usage: "exact decimal amount"}, &urfave.StringFlag{Name: "to", Usage: "recipient EVM address (defaults to profile master)"}, &urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"}}
}
func depositPrepareFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "amount", Usage: "USDC amount on Arbitrum"}, &urfave.StringFlag{Name: "to", Usage: "must equal profile master address if supplied"}, &urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"}}
}
func parseUnits(s string, decimals int) (*big.Int, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return nil, errors.New("invalid decimal amount")
	}
	for _, p := range parts {
		if p == "" {
			return nil, errors.New("invalid decimal amount")
		}
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return nil, errors.New("invalid decimal amount")
			}
		}
	}
	if len(parts) == 2 && len(parts[1]) > decimals {
		return nil, errors.New("amount has excessive precision")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	value, ok := new(big.Int).SetString(parts[0]+fraction+strings.Repeat("0", decimals-len(fraction)), 10)
	if !ok || value.Sign() <= 0 || value.BitLen() > 256 {
		return nil, errors.New("invalid or nonpositive amount")
	}
	return value, nil
}
func (a *App) cmdFundsPrepare(e *env, kind string) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	chain, token, recipient := uint64(42161), arbitrumUSDC, w.profile.Account
	if kind == "send" {
		chain, err = strconv.ParseUint(str(e, "chain"), 10, 64)
		if err != nil || !onchain.Allowed(chain) {
			return nil, output.Errorf(output.CodeInvalidArgument, "--chain must be an EVM chain ID")
		}
		if _, rerr := onchain.RPCURL(a.Getenv, chain); rerr != nil {
			return nil, output.Wrap(output.CodeConfigError, rerr, "chain RPC")
		}
		token = str(e, "token")
		if token != "native" && !onchain.ValidAddress(token) {
			return nil, output.Errorf(output.CodeInvalidArgument, "--token must be native or an EVM token address")
		}
	}
	if to := str(e, "to"); to != "" {
		if !onchain.ValidAddress(to) {
			return nil, output.Errorf(output.CodeInvalidArgument, "invalid recipient EVM address")
		}
		recipient = to
	}
	if forbiddenSendRecipient(recipient, w.agent) {
		return nil, output.Errorf(output.CodeInvalidArgument, "recipient is the agent or a Hyperliquid deposit contract")
	}
	if kind == "deposit" && !strings.EqualFold(recipient, w.profile.Account) {
		return nil, output.Errorf(output.CodeInvalidArgument, "deposit recipient must equal profile master EOA")
	}
	decimals := 18
	if token != "native" {
		decimals = 6
		if kind == "send" {
			rpc := onchain.HTTPRPC{Getenv: a.Getenv}
			ctx, cancel := e.requestCtx()
			defer cancel()
			raw, err := rpc.Call(ctx, chain, token, "0x313ce567")
			if err != nil {
				return nil, output.Wrap(output.CodeUpstream, err, "reading token decimals")
			}
			if !raw.IsInt64() || raw.Int64() < 0 || raw.Int64() > 36 {
				return nil, output.Errorf(output.CodeInvalidArgument, "unsupported token decimals")
			}
			decimals = int(raw.Int64())
		}
	}
	amount, err := parseUnits(str(e, "amount"), decimals)
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidDecimal, err, "amount")
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	t := journal.OnchainTemplate{ChainID: chain, To: recipient, Value: onchain.Hex(amount)}
	if token != "native" {
		t.To = token
		t.Value = "0x0"
		t.Data = "0xa9059cbb000000000000000000000000" + strings.ToLower(recipient[2:]) + fmt.Sprintf("%064x", amount)
		t.Token = token
		t.Amount = onchain.Hex(amount)
		t.Decimals = decimals
	}
	in := journal.OnchainIntent{ID: id, Kind: kind, ChainID: chain, Sender: w.agent, Recipient: recipient, Token: token, Amount: str(e, "amount"), Templates: []journal.OnchainTemplate{t}, CreatedAt: a.Now().UTC().Format(time.RFC3339Nano)}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	r, err := j.CreateOnchain(in)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentConflict, err, "creating onchain intent")
	}
	return fundsResult(r), nil
}
func fundsResult(r *journal.OnchainRecord) *result {
	if r.Kind == "deposit" {
		r.NextStep = depositNext
	}
	return &result{data: r, human: func(out io.Writer) {
		fmt.Fprintf(out, "%s %s: %s on chain %d to %s\nDigest: %s\nState: %s\n", r.Kind, r.ID, r.Amount, r.ChainID, r.Recipient, r.Digest, r.State)
		if r.Kind == "deposit" {
			fmt.Fprintln(out, depositNext)
		} else if r.NextStep != "" {
			fmt.Fprintln(out, r.NextStep)
		} else if r.State == "prepared" {
			fmt.Fprintf(out, "Submit with: oku send submit --intent %s --digest %s\n", r.ID, r.Digest)
		}
	}}
}
func (a *App) cmdSendPrepare(e *env) (*result, error)    { return a.cmdFundsPrepare(e, "send") }
func (a *App) cmdDepositPrepare(e *env) (*result, error) { return a.cmdFundsPrepare(e, "deposit") }
func (a *App) cmdFundsSubmit(e *env, kind string) (*result, error) {
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
	if errors.Is(err, journal.ErrNotFound) {
		return nil, output.Errorf(output.CodeNotFound, "intent not found")
	}
	if err != nil {
		return nil, err
	}
	if r.Kind != kind || !strings.EqualFold(r.Sender, w.agent) {
		return nil, output.Errorf(output.CodeIntentConflict, "intent does not belong to this agent or command")
	}
	if r.Digest != str(e, "digest") {
		return nil, output.Errorf(output.CodePayloadMismatch, "digest mismatch")
	}
	if r.State != "prepared" {
		return nil, output.Errorf(output.CodeIntentState, "intent is %s; never resubmit; use status", r.State)
	}
	if forbiddenSendRecipient(r.Recipient, w.agent) {
		return nil, output.Errorf(output.CodePayloadMismatch, "recipient is the agent or a Hyperliquid deposit contract")
	}
	if kind == "deposit" && (r.ChainID != 42161 || !strings.EqualFold(r.Token, arbitrumUSDC) || !strings.EqualFold(r.Recipient, w.profile.Account)) {
		return nil, output.Errorf(output.CodePayloadMismatch, "deposit must send Arbitrum USDC to master")
	}
	if len(r.Templates) != 1 || !verifySendTemplate(r.OnchainIntent) {
		return nil, output.Errorf(output.CodePayloadMismatch, "transaction template does not match intent")
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
	pc, err := e.privyClient(w.profile)
	if err != nil {
		return nil, err
	}
	_, sec, _, err := e.unlockCredential(w.profile.Name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
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
		return nil, output.Errorf(output.CodeSignerUnavailable, "authenticated Privy wallet differs from enrolled agent")
	}
	if err = j.BeginOnchain(r.ID, r.Digest, w.agent); err != nil {
		if errors.Is(err, journal.ErrUnresolved) {
			return nil, output.Wrap(output.CodeOutcomeUnknown, err, "reconcile the blocking onchain intent")
		}
		return nil, output.Wrap(output.CodeIntentState, err, "starting onchain intent")
	}
	execCtx, execCancel := context.WithTimeout(e.ctx, onchainExecBudget)
	defer execCancel()
	executor := onchain.Executor{Getenv: a.Getenv, RPC: onchain.HTTPRPC{Getenv: a.Getenv}, Sender: ps, Journal: j, WalletID: s.md.PrivyWalletID}
	if err = executor.Execute(execCtx, r.OnchainIntent); err != nil {
		return nil, executionError(err, "send")
	}
	r, err = j.GetOnchain(r.ID)
	if err != nil {
		return nil, err
	}
	return fundsResult(r), nil
}
func verifySendTemplate(in journal.OnchainIntent) bool {
	t := in.Templates[0]
	if t.ChainID != in.ChainID || !onchain.Allowed(t.ChainID) || !onchain.ValidAddress(in.Recipient) {
		return false
	}
	decimals := 18
	if in.Token != "native" {
		decimals = t.Decimals
		if decimals < 0 || decimals > 36 {
			return false
		}
		if in.Kind == "deposit" && decimals != 6 {
			return false
		}
	}
	amount, err := parseUnits(in.Amount, decimals)
	if err != nil {
		return false
	}
	if in.Token == "native" {
		return strings.EqualFold(t.To, in.Recipient) && t.Data == "" && t.Value == onchain.Hex(amount) && t.Token == ""
	}
	return strings.EqualFold(t.To, in.Token) && t.Value == "0x0" && strings.EqualFold(t.Data, "0xa9059cbb000000000000000000000000"+in.Recipient[2:]+fmt.Sprintf("%064x", amount)) && strings.EqualFold(t.Token, in.Token) && t.Amount == onchain.Hex(amount) && t.Spender == ""
}
func (a *App) cmdSendSubmit(e *env) (*result, error)    { return a.cmdFundsSubmit(e, "send") }
func (a *App) cmdDepositSubmit(e *env) (*result, error) { return a.cmdFundsSubmit(e, "deposit") }
func (a *App) cmdFundsStatus(e *env, kind string) (*result, error) {
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
	if r.Kind != kind || !strings.EqualFold(r.Sender, w.agent) {
		return nil, output.Errorf(output.CodeNotFound, "intent not found")
	}
	// A batch whose submit stopped before it was found on chain: locate it by
	// the reserved nonce. Absence never closes it; only a found, matching
	// transaction and its receipt resolve the intent.
	if r.Batch != nil && (r.State == "unknown" || r.State == "sending") && len(r.Hashes) == 0 {
		ctx, cancel := e.requestCtx()
		defer cancel()
		rpc := onchain.HTTPRPC{Getenv: a.Getenv}
		hash, err := onchain.LocateBatch(ctx, r.OnchainIntent, *r.Batch, rpc)
		if err == nil {
			if err = j.AdvanceOnchain(r.ID, r.State, "unknown", hash); err != nil {
				return nil, err
			}
			if r, err = j.GetOnchain(r.ID); err != nil {
				return nil, err
			}
		} else if !onchain.IsBatchNotMined(err) {
			return nil, output.Wrap(output.CodeUpstream, err, "batch lookup")
		}
	}
	if r, err = a.resolveReceipt(e, j, r); err != nil {
		return nil, err
	}
	return fundsResult(r), nil
}

// resolveReceipt advances a sent or unknown intent with a recorded hash from
// its confirmed receipt. A missing receipt leaves it unresolved.
func (a *App) resolveReceipt(e *env, j *journal.Journal, r *journal.OnchainRecord) (*journal.OnchainRecord, error) {
	if (r.State != "sent" && r.State != "unknown") || len(r.Hashes) == 0 {
		return r, nil
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	status, err := rpc.Receipt(ctx, r.ChainID, r.Hashes[len(r.Hashes)-1])
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "receipt lookup")
	}
	if status == nil {
		return r, nil
	}
	next := "failed"
	// One batch hash covers every call.
	if *status && (len(r.Hashes) == len(r.Templates) || r.Batch != nil) {
		next = "confirmed"
	}
	if *status && next != "confirmed" {
		// Earlier steps confirmed but later ones were never recorded.
		// Absence of a later transaction is not proof it cannot land, so
		// this stays unresolved until reconciled with a hash.
		next = "unknown"
	}
	if err = j.AdvanceOnchain(r.ID, r.State, next, ""); err != nil {
		return nil, err
	}
	return j.GetOnchain(r.ID)
}
func (a *App) cmdSendStatus(e *env) (*result, error)    { return a.cmdFundsStatus(e, "send") }
func (a *App) cmdDepositStatus(e *env) (*result, error) { return a.cmdFundsStatus(e, "deposit") }
