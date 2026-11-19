package cli

import (
	"fmt"
	"strings"
	"testing"
)

func competitionFixture() map[string]any {
	return map[string]any{
		"phase": "COMPETITION_PHASE_LIVE", "startTime": "2026-11-19T17:00:00Z", "endTime": "2026-12-03T17:00:00Z",
		"nextDailyResetTime": "2026-11-20T17:00:00Z", "prizePoolUsd": "25000", "unlockedPoolUsd": "25000",
		"participantCount": "55", "totalVolumeUsd": "1234.5", "dataAsOf": "2026-11-19T18:00:00Z",
		"rules":          map[string]any{"potMinimumUsd": "10000", "dailyVolumeCapUsd": "250000", "raffleMinimumUsd": "2000", "rafflePrizeUsd": "300", "raffleWinnersPerDay": 1, "raffleDayCount": 14, "volumePotUsd": "20800"},
		"consentText":    "I consent to the rules.",
		"eligibleStocks": []any{map[string]any{"address": "0xB200000000000000000000C2E324D24D7EECD1FB", "symbol": "AAPLc", "name": "Apple Inc."}},
		"raffleDays": []any{
			map[string]any{"dayNumber": 1, "drawTime": "2026-11-20T17:00:00Z", "status": "COMPETITION_RAFFLE_DAY_STATUS_DRAWN", "winners": []any{map[string]any{"name": "alice", "shortWallet": "0x12...ab", "prizeUsd": "300"}}},
			map[string]any{"dayNumber": 2, "drawTime": "2026-11-21T17:00:00Z", "status": "COMPETITION_RAFFLE_DAY_STATUS_OPEN"},
		},
	}
}

func TestCompetitionInfo(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		if method == "GetCompetition" {
			return competitionFixture(), true
		}
		return nil, false
	}
	r := h.run("competition", "info", "--json")
	if r.code != 0 {
		t.Fatalf("info: %s %s", r.stdout, r.stderr)
	}
	d := r.data()
	if d["phase"] != "live" || d["prizePoolUsd"] != "25000" || d["participants"] != float64(55) {
		t.Fatalf("info data: %v", d)
	}
	stocks := d["eligibleStocks"].([]any)
	if stocks[0].(map[string]any)["address"] != "0xb200000000000000000000c2e324d24d7eecd1fb" {
		t.Fatalf("stock address not lowercased: %v", stocks)
	}
	days := d["raffleDays"].([]any)
	if len(days) != 2 || days[0].(map[string]any)["status"] != "drawn" || len(days[1].(map[string]any)["winners"].([]any)) != 0 {
		t.Fatalf("raffle days: %v", days)
	}
	human := h.run("competition", "info")
	for _, want := range []string{"Phase: live", "Prize pool: $25000", "AAPLc", "alice", "Daily raffle: 1 winners x $300 over 14 days"} {
		if !strings.Contains(human.stdout, want) {
			t.Fatalf("human output missing %q:\n%s", want, human.stdout)
		}
	}
}

func TestCompetitionLeaderboardPagesAndSearch(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	var pages []float64
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method != "ListLeaderboard" {
			return nil, false
		}
		page, _ := body["page"].(float64)
		size, _ := body["pageSize"].(float64)
		pages = append(pages, page)
		rows := []any{}
		for i := 0; i < int(size) && int(page-1)*int(size)+i < 150; i++ {
			n := int(page-1)*int(size) + i + 1
			rows = append(rows, map[string]any{"rank": n, "displayName": fmt.Sprintf("trader%d", n), "shortWallet": "0x00...00", "countedVolumeUsd": "10", "raffleWinningsUsd": "0"})
		}
		if page == 2 && size == competitionPageSize {
			// A row moved across the page boundary while we were reading.
			rows = append(rows, map[string]any{"rank": 100, "displayName": "trader100", "shortWallet": "0x00...00"})
		}
		return map[string]any{"rows": rows, "totalCount": "400"}, true
	}
	r := h.run("competition", "leaderboard", "--page", "2", "--page-size", "10", "--json")
	if r.code != 0 || len(r.data()["rows"].([]any)) != 10 || r.data()["page"] != float64(2) {
		t.Fatalf("one page: %s", r.stdout)
	}
	pages = nil
	r = h.run("competition", "leaderboard", "--all", "--json")
	if r.code != 0 || len(r.data()["rows"].([]any)) != 150 || len(pages) != 2 {
		t.Fatalf("all pages=%v: %s", pages, r.stdout)
	}
	r = h.run("competition", "leaderboard", "--search", "TRADER14", "--json")
	rows := r.data()["rows"].([]any)
	// trader14 and trader140..149
	if r.code != 0 || len(rows) != 11 {
		t.Fatalf("search rows=%d: %s", len(rows), r.stdout)
	}
	if r := h.run("competition", "leaderboard", "--page-size", "101", "--json"); r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("oversized page: %s", r.stdout)
	}
	pages = nil
	for _, flags := range [][]string{{"--all", "--page", "1"}, {"--search", "trader", "--page-size", "25"}} {
		args := append([]string{"competition", "leaderboard", "--json"}, flags...)
		if r := h.run(args...); r.errCode() != "INVALID_ARGUMENT" || len(pages) != 0 {
			t.Fatalf("conflicting flags %v fetched %v: %s", flags, pages, r.stdout)
		}
	}
}

