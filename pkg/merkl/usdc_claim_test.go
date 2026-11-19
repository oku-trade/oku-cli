package merkl

import (
	"bytes"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
)

const agent = "0x3333333333333333333333333333333333333333"

func TestVerifyUSDCClaimRejectsTampering(t *testing.T) {
	proofs := [][32]byte{{0xab}}
	good, err := EncodeUSDCClaim(agent, big.NewInt(5), proofs)
	if err != nil {
		t.Fatal(err)
	}
	base := func() journal.OnchainIntent {
		return journal.OnchainIntent{
			ChainID: BaseChainID, Sender: agent, Recipient: agent, Token: BaseUSDC, Amount: "5",
			Templates: []journal.OnchainTemplate{{ChainID: BaseChainID, To: Distributor, Data: hexutil.Encode(good), Value: "0x0"}},
		}
	}
	if err := VerifyUSDCClaim(base(), agent); err != nil {
		t.Fatal(err)
	}
	other, err := EncodeUSDCClaim("0x4444444444444444444444444444444444444444", big.NewInt(5), proofs)
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
		if VerifyUSDCClaim(in, agent) == nil {
			t.Errorf("%s tampering accepted", name)
		}
	}
}

func TestUSDCClaimDoesNotUseRecipientOverrides(t *testing.T) {
	wallet := common.HexToAddress(agent)
	usdc := common.HexToAddress(BaseUSDC)
	other := common.HexToAddress("0x4444444444444444444444444444444444444444")
	data, err := EncodeUSDCClaim(agent, big.NewInt(5), [][32]byte{{0xab}})
	if err != nil {
		t.Fatal(err)
	}
	selector := crypto.Keccak256([]byte("claimWithRecipient(address[],address[],uint256[],bytes32[][],address[],bytes[])"))[:4]
	if !bytes.Equal(data[:4], selector) {
		t.Fatalf("wrong deployed method selector: %x", data[:4])
	}
	m := distributorABI.Methods[claimMethod]
	args, err := m.Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatal(err)
	}
	users, recipients, callbacks := args[0].([]common.Address), args[4].([]common.Address), args[5].([][]byte)
	if len(users) != 1 || users[0] != wallet || len(recipients) != 1 || recipients[0] != wallet || len(callbacks) != 1 || len(callbacks[0]) != 0 {
		t.Fatalf("unexpected owner, recipient or callback: %v %v %x", users, recipients, callbacks)
	}
	// The deployed contract consults these defaults only for a zero recipient
	// or a caller other than the owner. Reject sealed calls that use either
	// default's wallet as the explicit recipient, or leave it up to fallback.
	for name, defaults := range map[string]map[common.Address]common.Address{
		"USDC override":   {usdc: other},
		"global override": {{0}: other},
	} {
		t.Run(name, func(t *testing.T) {
			defaultRecipient := defaults[usdc]
			if defaultRecipient == (common.Address{}) {
				defaultRecipient = defaults[common.Address{}]
			}
			in := journal.OnchainIntent{ChainID: BaseChainID, Sender: agent, Recipient: agent, Token: BaseUSDC, Amount: "5",
				Templates: []journal.OnchainTemplate{{ChainID: BaseChainID, To: Distributor, Value: "0x0", Data: hexutil.Encode(data)}}}
			if err := VerifyUSDCClaim(in, agent); err != nil {
				t.Fatal(err)
			}
			for _, recipient := range []common.Address{{}, defaultRecipient} {
				tampered := append([]any(nil), args...)
				tampered[4] = []common.Address{recipient}
				packed, err := distributorABI.Pack(claimMethod, tampered...)
				if err != nil {
					t.Fatal(err)
				}
				in.Templates[0].Data = hexutil.Encode(packed)
				if VerifyUSDCClaim(in, agent) == nil {
					t.Fatalf("accepted override recipient %v", recipient)
				}
			}
			withCallback := append([]any(nil), args...)
			withCallback[5] = [][]byte{{1}}
			packed, err := distributorABI.Pack(claimMethod, withCallback...)
			if err != nil {
				t.Fatal(err)
			}
			in.Templates[0].Data = hexutil.Encode(packed)
			if VerifyUSDCClaim(in, agent) == nil {
				t.Fatal("accepted callback data")
			}
		})
	}
}

func TestFindUSDCLeaf(t *testing.T) {
	reward := func(proofs ...string) *v2.UserReward {
		return &v2.UserReward{DistributionChainId: 8453, Recipient: agent, Amount: "100", Claimed: "10", Proofs: proofs,
			Token: &v2.Token{ChainId: 8453, Address: BaseUSDC}}
	}
	find := func(rewards ...*v2.UserReward) (*USDCLeaf, error) {
		return FindUSDCLeaf(&v2.GetUserRewardsResponse{Chains: []*v2.UserRewardsByChain{{Chain: &v2.Chain{Id: 8453}, Rewards: rewards}}}, agent)
	}
	proof := "0x" + strings.Repeat("ab", 32)
	claimable := reward(proof)
	unknownBalance := reward(proof)
	unknownBalance.Claimed = ""
	if leaf, err := find(unknownBalance); leaf != nil || err == nil {
		t.Fatalf("missing claimed balance: leaf=%v err=%v", leaf, err)
	}
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
