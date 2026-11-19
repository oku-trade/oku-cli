package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/mcp"
	"github.com/oku-trade/oku-cli/pkg/merkl"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

const (
	merklClaimKind           = "merkl-claim"
	merklClaimPrepareCommand = "oku merkl claim prepare"
	// Merkl roots rotate: proofs this old are re-fetched by preparing again.
	merklClaimTTL = 10 * time.Minute
)

func (a *App) merklClaimCommands() map[string]*handler {
	return map[string]*handler{
		"merkl claim prepare":   {access: mcp.AccessRead, summary: "Preview claiming all the agent's unclaimed Merkl USDC on Base across every campaign (never signs)", run: a.cmdMerklClaimPrepare},
		"merkl claim submit":    {access: mcp.AccessMutation, summary: "Submit an approved claim for all the agent's unclaimed Merkl USDC on Base", flags: func() []urfave.Flag { return submitFlagsFor("merkl claim") }, run: a.cmdMerklClaimSubmit, required: map[string]bool{"intent": true, "digest": true}},
		"merkl claim status":    {access: mcp.AccessRead, summary: "Check claim status and show the whole Merkl USDC balance recorded at prepare", flags: statusFlags, run: a.cmdMerklClaimStatus, required: map[string]bool{"intent": true}},
		"merkl claim reconcile": {access: mcp.AccessMutation, summary: "Verify and attach a recovered Merkl claim transaction hash", flags: reconcileFlags, run: a.cmdMerklClaimReconcile, required: map[string]bool{"intent": true, "tx-hash": true}},
	}
}

type merklClaimAmounts struct {
	unclaimed, cumulative *big.Int
}

func verifyMerklClaim(in journal.OnchainIntent, agent string) (merklClaimAmounts, error) {
	if err := merkl.VerifyUSDCClaim(in, agent); err != nil {
		return merklClaimAmounts{}, err
	}
	unclaimed, ok1 := new(big.Int).SetString(in.ClaimableAmount, 10)
	entitlement, ok2 := new(big.Int).SetString(in.Amount, 10)
	if !ok1 || !ok2 || unclaimed.Sign() <= 0 || unclaimed.Cmp(entitlement) > 0 {
		return merklClaimAmounts{}, errors.New("invalid Merkl USDC balances in the claim intent")
	}
	return merklClaimAmounts{unclaimed, entitlement}, nil
}

func (m merklClaimAmounts) result(r *journal.OnchainRecord, preview map[string]any) *result {
	claimable, cumulative := formatUnits(m.unclaimed, 6), formatUnits(m.cumulative, 6)
	scope := fmt.Sprintf("Claims all unclaimed Merkl USDC on Base for %s: %s USDC (cumulative %s USDC). Amounts are from prepare and cover every Merkl campaign.", r.Sender, claimable, cumulative)
	amounts := map[string]any{"merkl_unclaimed_usdc": claimable, "cumulative_merkl_usdc": cumulative, "claim_scope": scope}
	data := maps.Clone(amounts)
	data["intent"] = r
	data["recipient_bound"] = true
	if preview != nil {
		p := maps.Clone(preview)
		maps.Copy(p, amounts)
		p["cumulative_raw"] = r.Amount
		data["preview"] = p
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "merkl claim %s on Base\n", r.ID)
		fmt.Fprintf(w, "  Bound payout recipient: %s\n", r.Recipient)
		fmt.Fprintf(w, "  %s\n", scope)
		for _, k := range sortedKeys(preview) {
			fmt.Fprintf(w, "  %s: %v\n", k, preview[k])
		}
		fmt.Fprintf(w, "Digest: %s\nState: %s\n", r.Digest, r.State)
		if len(r.Hashes) > 0 {
			fmt.Fprintf(w, "Tx: %s\n", r.Hashes[len(r.Hashes)-1])
		}
		if r.State == "prepared" {
			fmt.Fprintf(w, "Submit with: oku merkl claim submit --intent %s --digest %s\n", r.ID, r.Digest)
		} else if r.NextStep != "" {
			fmt.Fprintln(w, r.NextStep)
		}
	}}
}

