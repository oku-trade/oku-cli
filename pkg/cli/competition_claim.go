package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// Competition reward claims: the competition pays USDC on Base through a
// Merkl distribution. prepare reads fresh proofs from the account
// MerklService, builds one Distributor.claim call for the agent alone and
// journals it; submit re-derives that exact call from the intent before it
// signs, so nothing but the competition's USDC leaf to the agent can be sent.

const (
	competitionClaimKind  = "competition-claim"
	competitionClaimChain = uint64(8453)
	// USDC on Base: the only token the competition pays.
	competitionRewardToken = "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"
	// Merkl's Distributor proxy, the same address on every chain Merkl runs on.
	merklDistributor = "0x3Ef3D8bA38EBe18DB133cEc108f4D14CE00Dd9Ae"
	// Merkl roots rotate: proofs this old are re-fetched by preparing again.
	competitionClaimTTL = 10 * time.Minute
)

const merklClaimABI = `[{"type":"function","name":"claim","stateMutability":"nonpayable","inputs":[{"name":"users","type":"address[]"},{"name":"tokens","type":"address[]"},{"name":"amounts","type":"uint256[]"},{"name":"proofs","type":"bytes32[][]"}],"outputs":[]}]`

var merklABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(merklClaimABI))
	if err != nil {
		panic(err)
	}
	return a
}()

// competitionLeaf is the competition's reward in a Merkl response: USDC on
// Base for the agent, with proofs and an unclaimed amount.
type competitionLeaf struct {
	Amount    *big.Int // cumulative amount the proofs commit to
	Claimed   *big.Int
	Proofs    [][32]byte
	Root      string
	Recipient string
}

func findCompetitionLeaf(resp *v2.GetUserRewardsResponse, agent string) (*competitionLeaf, error) {
	for _, chain := range resp.Chains {
		if chain.Chain == nil || uint64(chain.Chain.Id) != competitionClaimChain {
			continue
		}
		if chain.DistributorAddress != "" && !strings.EqualFold(chain.DistributorAddress, merklDistributor) {
			return nil, fmt.Errorf("Merkl reports distributor %s, expected %s", chain.DistributorAddress, merklDistributor)
		}
		for _, r := range chain.Rewards {
			if r.Token == nil || !strings.EqualFold(r.Token.Address, competitionRewardToken) {
				continue
			}
			if r.Recipient != "" && !strings.EqualFold(r.Recipient, agent) {
				return nil, fmt.Errorf("reward recipient %s is not the agent", r.Recipient)
			}
			amount, ok1 := new(big.Int).SetString(orZero(r.Amount), 10)
			claimed, ok2 := new(big.Int).SetString(orZero(r.Claimed), 10)
			if !ok1 || !ok2 || amount.Sign() < 0 || claimed.Sign() < 0 {
				return nil, errors.New("Merkl returned a malformed amount")
			}
			if amount.Cmp(claimed) <= 0 || len(r.Proofs) == 0 {
				return nil, nil
			}
			proofs := make([][32]byte, len(r.Proofs))
			for i, p := range r.Proofs {
				b := common.FromHex(p)
				if !strings.HasPrefix(p, "0x") || len(b) != 32 {
					return nil, errors.New("Merkl returned a malformed proof")
				}
				copy(proofs[i][:], b)
			}
			return &competitionLeaf{Amount: amount, Claimed: claimed, Proofs: proofs, Root: r.Root, Recipient: agent}, nil
		}
	}
	return nil, nil
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func encodeMerklClaim(agent string, amount *big.Int, proofs [][32]byte) ([]byte, error) {
	return merklABI.Pack("claim", []common.Address{common.HexToAddress(agent)}, []common.Address{common.HexToAddress(competitionRewardToken)}, []*big.Int{amount}, [][][32]byte{proofs})
}

// verifyCompetitionClaim decodes the sealed template and requires it to be
// exactly one claim of the competition's USDC, for the agent, to the
// distributor, with no value.
func verifyCompetitionClaim(r *journal.OnchainRecord, agent string) error {
	if r.ChainID != competitionClaimChain || len(r.Templates) != 1 || !strings.EqualFold(r.Sender, agent) || !strings.EqualFold(r.Recipient, agent) || !strings.EqualFold(r.Token, competitionRewardToken) {
		return errors.New("claim intent chain, wallet or token differs")
	}
	t := r.Templates[0]
	if t.ChainID != competitionClaimChain || !strings.EqualFold(t.To, merklDistributor) || t.Value != "0x0" {
		return errors.New("claim target or value differs")
	}
	data, err := decodeHexData(t.Data)
	if err != nil || len(data) < 4 {
		return errors.New("claim calldata malformed")
	}
	m, err := merklABI.MethodById(data[:4])
	if err != nil || m.Name != "claim" {
		return errors.New("calldata is not a Merkl claim")
	}
	args, err := m.Inputs.Unpack(data[4:])
	if err != nil || len(args) != 4 {
		return errors.New("claim calldata malformed")
	}
	users, _ := args[0].([]common.Address)
	tokens, _ := args[1].([]common.Address)
	amounts, _ := args[2].([]*big.Int)
	proofs, _ := args[3].([][][32]byte)
	if len(users) != 1 || len(tokens) != 1 || len(amounts) != 1 || len(proofs) != 1 {
		return errors.New("claim must settle exactly one leaf")
	}
	if !strings.EqualFold(users[0].Hex(), agent) || !strings.EqualFold(tokens[0].Hex(), competitionRewardToken) || amounts[0].String() != r.Amount {
		return errors.New("claim user, token or amount differs from the intent")
	}
	again, err := encodeMerklClaim(agent, amounts[0], proofs[0])
	if err != nil || !strings.EqualFold(hexData(again), t.Data) {
		return errors.New("claim calldata is not canonical")
	}
	return nil
}

func competitionClaimResult(r *journal.OnchainRecord, preview map[string]any) *result {
	data := map[string]any{"intent": r}
	if preview != nil {
		data["preview"] = preview
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "competition claim %s on Base to %s\n", r.ID, r.Recipient)
		for _, k := range sortedKeys(preview) {
			fmt.Fprintf(w, "  %s: %v\n", k, preview[k])
		}
		fmt.Fprintf(w, "Digest: %s\nState: %s\n", r.Digest, r.State)
		if len(r.Hashes) > 0 {
			fmt.Fprintf(w, "Tx: %s\n", r.Hashes[len(r.Hashes)-1])
		}
		if r.State == "prepared" {
			fmt.Fprintf(w, "Submit with: oku competition claim submit --intent %s --digest %s\n", r.ID, r.Digest)
		}
	}}
}