func TestCompetitionStatusDefaultsToAgent(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	var asked []string
	rewardState := "COMPETITION_REWARD_STATE_CLAIMABLE"
	distributor := "0x3ef3d8ba38ebe18db133cec108f4d14ce00dd9ae"
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method != "GetParticipant" {
			return nil, false
		}
		asked = append(asked, body["wallet"].(string))
		if body["wallet"] != strings.ToLower(h.fake.agentAddress) {
			return map[string]any{"registered": false}, true
		}
		return map[string]any{
			"registered": true, "rank": 3, "countedVolumeUsd": "12000", "potMinimumReached": true, "projectedRewardUsd": "150.25", "provisional": true,
			"rafflesWon": 1, "raffleWinningsUsd": "300",
			"today":   map[string]any{"countedVolumeUsd": "500", "dailyCapUsd": "250000", "remainingVolumeUsd": "249500", "raffleEligible": false, "raffleMinimumUsd": "2000", "resetsAt": "2026-11-20T17:00:00Z"},
			"rewards": []any{map[string]any{"state": rewardState, "amount": "150.25", "token": map[string]any{"address": "0x833589FCD6EDB6E08F4C7C32D4F71B54BDA02913", "chain": "eip155:8453", "decimals": 6, "symbol": "USDC"}}},
			"claim":   map[string]any{"chain": "eip155:8453", "distributor": distributor},
		}, true
	}
	r := h.run("competition", "status", "--json")
	if r.code != 0 {
		t.Fatalf("status: %s %s", r.stdout, r.stderr)
	}
	if len(asked) != 1 || asked[0] != strings.ToLower(h.fake.agentAddress) {
		t.Fatalf("asked %v, want agent %s", asked, h.fake.agentAddress)
	}
	d := r.data()
	if d["rank"] != float64(3) || d["today"].(map[string]any)["remainingVolumeUsd"] != "249500" || d["registrationNote"] != nil {
		t.Fatalf("status data: %v", d)
	}
	reward := d["rewards"].([]any)[0].(map[string]any)
	if reward["state"] != "claimable" || reward["amount"] != "150.25" || reward["token"] != "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913" {
		t.Fatalf("reward: %v", reward)
	}
	next, claimNote := "oku merkl claim prepare", fmt.Sprint(d["claimNote"])
	if d["next"] != next || !strings.Contains(claimNote, "may include other campaigns") {
		t.Fatalf("claim guidance: %v", d)
	}
	if r := h.run("competition", "status", "--wallet", h.fake.agentAddress); r.code != 0 || !strings.Contains(r.stdout, "Claim with: "+next+"\n"+claimNote) || !strings.Contains(r.stdout, "150.25") {
		t.Fatalf("explicit wallet status: %s", r.stdout)
	}
	rewardState = "COMPETITION_REWARD_STATE_CLAIMED"
	if r := h.run("competition", "status", "--json"); r.code != 0 || r.data()["next"] != nil {
		t.Fatalf("already claimed award: %s", r.stdout)
	}
	rewardState = "COMPETITION_REWARD_STATE_CLAIMABLE"
	distributor = "0x1111111111111111111111111111111111111111"
	if r := h.run("competition", "status", "--json"); r.code != 0 || r.data()["next"] != nil {
		t.Fatalf("award with different distributor: %s", r.stdout)
	}
	other := "0x2222222222222222222222222222222222222222"
	r = h.run("competition", "status", "--wallet", other, "--json")
	note, _ := r.data()["registrationNote"].(string)
	if r.code != 0 || asked[len(asked)-1] != other || r.data()["registered"] != false || r.data()["next"] != nil || !strings.Contains(note, "may be cached") || !strings.Contains(note, "without --accept") {
		t.Fatalf("--wallet: %v %s", asked, r.stdout)
	}
	if r := h.run("competition", "status", "--wallet", other); r.code != 0 || !strings.Contains(r.stdout, note) || strings.Contains(r.stdout, "Claim with:") {
		t.Fatalf("status text lacks registration caveat: %s", r.stdout)
	}
	if r := h.run("competition", "status", "--wallet", "nope", "--json"); r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("bad wallet: %s", r.stdout)
	}
}
