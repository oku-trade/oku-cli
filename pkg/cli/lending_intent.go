package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/lending"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// Journal kinds of lending intents.
const (
	kindEarn   = "earn"
	kindBorrow = "borrow"
	kindLoop   = "loop"
)

// lendingQuoteTTL bounds how long prepared lending calldata stays valid:
// share-price bounds and allowances drift with the chain.
const lendingQuoteTTL = 10 * time.Minute

// lendingPrepared is what prepare shows for review: the intent record plus
// a human summary of the action and its projected risk.
type lendingPrepared struct {
	*journal.OnchainRecord
	Plan    lending.Plan   `json:"plan"`
	Preview map[string]any `json:"preview"`
	Calls   []lendingCall  `json:"calls"`
}

type lendingCall struct {
	To   string `json:"to"`
	Data string `json:"data"`
}

// sealLending requires calls to have exactly the wanted effects under the
// plan's scope, then records them as the intent's templates with the plan
// (and its expectation) bound into the digest.
func (a *App) sealLending(e *env, w *resolvedWallet, kind string, plan lending.Plan, calls []lending.Call, want lending.Want, token, amount string, preview map[string]any) (*result, error) {
	if len(calls) == 0 {
		return nil, output.Errorf(output.CodeInvalidArgument, "nothing to do")
	}
	if err := plan.Check(calls, want); err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing unverified lending calldata")
	}
	plan.Expect = want
	simulated, err := a.simulateLending(e, w, plan.Chain, calls)
	if err != nil {
		return nil, err
	}
	preview["simulated"] = simulated
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	templates := make([]journal.OnchainTemplate, len(calls))
	shown := make([]lendingCall, len(calls))
	for i, c := range calls {
		templates[i] = journal.OnchainTemplate{ChainID: plan.Chain, To: c.To.Hex(), Data: c.Hex(), Value: "0x0"}
		shown[i] = lendingCall{c.To.Hex(), c.Hex()}
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	now := a.Now().UTC()
	in := journal.OnchainIntent{ID: id, Kind: kind, ChainID: plan.Chain, Sender: w.agent, Recipient: w.agent, Token: token, Amount: amount, Templates: templates, Lending: raw, ExpiresAt: now.Add(lendingQuoteTTL).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano)}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	r, err := j.CreateOnchain(in)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentConflict, err, "creating "+kind+" intent")
	}
	return lendingResult(r, plan, preview, shown), nil
}

// simulateLending runs the exact calls in order from the agent before
// anything is journaled, so a revert fails prepare with its reason instead of
// surfacing at submit. An RPC without eth_simulateV1 cannot check them: that
// is reported as simulated=false, never as a pass, and submit still verifies
// each step against the chain before broadcasting.
func (a *App) simulateLending(e *env, w *resolvedWallet, chain uint64, calls []lending.Call) (bool, error) {
	templates := make([]journal.OnchainTemplate, len(calls))
	for i, c := range calls {
		templates[i] = journal.OnchainTemplate{ChainID: chain, To: c.To.Hex(), Data: c.Hex(), Value: "0x0"}
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	err := a.httpRPC().SimulateSequence(ctx, chain, w.agent, templates)
	var revert *onchain.SimulationRevert
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, onchain.ErrSimulationUnsupported):
		return false, nil
	case errors.As(err, &revert):
		return false, output.Wrap(output.CodeRejected, err, "simulation failed, nothing was journaled")
	default:
		return false, output.Wrap(output.CodeUpstream, err, "simulating the lending calls")
	}
}