func (a *App) cmdMerklClaimPrepare(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	agent := strings.ToLower(w.agent)
	ctx, cancel := e.requestCtx()
	var resp v2.GetUserRewardsResponse
	err = e.client(w.profile).Merkl(ctx, "GetUserRewards", &v2.GetUserRewardsRequest{Address: agent, ChainIds: []int64{int64(merkl.BaseChainID)}, Fresh: true}, &resp, false)
	cancel()
	if err != nil {
		return nil, apiError(err, "GetUserRewards")
	}
	leaf, err := merkl.FindUSDCLeaf(&resp, agent)
	if errors.Is(err, merkl.ErrProofsUnavailable) {
		return nil, output.Wrap(output.CodeUpstream, err, "Merkl USDC not yet claimable")
	}
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing Merkl reward")
	}
	if leaf == nil {
		return nil, output.Errorf(output.CodeNotFound, "no unclaimed Merkl USDC on Base for %s", agent)
	}
	claimable := new(big.Int).Sub(leaf.Amount, leaf.Claimed)
	data, err := merkl.EncodeUSDCClaim(agent, leaf.Amount, leaf.Proofs)
	if err != nil {
		return nil, err
	}
	tpl := journal.OnchainTemplate{ChainID: merkl.BaseChainID, To: merkl.Distributor, Data: hexutil.Encode(data), Value: "0x0"}
	simulated := true
	simCtx, simCancel := e.requestCtx()
	err = a.httpRPC().SimulateSequence(simCtx, merkl.BaseChainID, agent, []journal.OnchainTemplate{tpl})
	simCancel()
	if err != nil {
		var revert *onchain.SimulationRevert
		switch {
		case errors.Is(err, onchain.ErrSimulationUnsupported):
			simulated = false
		case errors.As(err, &revert):
			return nil, output.Wrap(output.CodeRejected, err, "claim simulation failed, nothing was journaled")
		default:
			return nil, output.Wrap(output.CodeUpstream, err, "simulating the claim")
		}
	}
	now := a.Now().UTC()
	in := journal.OnchainIntent{
		ID: randomID(), Kind: merklClaimKind, ChainID: merkl.BaseChainID,
		Sender: agent, Recipient: agent, Token: merkl.BaseUSDC,
		Amount: leaf.Amount.String(), ClaimableAmount: claimable.String(),
		Templates: []journal.OnchainTemplate{tpl}, ExpiresAt: now.Add(merklClaimTTL).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano),
	}
	amounts, err := verifyMerklClaim(in, agent)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "verifying claim intent")
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	r, err := j.CreateOnchain(in)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentConflict, err, "creating claim intent")
	}
	preview := map[string]any{"distributor": merkl.Distributor, "merkle_root": leaf.Root, "simulated": simulated, "expires_at": in.ExpiresAt}
	return amounts.result(r, preview), nil
}

func (a *App) cmdMerklClaimSubmit(e *env) (*result, error) {
	if str(e, "intent") == "" || str(e, "digest") == "" {
		return nil, output.Errorf(output.CodeUsage, "--intent and --digest required")
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	agent := strings.ToLower(w.agent)
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
	if r.Kind != merklClaimKind || !strings.EqualFold(r.Sender, agent) {
		return nil, output.Errorf(output.CodeIntentConflict, "intent does not belong to this agent or command")
	}
	if r.Digest != str(e, "digest") {
		return nil, output.Errorf(output.CodePayloadMismatch, "digest mismatch")
	}
	if r.State != "prepared" {
		return nil, output.Errorf(output.CodeIntentState, "intent is %s; never resubmit; use merkl claim status", r.State)
	}
	unlock, err := j.LockChain(agent, merkl.BaseChainID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if r, err = j.GetOnchain(r.ID); err != nil {
		return nil, err
	}
	if r.State != "prepared" || r.Digest != str(e, "digest") {
		return nil, output.Errorf(output.CodeIntentState, "intent changed while waiting for chain lock")
	}
	amounts, err := verifyMerklClaim(r.OnchainIntent, agent)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing unverified claim, prepare again and approve the new preview")
	}
	if expires, perr := time.Parse(time.RFC3339Nano, r.ExpiresAt); perr != nil || !a.Now().Before(expires) {
		return nil, output.Errorf(output.CodeIntentState, "prepared claim expired (Merkl roots rotate); run merkl claim prepare again")
	}
	ps, walletID, err := a.agentSigner(e, w)
	if err != nil {
		return nil, err
	}
	if err = j.BeginOnchain(r.ID, r.Digest, agent); err != nil {
		if errors.Is(err, journal.ErrUnresolved) {
			return nil, output.Wrap(output.CodeOutcomeUnknown, err, "reconcile the blocking onchain intent")
		}
		return nil, output.Wrap(output.CodeIntentState, err, "starting onchain intent")
	}
	execCtx, execCancel := context.WithTimeout(e.ctx, onchainExecBudget)
	defer execCancel()
	x := onchain.Executor{Getenv: a.Getenv, RPC: a.httpRPC(), Sender: ps, Journal: j, WalletID: walletID}
	if err = x.Execute(execCtx, r.OnchainIntent); err != nil {
		return nil, executionError(err, "merkl claim", r.ChainID, "native")
	}
	if r, err = j.GetOnchain(r.ID); err != nil {
		return nil, err
	}
	return amounts.result(r, nil), nil
}

func (a *App) cmdMerklClaimStatus(e *env) (*result, error) {
	res, err := a.cmdFundsStatus(e, merklClaimKind)
	if err != nil {
		return nil, err
	}
	r, ok := res.data.(*journal.OnchainRecord)
	if !ok {
		return nil, errors.New("missing Merkl claim record")
	}
	amounts, err := verifyMerklClaim(r.OnchainIntent, r.Sender)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "unverified Merkl claim")
	}
	return amounts.result(r, nil), nil
}

func (a *App) cmdMerklClaimReconcile(e *env) (*result, error) {
	return a.cmdFundsReconcile(e, merklClaimKind, a.cmdMerklClaimStatus)
}
