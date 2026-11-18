package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/merkl"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// Competition reward claims: the competition pays USDC on Base through a
// Merkl distribution. prepare reads fresh proofs from the account
// MerklService, builds one Distributor.claim call for the agent alone and
// journals it; submit re-derives that exact call from the intent before it
// signs. A cumulative USDC leaf can settle more than the competition award.

const (
	competitionClaimKind = "competition-claim"
	// Merkl roots rotate: proofs this old are re-fetched by preparing again.
	competitionClaimTTL = 10 * time.Minute
)

func competitionClaimResult(r *journal.OnchainRecord, preview map[string]any) *result {
	claimable := "unknown" // Older intents did not record an unclaimed balance.
	unclaimed, haveUnclaimed := new(big.Int).SetString(r.ClaimableAmount, 10)
	if haveUnclaimed {
		claimable = formatUnits(unclaimed, 6)
	}
	cumulative := "unknown"
	if raw, ok := new(big.Int).SetString(r.Amount, 10); ok {
		cumulative = formatUnits(raw, 6)
	}
	data := map[string]any{"intent": r, "claimable_usdc": claimable, "cumulative_merkl_usdc": cumulative}
	if preview != nil {
		preview["claimable_usdc"] = claimable
		preview["cumulative_raw"] = r.Amount
		data["preview"] = preview
	}
	var scope string
	if award, ok := new(big.Int).SetString(r.CompetitionAwardAmount, 10); ok && haveUnclaimed {
		data["competition_award_usdc"] = formatUnits(award, 6)
		scope = fmt.Sprintf("Competition reports %s USDC claimable. Merkl settles the wallet's USDC across campaigns.", formatUnits(award, 6))
		if extra := new(big.Int).Sub(unclaimed, award); extra.Sign() > 0 {
			scope += fmt.Sprintf(" This claim also settles %s USDC from other campaigns beyond the reported competition award.", formatUnits(extra, 6))
		}
		data["claim_scope"] = scope
		if preview != nil {
			preview["claim_scope"] = scope
		}
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "competition claim %s on Base to %s\n", r.ID, r.Recipient)
		fmt.Fprintf(w, "  Claims %s USDC (cumulative Merkl amount %s USDC)\n", claimable, cumulative)
		if scope != "" && preview == nil {
			fmt.Fprintf(w, "  %s\n", scope)
		}
		for _, k := range sortedKeys(preview) {
			fmt.Fprintf(w, "  %s: %v\n", k, preview[k])
		}
		fmt.Fprintf(w, "Digest: %s\nState: %s\n", r.Digest, r.State)
		if len(r.Hashes) > 0 {
			fmt.Fprintf(w, "Tx: %s\n", r.Hashes[len(r.Hashes)-1])
		}
		if r.State == "prepared" {
			if r.CompetitionAwardAmount == "" {
				fmt.Fprintln(w, "Prepare again to verify competition eligibility before submitting.")
			} else {
				fmt.Fprintf(w, "Submit with: oku competition claim submit --intent %s --digest %s\n", r.ID, r.Digest)
			}
		}
	}}
}

func (a *App) cmdCompetitionClaimPrepare(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	agent := strings.ToLower(w.agent)
	award, err := competitionClaimableAward(e, agent)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	var resp v2.GetUserRewardsResponse
	err = e.client(w.profile).Merkl(ctx, "GetUserRewards", &v2.GetUserRewardsRequest{Address: agent, ChainIds: []int64{int64(merkl.CompetitionChain)}, Fresh: true}, &resp, false)
	cancel()
	if err != nil {
		return nil, apiError(err, "GetUserRewards")
	}
	leaf, err := merkl.FindCompetitionLeaf(&resp, agent)
	if errors.Is(err, merkl.ErrProofsUnavailable) {
		return nil, output.Wrap(output.CodeUpstream, err, "competition reward not yet claimable")
	}
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing Merkl reward")
	}
	if leaf == nil {
		return nil, output.Errorf(output.CodeNotFound, "no claimable competition USDC on Base for %s; check 'oku competition status'", agent)
	}
	claimable := new(big.Int).Sub(leaf.Amount, leaf.Claimed)
	if claimable.Cmp(award) < 0 {
		return nil, output.Errorf(output.CodePayloadMismatch, "Merkl's unclaimed Base USDC does not cover the reported competition award; try preparing again after the reward is published")
	}
	data, err := merkl.EncodeCompetitionClaim(agent, leaf.Amount, leaf.Proofs)
	if err != nil {
		return nil, err
	}
	tpl := journal.OnchainTemplate{ChainID: merkl.CompetitionChain, To: merkl.Distributor, Data: hexutil.Encode(data), Value: "0x0"}
	simulated := true
	simCtx, simCancel := e.requestCtx()
	err = a.httpRPC().SimulateSequence(simCtx, merkl.CompetitionChain, agent, []journal.OnchainTemplate{tpl})
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
		ID: randomID(), Kind: competitionClaimKind, ChainID: merkl.CompetitionChain,
		Sender: agent, Recipient: agent, Token: merkl.CompetitionToken,
		Amount: leaf.Amount.String(), ClaimableAmount: claimable.String(), CompetitionAwardAmount: award.String(),
		Templates: []journal.OnchainTemplate{tpl}, ExpiresAt: now.Add(competitionClaimTTL).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano),
	}
	if err := merkl.VerifyCompetitionClaim(in, agent); err != nil {
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
	return competitionClaimResult(r, preview), nil
}

