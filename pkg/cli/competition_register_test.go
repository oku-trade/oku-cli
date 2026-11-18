package cli

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
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
	raw, err := hexutil.Decode(sigHex)
	if err != nil || len(raw) != 65 {
		t.Error("invalid personal signature")
		return ""
	}
	addr, err := signing.RecoverAddress(personalDigest(message), signing.Signature{R: hexutil.Encode(raw[:32]), S: hexutil.Encode(raw[32:64]), V: uint64(raw[64])})
	if err != nil {
		t.Error(err)
	}
	return addr
}

const competitionMessage = "Join the Oku trading competition.\nI consent to the rules."

// competitionRegistrar fakes StartRegistration and CompleteRegistration,
// verifying the signature recovers to the registering wallet.
type competitionRegistrar struct {
	t          *testing.T
	registered bool
	failure    string
	expiresIn  time.Duration
	messages   []string
	starts     int
	completes  int
}

func (c *competitionRegistrar) message() string {
	if len(c.messages) == 0 {
		return competitionMessage
	}
	return c.messages[min(c.starts, len(c.messages))-1]
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
		return map[string]any{"challenge": map[string]any{"message": c.message(), "challengeToken": fmt.Sprintf("tok-%d", c.starts), "expiresAt": time.Now().Add(exp).UTC().Format(time.RFC3339)}}, true
	case "CompleteRegistration":
		c.completes++
		if c.failure != "" {
			return map[string]any{"failure": c.failure, "failureMessage": "nope"}, true
		}
		wallet, _ := body["wallet"].(string)
		if got := recoverPersonal(c.t, c.message(), body["signature"].(string)); !strings.EqualFold(got, wallet) {
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

func TestCompetitionRegisterRequiresAcceptAndDigest(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	reg := &competitionRegistrar{t: t}
	h.fake.routeReply = reg.reply
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(competitionMessage)))
	r := h.run("competition", "register", "--json")
	if r.code != 0 || r.data()["registered"] != false || r.data()["message"] != competitionMessage || r.data()["digest"] != digest || r.data()["next"] != "oku competition register --accept --digest "+digest {
		t.Fatalf("preview: %s", r.stdout)
	}
	r = h.run("competition", "register", "--accept", "--json")
	if r.errCode() != "USAGE" || reg.starts != 1 || reg.completes != 0 || len(h.privy.personalMessages) != 0 {
		t.Fatalf("missing digest: starts=%d completes=%d response=%s", reg.starts, reg.completes, r.stdout)
	}
	r = h.run("competition", "register", "--accept", "--digest", digest, "--json")
	if r.code != 0 || r.data()["registered"] != true || r.data()["alreadyRegistered"] != false || r.data()["digest"] != digest {
		t.Fatalf("register: %s %s", r.stdout, r.stderr)
	}
	if len(h.privy.personalMessages) != 1 || h.privy.personalMessages[0] != competitionMessage {
		t.Fatalf("signed %q", h.privy.personalMessages)
	}
	r = h.run("competition", "register", "--accept", "--digest", digest, "--json")
	if r.code != 0 || r.data()["alreadyRegistered"] != true || r.data()["digest"] != nil || len(h.privy.personalMessages) != 1 {
		t.Fatalf("already registered: %s", r.stdout)
	}
}

func TestCompetitionRegisterRejectsChangedConsent(t *testing.T) {
	const current = "Updated competition rules.\nReview before proceeding."
	for _, tc := range []struct {
		name     string
		messages []string
		expiry   time.Duration
	}{
		{"fresh challenge", []string{competitionMessage, current}, 0},
		{"after expiry refresh", []string{competitionMessage, competitionMessage, current}, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			reg := &competitionRegistrar{t: t, messages: tc.messages}
			h.fake.routeReply = reg.reply
			r := h.run("competition", "register", "--json")
			approved, ok := r.data()["digest"].(string)
			if !ok {
				t.Fatalf("missing preview digest: %s", r.stdout)
			}
			reg.expiresIn = tc.expiry
			r = h.run("competition", "register", "--accept", "--digest", approved, "--json")
			if r.errCode() != "PAYLOAD_MISMATCH" || reg.starts != len(tc.messages) || reg.completes != 0 || len(h.privy.personalMessages) != 0 {
				t.Fatalf("changed consent: starts=%d completes=%d response=%s", reg.starts, reg.completes, r.stdout)
			}
			oe := r.env["error"].(map[string]any)
			details, _ := oe["details"].(map[string]any)
			newDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(current)))
			if details["message"] != current || details["digest"] != newDigest {
				t.Fatalf("missing current consent: %s", r.stdout)
			}
			r = h.run("competition", "register", "--accept", "--digest", newDigest, "--json")
			if r.code != 0 || r.data()["digest"] != newDigest || len(h.privy.personalMessages) != 1 || h.privy.personalMessages[0] != current {
				t.Fatalf("approved updated consent: %s", r.stdout)
			}
		})
	}
}

func TestCompetitionRegisterRefreshesExpiringChallenge(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	reg := &competitionRegistrar{t: t, expiresIn: 5 * time.Second}
	h.fake.routeReply = reg.reply
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(competitionMessage)))
	r := h.run("competition", "register", "--accept", "--digest", digest, "--json")
	if r.code != 0 || reg.starts != 2 || reg.completes != 1 {
		t.Fatalf("starts=%d completes=%d: %s", reg.starts, reg.completes, r.stdout)
	}
}

func TestCompetitionRegisterTypedFailure(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	reg := &competitionRegistrar{t: t, failure: "COMPETITION_REGISTRATION_FAILURE_REGISTERED_WITH_OTHER_WALLET"}
	h.fake.routeReply = reg.reply
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(competitionMessage)))
	r := h.run("competition", "register", "--accept", "--digest", digest, "--json")
	if r.errCode() != "REJECTED" || !strings.Contains(r.stdout, "another wallet") || !strings.Contains(r.stdout, "registered_with_other_wallet") {
		t.Fatalf("failure: %s", r.stdout)
	}
	reg.failure = "COMPETITION_REGISTRATION_FAILURE_RATE_LIMITED"
	if r := h.run("competition", "register", "--accept", "--digest", digest, "--json"); r.errCode() != "RATE_LIMITED" {
		t.Fatalf("rate limited: %s", r.stdout)
	}
}
