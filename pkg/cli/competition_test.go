package cli

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/signing"
	"golang.org/x/crypto/sha3"
)

// personalDigest is the EIP-191 personal_sign digest of message.
func personalDigest(message string) [32]byte {
	h := sha3.NewLegacyKeccak256()
	fmt.Fprintf(h, "\x19Ethereum Signed Message:\n%d%s", len(message), message)
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// recoverPersonal recovers the signer of a 0x hex 65-byte personal_sign signature.
func recoverPersonal(t *testing.T, message, sigHex string) string {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimPrefix(sigHex, "0x"))
	if err != nil || len(raw) != 65 {
		t.Fatalf("bad signature %q", sigHex)
	}
	addr, err := signing.RecoverAddress(personalDigest(message), signing.Signature{R: "0x" + hex.EncodeToString(raw[:32]), S: "0x" + hex.EncodeToString(raw[32:64]), V: uint64(raw[64])})
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

const competitionMessage = "Join the Oku trading competition.\nI consent to the rules."

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
		return map[string]any{"rows": rows, "totalCount": "150"}, true
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
}

func TestCompetitionStatusDefaultsToAgent(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	var asked []string
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method != "GetParticipant" {
			return nil, false
		}
		asked = append(asked, body["wallet"].(string))
		return map[string]any{
			"registered": true, "rank": 3, "countedVolumeUsd": "12000", "potMinimumReached": true, "projectedRewardUsd": "150.25", "provisional": true,
			"rafflesWon": 1, "raffleWinningsUsd": "300",
			"today":   map[string]any{"countedVolumeUsd": "500", "dailyCapUsd": "250000", "remainingVolumeUsd": "249500", "raffleEligible": false, "raffleMinimumUsd": "2000", "resetsAt": "2026-11-20T17:00:00Z"},
			"rewards": []any{map[string]any{"state": "COMPETITION_REWARD_STATE_CLAIMABLE", "amount": "300000000", "token": map[string]any{"address": "0x833589FCD6EDB6E08F4C7C32D4F71B54BDA02913", "chain": "eip155:8453", "decimals": 6, "symbol": "USDC"}}},
			"claim":   map[string]any{"chain": "eip155:8453", "distributor": "0x3ef3d8ba38ebe18db133cec108f4d14ce00dd9ae"},
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
	if d["rank"] != float64(3) || d["today"].(map[string]any)["remainingVolumeUsd"] != "249500" {
		t.Fatalf("status data: %v", d)
	}
	reward := d["rewards"].([]any)[0].(map[string]any)
	if reward["state"] != "claimable" || reward["token"] != "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913" {
		t.Fatalf("reward: %v", reward)
	}
	other := "0x2222222222222222222222222222222222222222"
	if r := h.run("competition", "status", "--wallet", other, "--json"); r.code != 0 || asked[1] != other {
		t.Fatalf("--wallet: %v %s", asked, r.stdout)
	}
	if r := h.run("competition", "status", "--wallet", "nope", "--json"); r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("bad wallet: %s", r.stdout)
	}
}

// competitionRegistrar fakes StartRegistration and CompleteRegistration,
// verifying the signature recovers to the registering wallet.
type competitionRegistrar struct {
	t          *testing.T
	registered bool
	failure    string
	expiresIn  time.Duration
	starts     int
	completes  int
}

func (c *competitionRegistrar) reply(method string, body map[string]any) (any, bool) {
	switch method {
	case "StartRegistration":
		c.starts++
		if c.registered {
			return map[string]any{"alreadyRegistered": map[string]any{}}, true
		}
		exp := c.expiresIn
		if exp == 0 {
			exp = 5 * time.Minute
		}
		c.expiresIn = 0 // a refetch gets a fresh challenge
		return map[string]any{"challenge": map[string]any{"message": competitionMessage, "challengeToken": fmt.Sprintf("tok-%d", c.starts), "expiresAt": time.Now().Add(exp).UTC().Format(time.RFC3339)}}, true
	case "CompleteRegistration":
		c.completes++
		if c.failure != "" {
			return map[string]any{"failure": c.failure, "failureMessage": "nope"}, true
		}
		wallet, _ := body["wallet"].(string)
		if got := recoverPersonal(c.t, competitionMessage, body["signature"].(string)); !strings.EqualFold(got, wallet) {
			c.t.Errorf("signature recovers to %s, want %s", got, wallet)
			return map[string]any{"failure": "COMPETITION_REGISTRATION_FAILURE_INVALID_SIGNATURE"}, true
		}
		if body["challengeToken"] != fmt.Sprintf("tok-%d", c.starts) {
			c.t.Errorf("signed stale challenge %v", body["challengeToken"])
		}
		c.registered = true
		return map[string]any{"registered": map[string]any{}}, true
	}
	return nil, false
}

func TestCompetitionRegisterRequiresAccept(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	reg := &competitionRegistrar{t: t}
	h.fake.routeReply = reg.reply
	r := h.run("competition", "register", "--json")
	if r.code != 0 || r.data()["registered"] != false || r.data()["message"] != competitionMessage {
		t.Fatalf("preview: %s", r.stdout)
	}
	if reg.completes != 0 || len(h.privy.personalMessages) != 0 {
		t.Fatal("register without --accept signed or completed")
	}
	r = h.run("competition", "register", "--accept", "--json")
	if r.code != 0 || r.data()["registered"] != true || r.data()["alreadyRegistered"] != false {
		t.Fatalf("register: %s %s", r.stdout, r.stderr)
	}
	if len(h.privy.personalMessages) != 1 || h.privy.personalMessages[0] != competitionMessage {
		t.Fatalf("signed %q", h.privy.personalMessages)
	}
	r = h.run("competition", "register", "--accept", "--json")
	if r.code != 0 || r.data()["alreadyRegistered"] != true || len(h.privy.personalMessages) != 1 {
		t.Fatalf("already registered: %s", r.stdout)
	}
}

func TestCompetitionRegisterRefreshesExpiringChallenge(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	reg := &competitionRegistrar{t: t, expiresIn: 5 * time.Second}
	h.fake.routeReply = reg.reply
	r := h.run("competition", "register", "--accept", "--json")
	if r.code != 0 || reg.starts != 2 || reg.completes != 1 {
		t.Fatalf("starts=%d completes=%d: %s", reg.starts, reg.completes, r.stdout)
	}
}

func TestCompetitionRegisterTypedFailure(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	reg := &competitionRegistrar{t: t, failure: "COMPETITION_REGISTRATION_FAILURE_REGISTERED_WITH_OTHER_WALLET"}
	h.fake.routeReply = reg.reply
	r := h.run("competition", "register", "--accept", "--json")
	if r.errCode() != "REJECTED" || !strings.Contains(r.stdout, "another wallet") || !strings.Contains(r.stdout, "registered_with_other_wallet") {
		t.Fatalf("failure: %s", r.stdout)
	}
	reg.failure = "COMPETITION_REGISTRATION_FAILURE_RATE_LIMITED"
	if r := h.run("competition", "register", "--accept", "--json"); r.errCode() != "RATE_LIMITED" {
		t.Fatalf("rate limited: %s", r.stdout)
	}
}