func competitionClaimableAward(e *env, agent string) (*big.Int, error) {
	var participant v2.GetParticipantResponse
	if err := e.competitionCall("GetParticipant", &v2.GetParticipantRequest{Wallet: agent}, &participant); err != nil {
		return nil, err
	}
	base := fmt.Sprintf("eip155:%d", merkl.CompetitionChain)
	award := new(big.Int)
	if participant.Registered {
		for _, reward := range participant.Rewards {
			token := reward.GetToken()
			if reward.GetState() != v2.CompetitionRewardState_COMPETITION_REWARD_STATE_CLAIMABLE ||
				token.GetChain() != base || !strings.EqualFold(token.GetAddress(), merkl.CompetitionToken) {
				continue
			}
			// GetParticipant reports token units. Merkl reports raw base units.
			amount, err := parseNonnegativeUnits(reward.GetAmount(), 6)
			if err != nil || token.GetDecimals() != 6 {
				return nil, output.Errorf(output.CodeUpstream, "GetParticipant returned an invalid Base USDC award")
			}
			award.Add(award, amount)
		}
	}
	if award.Sign() == 0 {
		return nil, output.Errorf(output.CodeNotFound, "no claimable competition USDC on Base for %s; check 'oku competition status'", agent)
	}
	if participant.Claim.GetChain() != base || !strings.EqualFold(participant.Claim.GetDistributor(), merkl.Distributor) {
		return nil, output.Errorf(output.CodePayloadMismatch, "GetParticipant did not report the expected Merkl distributor on Base")
	}
	return award, nil
}

func (a *App) cmdCompetitionClaimSubmit(e *env) (*result, error) {
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
	if r.Kind != competitionClaimKind || !strings.EqualFold(r.Sender, agent) {
		return nil, output.Errorf(output.CodeIntentConflict, "intent does not belong to this agent or command")
	}
	if r.Digest != str(e, "digest") {
		return nil, output.Errorf(output.CodePayloadMismatch, "digest mismatch")
	}
	if r.State != "prepared" {
		return nil, output.Errorf(output.CodeIntentState, "intent is %s; never resubmit; use competition claim status", r.State)
	}
	unlock, err := j.LockChain(agent, merkl.CompetitionChain)
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
	if r.CompetitionAwardAmount == "" {
		return nil, output.Errorf(output.CodeIntentState, "claim intent has no verified competition award; run competition claim prepare again")
	}
	if err := merkl.VerifyCompetitionClaim(r.OnchainIntent, agent); err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing unverified claim")
	}
	if expires, perr := time.Parse(time.RFC3339Nano, r.ExpiresAt); perr != nil || !a.Now().Before(expires) {
		return nil, output.Errorf(output.CodeIntentState, "prepared claim expired (Merkl roots rotate); run competition claim prepare again")
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
		return nil, executionError(err, "competition claim", r.ChainID, "native")
	}
	if r, err = j.GetOnchain(r.ID); err != nil {
		return nil, err
	}
	return competitionClaimResult(r, nil), nil
}

func (a *App) cmdCompetitionClaimStatus(e *env) (*result, error) {
	res, err := a.cmdFundsStatus(e, competitionClaimKind)
	if err != nil {
		return nil, err
	}
	if r, ok := res.data.(*journal.OnchainRecord); ok {
		return competitionClaimResult(r, nil), nil
	}
	return res, nil
}

func (a *App) cmdCompetitionClaimReconcile(e *env) (*result, error) {
	return a.cmdFundsReconcile(e, competitionClaimKind, a.cmdCompetitionClaimStatus)
}