func lendingResult(r *journal.OnchainRecord, plan lending.Plan, preview map[string]any, calls []lendingCall) *result {
	return &result{data: lendingPrepared{OnchainRecord: r, Plan: plan, Preview: preview, Calls: calls}, human: func(w io.Writer) {
		fmt.Fprintf(w, "%s %s (%s) on chain %d: %d transaction(s)\n", r.Kind, plan.Action, plan.Protocol, r.ChainID, len(r.Templates))
		for _, k := range sortedKeys(preview) {
			fmt.Fprintf(w, "  %s: %v\n", k, preview[k])
		}
		fmt.Fprintf(w, "Digest: %s\nState: %s\n", r.Digest, r.State)
		if r.State == "prepared" {
			fmt.Fprintf(w, "Submit with: oku %s submit --intent %s --digest %s\n", r.Kind, r.ID, r.Digest)
		}
	}}
}

func lendingPlanOf(r *journal.OnchainRecord) (lending.Plan, []lending.Call, error) {
	var plan lending.Plan
	if len(r.Lending) == 0 {
		return plan, nil, errors.New("intent has no lending plan")
	}
	if err := json.Unmarshal(r.Lending, &plan); err != nil {
		return plan, nil, err
	}
	if plan.Chain != r.ChainID || !strings.EqualFold(plan.Owner.Hex(), r.Sender) {
		return plan, nil, errors.New("lending plan chain or owner differs from the intent")
	}
	calls := make([]lending.Call, len(r.Templates))
	for i, t := range r.Templates {
		if t.ChainID != r.ChainID || !onchain.ValidAddress(t.To) || t.Value != "0x0" {
			return plan, nil, errors.New("template chain, target or value is invalid")
		}
		data, err := decodeHexData(t.Data)
		if err != nil {
			return plan, nil, err
		}
		calls[i] = lending.Call{To: common.HexToAddress(t.To), Data: data, Value: new(big.Int)}
	}
	return plan, calls, nil
}

func decodeHexData(s string) ([]byte, error) {
	if !strings.HasPrefix(s, "0x") {
		return nil, errors.New("calldata must be 0x-prefixed")
	}
	return common.FromHex(s), nil
}

// cmdLendingSubmit re-verifies the sealed calldata against the bound plan,
// then executes it like lp submit: one EIP-7702 batch when the agent
// supports it, otherwise each transaction in order with a durable resume
// point after every confirmed step.
func (a *App) cmdLendingSubmit(e *env, kind string, after func(context.Context, *env, *resolvedWallet, *journal.OnchainRecord, lending.Plan) error) (*result, error) {
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
	resumable := func(r *journal.OnchainRecord) bool {
		return r.State == "sending" && r.ExecutionMode == "sequential" && r.StepIndex > 0 && !r.StepInFlight
	}
	if r.State != "prepared" && !resumable(r) {
		return nil, output.Errorf(output.CodeIntentState, "intent is %s; never resubmit; use %s status", r.State, kind)
	}
	plan, calls, err := lendingPlanOf(r)
	if err == nil {
		err = plan.Verify(calls)
	}
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing unverified lending calldata")
	}
	unlock, err := j.LockChain(w.agent, r.ChainID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if r, err = j.GetOnchain(r.ID); err != nil {
		return nil, err
	}
	resume := resumable(r)
	if r.Digest != str(e, "digest") || r.State != "prepared" && !resume {
		return nil, output.Errorf(output.CodeIntentState, "intent changed while waiting for chain lock")
	}
	// Expiry is checked under the lock and also before resuming: share-price
	// bounds and swap minimums in the remaining calls go stale either way.
	if expires, perr := time.Parse(time.RFC3339Nano, r.ExpiresAt); perr != nil || !a.Now().Before(expires) {
		if resume {
			// Close it so it no longer blocks the chain; the confirmed
			// steps stay recorded.
			if cerr := j.AdvanceOnchain(r.ID, "sending", "failed", ""); cerr != nil {
				return nil, output.Wrap(output.CodeIntentState, cerr, "closing the expired intent")
			}
			return nil, output.Errorf(output.CodeIntentState, "%s intent expired after step %d; the remaining steps were not sent and the intent is closed, prepare a new one", kind, r.StepIndex)
		}
		return nil, output.Errorf(output.CodeIntentState, "prepared %s expired; prepare a new intent", kind)
	}
	if err = a.recheckRisk(e.ctx, e, w, plan); err != nil {
		return nil, err
	}
	ps, walletID, err := a.agentSigner(e, w)
	if err != nil {
		return nil, err
	}
	execCtx, execCancel := context.WithTimeout(e.ctx, onchainExecBudget)
	defer execCancel()
	rpc := a.httpRPC()
	x := onchain.Executor{Getenv: a.Getenv, RPC: rpc, Sender: ps, Journal: j, WalletID: walletID}
	if resume {
		err = x.ExecuteSteps(execCtx, r.OnchainIntent, r.StepIndex)
	} else {
		if err = j.BeginOnchain(r.ID, r.Digest, w.agent); err != nil {
			if errors.Is(err, journal.ErrUnresolved) {
				return nil, output.Wrap(output.CodeOutcomeUnknown, err, "reconcile the blocking onchain intent")
			}
			return nil, output.Wrap(output.CodeIntentState, err, "starting onchain intent")
		}
		if err = a.lendingExecute(execCtx, j, x, rpc, r); errors.Is(err, errLendingCapability) {
			return nil, output.Wrap(output.CodeUpstream, err, "nothing broadcast; prepare a new "+kind+" intent")
		}
	}
	if err != nil {
		return nil, executionError(err, kind+" intent", r.ChainID, "")
	}
	if r, err = j.GetOnchain(r.ID); err != nil {
		return nil, err
	}
	if after != nil && r.State == "confirmed" {
		if aerr := after(execCtx, e, w, r, plan); aerr != nil {
			// The transaction confirmed; only the backend report failed.
			e.diag("warning: %s confirmed but reporting to Oku failed: %v; run %s status", kind, aerr, kind)
		}
	}
	return lendingResult(r, plan, nil, nil), nil
}

