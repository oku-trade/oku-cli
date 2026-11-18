package merkl

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
)

const agent = "0x3333333333333333333333333333333333333333"

func TestVerifyCompetitionClaimRejectsTampering(t *testing.T) {
	proofs := [][32]byte{{0xab}}
	good, err := EncodeCompetitionClaim(agent, big.NewInt(5), proofs)
	if err != nil {
		t.Fatal(err)
	}
	base := func() journal.OnchainIntent {
		return journal.OnchainIntent{
			ChainID: CompetitionChain, Sender: agent, Recipient: agent, Token: CompetitionToken, Amount: "5",
			Templates: []journal.OnchainTemplate{{ChainID: CompetitionChain, To: Distributor, Data: hexutil.Encode(good), Value: "0x0"}},
		}
	}
	if err := VerifyCompetitionClaim(base(), agent); err != nil {
		t.Fatal(err)
	}
	other, err := EncodeCompetitionClaim("0x4444444444444444444444444444444444444444", big.NewInt(5), proofs)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*journal.OnchainIntent){
		"target":    func(in *journal.OnchainIntent) { in.Templates[0].To = "0x5555555555555555555555555555555555555555" },
		"value":     func(in *journal.OnchainIntent) { in.Templates[0].Value = "0x1" },
		"user":      func(in *journal.OnchainIntent) { in.Templates[0].Data = hexutil.Encode(other) },
		"amount":    func(in *journal.OnchainIntent) { in.Amount = "6" },
		"chain":     func(in *journal.OnchainIntent) { in.ChainID = 1 },
		"two calls": func(in *journal.OnchainIntent) { in.Templates = append(in.Templates, in.Templates[0]) },
	} {
		in := base()
		mutate(&in)
		if VerifyCompetitionClaim(in, agent) == nil {
			t.Errorf("%s tampering accepted", name)
		}
	}
}

func TestFindCompetitionLeaf(t *testing.T) {
	reward := func(proofs ...string) *v2.UserReward {
		return &v2.UserReward{DistributionChainId: 8453, Recipient: agent, Amount: "100", Claimed: "10", Proofs: proofs,
			Token: &v2.Token{ChainId: 8453, Address: CompetitionToken}}
	}
	find := func(rewards ...*v2.UserReward) (*CompetitionLeaf, error) {
		return FindCompetitionLeaf(&v2.GetUserRewardsResponse{Chains: []*v2.UserRewardsByChain{{Chain: &v2.Chain{Id: 8453}, Rewards: rewards}}}, agent)
	}
	proof := "0x" + strings.Repeat("ab", 32)
	claimable := reward(proof)
	if leaf, err := find(reward()); leaf != nil || !errors.Is(err, ErrProofsUnavailable) {
		t.Fatalf("missing proofs: leaf=%v err=%v", leaf, err)
	}
	claimed := reward(proof)
	claimed.Claimed = claimed.Amount
	if leaf, err := find(claimed, reward(), claimable); err != nil || leaf == nil || leaf.Amount.String() != "100" || leaf.Claimed.String() != "10" || len(leaf.Proofs) != 1 {
		t.Fatalf("later duplicate with proofs: leaf=%v err=%v", leaf, err)
	}
	if leaf, err := find(claimable, reward(proof)); leaf != nil || err == nil || !strings.Contains(err.Error(), "multiple claimable") {
		t.Fatalf("multiple claimable rewards: leaf=%v err=%v", leaf, err)
	}
	wrongDistribution, wrongToken := reward(proof), reward(proof)
	wrongDistribution.DistributionChainId, wrongToken.Token.ChainId = 1, 42161
	for _, r := range []*v2.UserReward{wrongDistribution, wrongToken} {
		if leaf, err := find(r); leaf != nil || err == nil || !strings.Contains(err.Error(), "both be on Base") {
			t.Errorf("wrong chain IDs: leaf=%v err=%v", leaf, err)
		}
	}
}