func formatUSDC(raw *big.Int) string {
	q, m := new(big.Int).QuoRem(raw, big.NewInt(1_000_000), new(big.Int))
	frac := strings.TrimRight(fmt.Sprintf("%06d", m.Int64()), "0")
	if frac == "" {
		return q.String()
	}
	return q.String() + "." + frac
}

func (a *App) cmdCompetitionClaimPrepare(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	agent := strings.ToLower(w.agent)
	ctx, cancel := e.requestCtx()
	defer cancel()
	var resp v2.GetUserRewardsResponse
	if err := e.client(w.profile).Merkl(ctx, "GetUserRewards", &v2.GetUserRewardsRequest{Address: agent, ChainIds: []int64{int64(competitionClaimChain)}, Fresh: true}, &resp, false); err != nil {
		return nil, apiError(err, "GetUserRewards")
	}
	leaf, err := findCompetitionLeaf(&resp, agent)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing Merkl reward")
	}
	if leaf == nil {
		return nil, output.Errorf(output.CodeNotFound, "no claimable competition USDC on Base for %s; check 'oku competition status'", agent)
	}
	data, err := encodeMerklClaim(agent, leaf.Amount, leaf.Proofs)
	if err != nil {
		return nil, err
	}
	tpl := journal.OnchainTemplate{ChainID: competitionClaimChain, To: merklDistributor, Data: hexData(data), Value: "0x0"}
	simulated := true
	if err := a.httpRPC().SimulateSequence(ctx, competitionClaimChain, agent, []journal.OnchainTemplate{tpl}); err != nil {
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
	unclaimed := new(big.Int).Sub(leaf.Amount, leaf.Claimed)
	now := a.Now().UTC()
	in := journal.OnchainIntent{ID: randomID(), Kind: competitionClaimKind, ChainID: competitionClaimChain, Sender: agent, Recipient: agent, Token: competitionRewardToken, Amount: leaf.Amount.String(), Templates: []journal.OnchainTemplate{tpl}, ExpiresAt: now.Add(competitionClaimTTL).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano)}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	r, err := j.CreateOnchain(in)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentConflict, err, "creating claim intent")
	}
	if err := verifyCompetitionClaim(r, agent); err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "sealed claim")
	}
	preview := map[string]any{"claimable_usdc": formatUSDC(unclaimed), "cumulative_raw": leaf.Amount.String(), "distributor": merklDistributor, "merkle_root": leaf.Root, "simulated": simulated, "expires_at": in.ExpiresAt}
	return competitionClaimResult(r, preview), nil
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
	if err := verifyCompetitionClaim(r, agent); err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing unverified claim")
	}
	unlock, err := j.LockChain(agent, r.ChainID)
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

func hexData(b []byte) string { return "0x" + hex.EncodeToString(b) }
