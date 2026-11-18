package cli

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/oku-trade/oku-cli/pkg/auth"
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
	now        func() time.Time
	starts     int
	completes  int
}

func (c *competitionRegistrar) message() string {
	return fmt.Sprintf("%s\nNonce: %d", competitionMessage, c.starts)
}

func (c *competitionRegistrar) reply(method string, body map[string]any) (any, bool) {
	switch method {
	case "StartRegistration":
		c.starts++
		if c.registered {
			return map[string]any{"alreadyRegistered": map[string]any{}}, true
		}
		return map[string]any{"challenge": map[string]any{"message": c.message(), "challengeToken": fmt.Sprintf("tok-%d", c.starts), "expiresAt": c.now().Add(5 * time.Minute).UTC().Format(time.RFC3339)}}, true
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
	reg := &competitionRegistrar{t: t, now: h.now}
	h.fake.routeReply = reg.reply
	if r := h.run("competition", "register", "--accept", "--digest", "unreviewed", "--json"); r.errCode() != "INTENT_STATE" || reg.starts != 0 {
		t.Fatalf("accept without preview: %s", r.stdout)
	}
	r := h.run("competition", "register", "--json")
	message := reg.message()
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(message)))
	if r.code != 0 || r.data()["registered"] != false || r.data()["message"] != message || r.data()["digest"] != digest || r.data()["next"] != "oku --profile main competition register --accept --digest "+digest || strings.Contains(r.stdout, "tok-1") {
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
	if reg.starts != 1 || reg.completes != 1 || len(h.privy.personalMessages) != 1 || h.privy.personalMessages[0] != message {
		t.Fatalf("signed %q", h.privy.personalMessages)
	}
	r = h.run("competition", "register", "--accept", "--digest", digest, "--json")
	if r.errCode() != "INTENT_STATE" || reg.starts != 1 || len(h.privy.personalMessages) != 1 {
		t.Fatalf("replay: %s", r.stdout)
	}
	r = h.run("competition", "register", "--json")
	if r.code != 0 || r.data()["alreadyRegistered"] != true || r.data()["digest"] != nil || len(h.privy.personalMessages) != 1 {
		t.Fatalf("already registered: %s", r.stdout)
	}
}

func TestCompetitionRegisterExpiredPreviewRequiresNewApproval(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	now := time.Now()
	h.now = func() time.Time { return now }
	reg := &competitionRegistrar{t: t, now: h.now}
	h.fake.routeReply = reg.reply
	old := registrationPreviewDigest(t, h)
	now = now.Add(6 * time.Minute)
	accept := func(digest string) run {
		return h.run("competition", "register", "--accept", "--digest", digest, "--json")
	}
	if r := accept(old); r.errCode() != "INTENT_STATE" || !strings.Contains(r.stdout, "expired") || reg.starts != 1 || reg.completes != 0 || len(h.privy.personalMessages) != 0 {
		t.Fatalf("expired preview: %s", r.stdout)
	}
	fresh := registrationPreviewDigest(t, h)
	if r := accept(old); r.errCode() != "PAYLOAD_MISMATCH" || fresh == old || reg.starts != 2 || reg.completes != 0 || len(h.privy.personalMessages) != 0 {
		t.Fatalf("stale approval: %s", r.stdout)
	}
	if r := accept(fresh); r.code != 0 || reg.starts != 2 || reg.completes != 1 {
		t.Fatalf("new approval: %s", r.stdout)
	}
}

func TestCompetitionRegisterBindsPreviewToProfileAndWallet(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	reg := &competitionRegistrar{t: t, now: h.now}
	h.fake.routeReply = reg.reply
	digest := registrationPreviewDigest(t, h)
	if r := h.run("profiles", "add", "other", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	md, sec, err := auth.LoadPrivy("main")
	if err != nil {
		t.Fatal(err)
	}
	other := *md
	other.Profile = "other"
	if err := auth.SavePrivy("other", other, *sec); err != nil {
		t.Fatal(err)
	}
	accept := func(profile string) run {
		return h.run("--profile", profile, "competition", "register", "--accept", "--digest", digest, "--json")
	}
	if r := accept("other"); r.errCode() != "INTENT_STATE" {
		t.Fatalf("wrong profile: %s", r.stdout)
	}
	md.AgentAddress = "0x2222222222222222222222222222222222222222"
	if err := auth.SavePrivy("main", *md, *sec); err != nil {
		t.Fatal(err)
	}
	if r := accept("main"); r.errCode() != "INTENT_STATE" || reg.starts != 1 || reg.completes != 0 || len(h.privy.personalMessages) != 0 {
		t.Fatalf("wrong agent: %s", r.stdout)
	}
}

func registrationPreviewDigest(t *testing.T, h *harness) string {
	t.Helper()
	r := h.run("competition", "register", "--json")
	digest, ok := r.data()["digest"].(string)
	if r.code != 0 || !ok {
		t.Fatalf("missing preview digest: %s", r.stdout)
	}
	return digest
}

func TestCompetitionRegisterTypedFailure(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	reg := &competitionRegistrar{t: t, now: h.now, failure: "COMPETITION_REGISTRATION_FAILURE_REGISTERED_WITH_OTHER_WALLET"}
	h.fake.routeReply = reg.reply
	digest := registrationPreviewDigest(t, h)
	r := h.run("competition", "register", "--accept", "--digest", digest, "--json")
	if r.errCode() != "REJECTED" || !strings.Contains(r.stdout, "another wallet") || !strings.Contains(r.stdout, "registered_with_other_wallet") {
		t.Fatalf("failure: %s", r.stdout)
	}
	reg.failure = "COMPETITION_REGISTRATION_FAILURE_RATE_LIMITED"
	digest = registrationPreviewDigest(t, h)
	if r := h.run("competition", "register", "--accept", "--digest", digest, "--json"); r.errCode() != "RATE_LIMITED" {
		t.Fatalf("rate limited: %s", r.stdout)
	}
}