var errLendingCapability = errors.New("execution capability check failed")

func (a *App) lendingExecute(ctx context.Context, j *journal.Journal, x onchain.Executor, rpc onchain.HTTPRPC, r *journal.OnchainRecord) error {
	if len(r.Templates) == 1 {
		if err := j.SetExecutionMode(r.ID, "sequential"); err != nil {
			return err
		}
		return x.Execute(ctx, r.OnchainIntent)
	}
	allowed, err := onchain.BatchAllowed(ctx, rpc, r.ChainID, r.Sender)
	if err != nil {
		if closeErr := j.AdvanceOnchain(r.ID, "sending", "failed", ""); closeErr != nil {
			return fmt.Errorf("%w: capability check failed (%v) and closing the intent failed: %v", onchain.ErrUnknown, err, closeErr)
		}
		return fmt.Errorf("%w: %v", errLendingCapability, err)
	}
	mode := "sequential"
	if allowed {
		mode = "batch"
	}
	if err = j.SetExecutionMode(r.ID, mode); err != nil {
		return err
	}
	if allowed {
		batcher, ok := x.Sender.(onchain.BatchSender)
		if !ok {
			return fmt.Errorf("%w: signer cannot batch", onchain.ErrUnknown)
		}
		return x.ExecuteBatch(ctx, r.OnchainIntent, batcher, rpc)
	}
	return x.ExecuteSteps(ctx, r.OnchainIntent, 0)
}

func (a *App) cmdLendingStatus(e *env, kind string) (*result, error) {
	res, err := a.cmdFundsStatus(e, kind)
	if err != nil {
		return nil, err
	}
	r, ok := res.data.(*journal.OnchainRecord)
	if !ok {
		return res, nil
	}
	plan, _, err := lendingPlanOf(r)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "stored lending plan")
	}
	return lendingResult(r, plan, nil, nil), nil
}

func (a *App) cmdLendingReconcile(e *env, kind string) (*result, error) {
	return a.cmdFundsReconcile(e, kind, func(e *env) (*result, error) { return a.cmdLendingStatus(e, kind) })
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
