package merkl

import (
	"errors"
	"math/big"
	"strings"
)

// UnclaimedCompetitionAmount attributes part of this exact leaf to campaign
// IDs supplied by the competition provider. Both cumulative and claimed sums
// must reconcile, so pending or partial breakdowns cannot account for payout.
func (l *CompetitionLeaf) UnclaimedCompetitionAmount(campaignIDs []string) (*big.Int, error) {
	if len(campaignIDs) == 0 {
		return nil, errors.New("competition provider did not identify its Merkl payout campaigns")
	}
	campaigns := make(map[string]bool, len(campaignIDs))
	for _, id := range campaignIDs {
		if strings.TrimSpace(id) != id || id == "" {
			return nil, errors.New("competition provider returned an invalid Merkl campaign ID")
		}
		campaigns[id] = true
	}
	total, totalClaimed, competition := new(big.Int), new(big.Int), new(big.Int)
	for _, b := range l.breakdowns {
		amount, amountOK := new(big.Int).SetString(b.GetAmount(), 10)
		claimed, claimedOK := new(big.Int).SetString(b.GetClaimed(), 10)
		id := b.GetCampaignId()
		if id == "" || strings.TrimSpace(id) != id || !amountOK || !claimedOK || amount.Sign() < 0 || claimed.Sign() < 0 || claimed.Cmp(amount) > 0 {
			return nil, errors.New("Merkl returned an incomplete or invalid campaign breakdown")
		}
		total.Add(total, amount)
		totalClaimed.Add(totalClaimed, claimed)
		if campaigns[id] {
			competition.Add(competition, new(big.Int).Sub(amount, claimed))
		}
	}
	if total.Cmp(l.Amount) != 0 || totalClaimed.Cmp(l.Claimed) != 0 {
		return nil, errors.New("Merkl campaign balances do not reconcile to this leaf")
	}
	if competition.Sign() <= 0 {
		return nil, errors.New("no unclaimed payout from the competition's Merkl campaigns is in this leaf")
	}
	return competition, nil
}
