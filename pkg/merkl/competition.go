// Package merkl selects and verifies Merkl reward claims.
package merkl

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
)

const (
	CompetitionChain = uint64(8453)
	// CompetitionToken is USDC on Base, the only token the competition pays.
	CompetitionToken = "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"
	// Distributor is Merkl's Distributor proxy, shared across chains.
	Distributor = "0x3Ef3D8bA38EBe18DB133cEc108f4D14CE00Dd9Ae"
)

const claimABI = `[{"type":"function","name":"claim","stateMutability":"nonpayable","inputs":[{"name":"users","type":"address[]"},{"name":"tokens","type":"address[]"},{"name":"amounts","type":"uint256[]"},{"name":"proofs","type":"bytes32[][]"}],"outputs":[]}]`

var distributorABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(claimABI))
	if err != nil {
		panic(err)
	}
	return a
}()

// ErrProofsUnavailable means Merkl reports unclaimed USDC but has no proofs for it yet.
var ErrProofsUnavailable = errors.New("Merkl reports unclaimed competition USDC but has not published proofs yet; try again later")

// CompetitionLeaf is an unclaimed USDC reward on Base with proofs for its cumulative amount.
type CompetitionLeaf struct {
	Amount  *big.Int
	Claimed *big.Int
	Proofs  [][32]byte
	Root    string
}

// FindCompetitionLeaf returns nil when no matching reward has an unclaimed balance.
func FindCompetitionLeaf(resp *v2.GetUserRewardsResponse, agent string) (*CompetitionLeaf, error) {
	var leaf *CompetitionLeaf
	missingProofs := false
	for _, chain := range resp.GetChains() {
		if chain.GetChain().GetId() != int64(CompetitionChain) {
			continue
		}
		if addr := chain.GetDistributorAddress(); addr != "" && !strings.EqualFold(addr, Distributor) {
			return nil, fmt.Errorf("Merkl reports distributor %s, expected %s", addr, Distributor)
		}
		for _, r := range chain.GetRewards() {
			token := r.GetToken()
			if !strings.EqualFold(token.GetAddress(), CompetitionToken) {
				continue
			}
			if r.DistributionChainId != int64(CompetitionChain) || token.ChainId != int64(CompetitionChain) {
				return nil, fmt.Errorf("Merkl reward distribution and token must both be on Base, got chains %d and %d", r.DistributionChainId, token.ChainId)
			}
			if r.Recipient != "" && !strings.EqualFold(r.Recipient, agent) {
				return nil, fmt.Errorf("reward recipient %s is not the agent", r.Recipient)
			}
			amount, ok1 := new(big.Int).SetString(orZero(r.Amount), 10)
			claimed, ok2 := new(big.Int).SetString(orZero(r.Claimed), 10)
			if !ok1 || !ok2 || amount.Sign() < 0 || claimed.Sign() < 0 {
				return nil, errors.New("Merkl returned a malformed amount")
			}
			if amount.Cmp(claimed) <= 0 {
				continue
			}
			if len(r.Proofs) == 0 {
				missingProofs = true
				continue
			}
			if leaf != nil {
				return nil, errors.New("Merkl returned multiple claimable competition USDC rewards")
			}
			proofs := make([][32]byte, len(r.Proofs))
			for i, p := range r.Proofs {
				b, err := hexutil.Decode(p)
				if err != nil || len(b) != 32 {
					return nil, errors.New("Merkl returned a malformed proof")
				}
				proofs[i] = [32]byte(b)
			}
			leaf = &CompetitionLeaf{Amount: amount, Claimed: claimed, Proofs: proofs, Root: r.Root}
		}
	}
	if leaf == nil && missingProofs {
		return nil, ErrProofsUnavailable
	}
	return leaf, nil
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// EncodeCompetitionClaim packs a single USDC leaf using its cumulative Merkl amount.
func EncodeCompetitionClaim(agent string, amount *big.Int, proofs [][32]byte) ([]byte, error) {
	return distributorABI.Pack("claim", []common.Address{common.HexToAddress(agent)}, []common.Address{common.HexToAddress(CompetitionToken)}, []*big.Int{amount}, [][][32]byte{proofs})
}

// VerifyCompetitionClaim decodes the sealed template and requires exactly one
// claim of the competition's USDC, for the agent, to the distributor, with no value.
func VerifyCompetitionClaim(in journal.OnchainIntent, agent string) error {
	if in.ChainID != CompetitionChain || len(in.Templates) != 1 || !strings.EqualFold(in.Sender, agent) || !strings.EqualFold(in.Recipient, agent) || !strings.EqualFold(in.Token, CompetitionToken) {
		return errors.New("claim intent chain, wallet or token differs")
	}
	t := in.Templates[0]
	if t.ChainID != CompetitionChain || !strings.EqualFold(t.To, Distributor) || t.Value != "0x0" {
		return errors.New("claim target or value differs")
	}
	data, err := hexutil.Decode(t.Data)
	if err != nil || len(data) < 4 {
		return errors.New("claim calldata malformed")
	}
	m, err := distributorABI.MethodById(data[:4])
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
	if !strings.EqualFold(users[0].Hex(), agent) || !strings.EqualFold(tokens[0].Hex(), CompetitionToken) || amounts[0].String() != in.Amount {
		return errors.New("claim user, token or amount differs from the intent")
	}
	again, err := EncodeCompetitionClaim(agent, amounts[0], proofs[0])
	if err != nil || !strings.EqualFold(hexutil.Encode(again), t.Data) {
		return errors.New("claim calldata is not canonical")
	}
	return nil
}
