package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/signing"
)

const (
	acct  = "0x1111111111111111111111111111111111111111"
	token = "okud_testtoken_abcdefghijklmnopqrstuvwxyz"
	poll  = "pollsecret_abcdefghijklmnopqrstuvwxyz012345"
)

// fakeOku implements the subset of PerpsService/CliDeviceService the CLI
// uses, following perps.proto and oku-cli-device-contract.md.
type fakeOku struct {
	t  *testing.T
	mu sync.Mutex

	pollStatuses []string
	redeemed     *struct{ Agent, Name, Sig, Code string }
	approveAcct  string
	// scopes granted at enrollment approval (default read, trade).
	scopes []string
	// riskAction overrides the leverage/margin action the server builds.
	riskAction func(kind string, meta map[string]any) string

	// actionJSON overrides the order action the server builds.
	actionJSON func(meta map[string]any) string
	confirm    func() (int, string)
	actions    map[string]string // id -> state
	oids       map[string]string // id -> hl oid, when acknowledged
	confirms   int
	creates    int
	lastSigned map[string]any
	calls      []string
}

func newFake(t *testing.T) (*fakeOku, *httptest.Server) {
	f := &fakeOku{t: t, actions: map[string]string{}, oids: map[string]string{}, approveAcct: acct}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeOku) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.calls = append(f.calls, method)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	authed := r.Header.Get("x-oku-device-token") == token
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	fail := func(status int, code, msg string) {
		w.WriteHeader(status)
		reply(map[string]string{"code": code, "message": msg})
	}
	needAuth := map[string]bool{"CreateAction": true, "ConfirmAction": true, "CancelAction": true, "GetAction": true,
		"ReconcileActions": true, "GetCurrentDevice": true, "RevokeCurrentDevice": true}
	if needAuth[method] && !authed {
		fail(401, "unauthenticated", "authentication required")
		return
	}
	switch method {
	case "GetMarkets":
		reply(map[string]any{"markets": []map[string]any{{"coin": "BTC", "szDecimals": 5, "maxLeverage": 40, "assetId": 0, "markPx": "50000.0", "midPx": "50000.5", "funding": "0.00001"}}})
	case "GetMarketPrice":
		reply(map[string]any{"coin": "BTC", "markPx": "50000.0", "midPx": "50000", "oraclePx": "50001.0"})
	case "GetBuilderFeeStatus":
		reply(map[string]any{"builderAddress": "0xd637f2a36c1a3b37d57ef4c7022cb183d8922f2c", "builderFeeChargedPpm": 100, "builderFeeApproved": true, "builderFeePpm": 500})
	case "GetPerpPositions":
		reply(map[string]any{"marginSummary": map[string]string{"accountValue": "100.71"}, "withdrawable": "100.71"})
	case "GetAccountBalances":
		reply(map[string]any{"accountAbstraction": "unifiedAccount", "spotUsdcBalance": "0.005666"})
	case "RedeemEnrollment":
		code := body["enrollmentCode"].(string)
		agent := body["agentAddress"].(string)
		name := body["deviceName"].(string)
		sig := body["signature"].(string)
		f.redeemed = &struct{ Agent, Name, Sig, Code string }{agent, name, sig, code}
		reply(map[string]any{"enrollmentId": "enr1", "pollSecret": poll, "confirmationCode": "123456", "expiresAt": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "pollIntervalSeconds": 5})
	case "PollEnrollment":
		if body["pollSecret"] != poll {
			fail(404, "not_found", "no")
			return
		}
		st := f.pollStatuses[0]
		if len(f.pollStatuses) > 1 {
			f.pollStatuses = f.pollStatuses[1:]
		}
		resp := map[string]any{"status": st}
		if st == "approved" {
			resp["deviceId"], resp["deviceToken"], resp["scopes"], resp["accountAddress"] = "dev1", token, f.grantedScopes(), f.approveAcct
		}
		reply(resp)
	case "GetCurrentDevice", "RevokeCurrentDevice":
		st := "active"
		if method == "RevokeCurrentDevice" {
			st = "revoked"
		}
		reply(map[string]any{"device": map[string]any{"deviceId": "dev1", "agentAddress": f.redeemed.Agent, "accountAddress": acct, "status": st}})
	case "CreateAction":
		f.creates++
		params := body["params"].(map[string]any)
		id := fmt.Sprintf("act%d", f.creates)
		f.actions[id] = "pending"
		var aj string
		if o, ok := params["order"].(map[string]any); ok {
			if f.actionJSON != nil {
				aj = f.actionJSON(o)
			} else {
				aj = defaultOrderAction(o)
			}
		} else if l, ok := params["leverage"].(map[string]any); ok {
			// protojson omits zero values (isCross false).
			isCross, _ := l["isCross"].(bool)
			aj = fmt.Sprintf(`{"type":"updateLeverage","asset":0,"isCross":%v,"leverage":%v}`, isCross, l["leverage"])
			if f.riskAction != nil {
				aj = f.riskAction("leverage", l)
			}
		} else if m, ok := params["margin"].(map[string]any); ok {
			aj = fmt.Sprintf(`{"type":"updateIsolatedMargin","asset":0,"isBuy":true,"ntli":%d}`, marginNtli(m["amount"].(string)))
			if f.riskAction != nil {
				aj = f.riskAction("margin", m)
			}
		} else {
			c := params["cancel"].(map[string]any)
			aj = fmt.Sprintf(`{"type":"cancel","cancels":[{"a":0,"o":%s}]}`, c["oid"])
		}
		reply(map[string]any{"actionId": id, "actionType": "order", "status": "pending", "actionJson": aj})
	case "ConfirmAction":
		f.confirms++
		f.lastSigned = body
		id := body["actionId"].(string)
		if f.confirm != nil {
			status, resp := f.confirm()
			if status != 200 {
				w.WriteHeader(status)
				io.WriteString(w, resp)
				return
			}
			io.WriteString(w, resp)
			return
		}
		f.actions[id], f.oids[id] = "submitted", "987654321"
		reply(map[string]any{"status": "submitted", "hlOid": "987654321"})
	case "CancelAction":
		f.actions[body["actionId"].(string)] = "cancelled"
		reply(map[string]any{"status": "cancelled"})
	case "GetAction":
		id := body["actionId"].(string)
		reply(map[string]any{"actionId": id, "state": f.actions[id]})
	case "ReconcileActions":
		out := []map[string]any{}
		for _, id := range body["actionIds"].([]any) {
			a := map[string]any{"actionId": id, "state": f.actions[id.(string)]}
			if f.oids[id.(string)] != "" {
				a["hlOid"] = f.oids[id.(string)]
			}
			out = append(out, a)
		}
		reply(map[string]any{"actions": out})
	default:
		fail(404, "unimplemented", method)
	}
}

func (f *fakeOku) grantedScopes() []string {
	if f.scopes != nil {
		return f.scopes
	}
	return []string{"read", "trade"}
}

// marginNtli is the fake backend's independent USDC -> 6dp integer
// conversion (not trading.MarginUnits, so the CLI check is exercised).
func marginNtli(amount string) int64 {
	r, ok := new(big.Rat).SetString(amount)
	if !ok {
		panic(amount)
	}
	r.Mul(r, big.NewRat(1_000_000, 1))
	return r.Num().Int64()
}

func defaultOrderAction(o map[string]any) string {
	b := o["side"] == "buy"
	p, _ := o["limitPx"].(string)
	tif := "Gtc"
	if o["orderType"] == "market" {
		p, tif = "50500", "Ioc"
	}
	r := o["reduceOnly"] == true
	return fmt.Sprintf(`{"type":"order","orders":[{"a":0,"b":%v,"p":"%s","s":"%s","r":%v,"t":{"limit":{"tif":"%s"}},"c":"0x0123456789abcdef0123456789abcdef"}],"grouping":"na","builder":{"b":"0xd637f2a36c1a3b37d57ef4c7022cb183d8922f2c","f":10}}`, b, p, o["sz"], r, tif)
}

type harness struct {
	t      *testing.T
	fake   *fakeOku
	srv    *httptest.Server
	stdin  string
	passFn string
}

func newHarness(t *testing.T) *harness {
	if runtime.GOOS == "darwin" {
		if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("CI") != "true" || os.Getenv("OKU_KEYCHAIN_TEST_ACK") != "disposable-runner" {
			t.Skip("macOS auth tests require the disposable Keychain CI wrapper")
		}
		dedicated := os.Getenv("OKU_TEST_KEYCHAIN")
		if dedicated == "" || !filepath.IsAbs(dedicated) || !strings.Contains(filepath.Base(dedicated), "oku-ci-") {
			t.Fatal("missing dedicated Keychain path")
		}
		out, err := exec.Command("security", "default-keychain", "-d", "user").Output()
		if err != nil || strings.Trim(strings.TrimSpace(string(out)), "\"") != dedicated {
			t.Fatalf("disposable Keychain is not the user default: %v", err)
		}
	}
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("OKU_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	fake, srv := newFake(t)
	pf := filepath.Join(t.TempDir(), "pass")
	os.WriteFile(pf, []byte("correct horse battery\n"), 0o600)
	return &harness{t: t, fake: fake, srv: srv, passFn: pf}
}

type run struct {
	code   int
	stdout string
	stderr string
	env    map[string]any
}

func (h *harness) run(args ...string) run {
	var out, errb bytes.Buffer
	a := &App{
		Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(h.stdin),
		ReadSecret: func(string) ([]byte, error) { return nil, errNoTTY },
		Getenv: func(k string) string {
			if k == "OKU_PASSPHRASE_FILE" {
				return h.passFn
			}
			return ""
		},
		Now:     time.Now,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Version: "test",
	}
	code := a.Run(args)
	r := run{code: code, stdout: out.String(), stderr: errb.String()}
	_ = json.Unmarshal(out.Bytes(), &r.env)
	return r
}

func (r run) errCode() string {
	if e, ok := r.env["error"].(map[string]any); ok {
		return e["code"].(string)
	}
	return ""
}

func (r run) data() map[string]any {
	d, _ := r.env["data"].(map[string]any)
	return d
}

func (h *harness) enroll() {
	h.t.Helper()
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
		h.t.Fatalf("profiles add: %s %s", r.stdout, r.stderr)
	}
	h.fake.pollStatuses = []string{"pending_approval", "device_verified", "approved"}
	h.stdin = "abcd-efgh jkmn-pqrs\n"
	r := h.run("auth", "setup", "--code-stdin", "--device-name", "laptop", "--json")
	if r.code != 0 {
		h.t.Fatalf("auth setup: %s %s", r.stdout, r.stderr)
	}
}

func TestEnrollmentFlow(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	red := h.fake.redeemed
	if red.Code != "ABCDEFGHJKMNPQRS" || red.Name != "laptop" || red.Agent != strings.ToLower(red.Agent) {
		t.Fatalf("redeem request: %+v", red)
	}
	// The redeem signature must recover to the agent over the contract message.
	msg := signing.EnrollmentMessage(red.Code, red.Agent, "laptop")
	sig := signing.Signature{R: red.Sig[:66], S: "0x" + red.Sig[66:130], V: map[string]uint64{"1b": 27, "1c": 28}[red.Sig[130:]]}
	if got, err := signing.RecoverAddress(signing.PersonalDigest(msg), sig); err != nil || !strings.EqualFold(got, red.Agent) {
		t.Fatalf("enrollment signature recovers to %s, want %s (%v)", got, red.Agent, err)
	}
	r := h.run("auth", "status", "--json")
	if r.code != 0 || r.data()["credential"].(map[string]any)["status"] != "active" {
		t.Fatalf("status: %s", r.stdout)
	}
	// Secrets never appear in any output.
	for _, s := range []string{r.stdout, r.stderr} {
		if strings.Contains(s, token) || strings.Contains(s, poll) {
			t.Fatal("secret in output")
		}
	}
	if runtime.GOOS != "darwin" {
		cred, _ := filepath.Glob(filepath.Join(os.Getenv("OKU_CONFIG_DIR"), "credentials", "*"))
		raw, _ := os.ReadFile(cred[0])
		if bytes.Contains(raw, []byte(token)) {
			t.Fatal("token stored in plaintext")
		}
	}
	if r := h.run("auth", "status", "--verify", "--json"); r.code != 0 {
		t.Fatalf("verify: %s", r.stdout)
	}
}

func TestEnrollmentRefusesDifferentApprovedAccount(t *testing.T) {
	h := newHarness(t)
	h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL)
	h.fake.pollStatuses = []string{"approved"}
	h.fake.approveAcct = "0x2222222222222222222222222222222222222222"
	h.stdin = "ABCD-EFGH-JKMN-PQRS\n"
	r := h.run("auth", "setup", "--code-stdin", "--json")
	if r.code == 0 || r.errCode() != "CONFIG_ERROR" {
		t.Fatalf("got %d %s", r.code, r.stdout)
	}
	if strings.Contains(r.stdout, token) {
		t.Fatal("token leaked in error")
	}
}

func TestCodeNeverAcceptedWithoutStdinOrTTY(t *testing.T) {
	h := newHarness(t)
	h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL)
	r := h.run("auth", "setup", "--json")
	if r.errCode() != "USAGE" || h.fake.redeemed != nil {
		t.Fatalf("got %s", r.stdout)
	}
}

func prepare(t *testing.T, h *harness, extra ...string) (string, string) {
	t.Helper()
	args := append([]string{"orders", "prepare", "--market", "BTC", "--side", "buy", "--type", "limit", "--size", "0.00100", "--price", "50000", "--json"}, extra...)
	r := h.run(args...)
	if r.code != 0 {
		t.Fatalf("prepare: %s %s", r.stdout, r.stderr)
	}
	d := r.data()
	return d["intent_id"].(string), d["digest"].(string)
}

func TestPrepareSubmitHappyPath(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	id, dg := prepare(t, h)
	if h.fake.creates != 0 {
		t.Fatal("prepare contacted CreateAction")
	}
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.code != 0 || r.data()["state"] != "submitted" {
		t.Fatalf("submit: %s %s", r.stdout, r.stderr)
	}
	// Canonicalized size was sent; signature recovers to the enrolled agent.
	sa := h.fake.lastSigned["signedAction"].(map[string]any)
	if !strings.Contains(sa["actionJson"].(string), `"s":"0.001"`) || sa["vaultAddress"] != nil {
		t.Fatalf("signed action: %v", sa)
	}
	var nonce uint64
	fmt.Sscan(sa["nonce"].(string), &nonce)
	conn, _ := signing.ActionHash(sa["actionJson"].(string), "", nonce)
	s := sa["signature"].(map[string]any)
	var v uint64
	fmt.Sscan(s["v"].(string), &v)
	got, _ := signing.RecoverAddress(signing.AgentDigest(conn), signing.Signature{R: s["r"].(string), S: s["s"].(string), V: v})
	if !strings.EqualFold(got, h.fake.redeemed.Agent) {
		t.Fatalf("signed by %s, want agent %s", got, h.fake.redeemed.Agent)
	}
	// Same intent never sent twice.
	r = h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "INTENT_STATE" || h.fake.confirms != 1 {
		t.Fatalf("resubmit: %s confirms=%d", r.stdout, h.fake.confirms)
	}
}

func TestSameIntentIDDifferentPayloadRejected(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	prepare(t, h, "--intent-id", "my-order-1")
	r := h.run("orders", "prepare", "--market", "BTC", "--side", "buy", "--type", "limit", "--size", "0.002", "--price", "50000", "--intent-id", "my-order-1", "--json")
	if r.errCode() != "INTENT_CONFLICT" {
		t.Fatalf("got %s", r.stdout)
	}
}

func TestWrongDigestRejectedBeforeNetwork(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	id, _ := prepare(t, h)
	r := h.run("orders", "submit", "--intent", id, "--digest", strings.Repeat("0", 64), "--json")
	if r.errCode() != "PAYLOAD_MISMATCH" || h.fake.creates != 0 {
		t.Fatalf("got %s", r.stdout)
	}
}

func TestServerPayloadMismatchNeverSigned(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.actionJSON = func(o map[string]any) string {
		return strings.Replace(defaultOrderAction(o), `"s":"0.001"`, `"s":"1"`, 1)
	}
	id, dg := prepare(t, h)
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "PAYLOAD_MISMATCH" || h.fake.confirms != 0 {
		t.Fatalf("got %s confirms=%d", r.stdout, h.fake.confirms)
	}
	if h.fake.actions["act1"] != "cancelled" {
		t.Fatal("unsigned mismatched server action not cancelled")
	}
}

func TestAmbiguousConfirmIsUnknownAndBlocksResend(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.confirm = func() (int, string) { return 503, "gateway" }
	id, dg := prepare(t, h)
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.code != 6 || r.errCode() != "OUTCOME_UNKNOWN" {
		t.Fatalf("got %d %s", r.code, r.stdout)
	}
	e := r.env["error"].(map[string]any)
	if e["execution_state"] != "unknown" {
		t.Fatalf("execution_state %v", e["execution_state"])
	}
	// Retrying the same intent does not resend.
	r = h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "OUTCOME_UNKNOWN" || h.fake.confirms != 1 {
		t.Fatalf("resend: %s confirms=%d", r.stdout, h.fake.confirms)
	}
	// New risk is blocked until reconciled.
	id2, dg2 := prepare(t, h)
	r = h.run("orders", "submit", "--intent", id2, "--digest", dg2, "--json")
	if r.errCode() != "OUTCOME_UNKNOWN" || h.fake.confirms != 1 {
		t.Fatalf("new order while unknown: %s", r.stdout)
	}
	// Server says the confirm actually landed and the order rests.
	h.fake.actions["act1"], h.fake.oids["act1"] = "submitted", "987654321"
	r = h.run("orders", "reconcile", "--json")
	if r.code != 0 || r.data()["still_unknown"].(float64) != 0 {
		t.Fatalf("reconcile: %s", r.stdout)
	}
	r = h.run("orders", "status", "--intent", id, "--json")
	if r.data()["state"] != "submitted" || r.data()["order_id"].(float64) != 987654321 {
		t.Fatalf("status: %s", r.stdout)
	}
	h.fake.confirm = nil
	if r := h.run("orders", "submit", "--intent", id2, "--digest", dg2, "--json"); r.code != 0 {
		t.Fatalf("after reconcile: %s", r.stdout)
	}
}

func TestUndecodableConfirmSuccessIsUnknown(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.confirm = func() (int, string) { return 200, `{"status":` }
	id, dg := prepare(t, h)
	if r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json"); r.errCode() != "OUTCOME_UNKNOWN" {
		t.Fatalf("got %s", r.stdout)
	}
}

func TestDefiniteRejectIsRejected(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.confirm = func() (int, string) { return 400, `{"code":"invalid_argument","message":"bad signature"}` }
	id, dg := prepare(t, h)
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "REJECTED" || r.env["error"].(map[string]any)["execution_state"] != "rejected" {
		t.Fatalf("got %s", r.stdout)
	}
}

func TestMarketOrderSlippageBound(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	r := h.run("orders", "prepare", "--market", "BTC", "--side", "buy", "--type", "market", "--size", "0.001", "--json")
	if r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("market without slippage: %s", r.stdout)
	}
	r = h.run("orders", "prepare", "--market", "BTC", "--side", "buy", "--type", "market", "--size", "0.001", "--slippage-bps", "50", "--json")
	d := r.data()
	if d["intent"].(map[string]any)["worst_px"] != "50250" {
		t.Fatalf("worst px: %s", r.stdout)
	}
	// Server price 50500 exceeds the 50 bps bound: never signed.
	r = h.run("orders", "submit", "--intent", d["intent_id"].(string), "--digest", d["digest"].(string), "--json")
	if r.errCode() != "PAYLOAD_MISMATCH" || h.fake.confirms != 0 {
		t.Fatalf("got %s", r.stdout)
	}
}

func TestProfileBindingEnforced(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	id, dg := prepare(t, h)
	// Hand-edit the config to point the enrolled profile at another API.
	cfgPath := filepath.Join(os.Getenv("OKU_CONFIG_DIR"), "config.json")
	raw, _ := os.ReadFile(cfgPath)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-oku-device-token") != "" {
			t.Error("token sent to a different origin")
		}
	}))
	defer other.Close()
	os.WriteFile(cfgPath, bytes.Replace(raw, []byte(h.srv.URL), []byte(other.URL), 1), 0o600)
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "CONFIG_ERROR" {
		t.Fatalf("got %s", r.stdout)
	}
	if r := h.run("auth", "status", "--json"); r.errCode() != "CONFIG_ERROR" {
		t.Fatalf("status: %s", r.stdout)
	}
	if r := h.run("orders", "prepare", "--market", "BTC", "--side", "buy", "--type", "limit", "--size", "1", "--price", "1", "--account", "0x2222222222222222222222222222222222222222", "--json"); r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("trade account override: %s", r.stdout)
	}
}

func TestNoPassphraseFailsClosed(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Keychain uses OS authorization rather than a file passphrase")
	}
	h := newHarness(t)
	h.enroll()
	id, dg := prepare(t, h)
	h.passFn = ""
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "SIGNER_UNAVAILABLE" || h.fake.creates != 0 {
		t.Fatalf("got %s", r.stdout)
	}
}

func TestRevokeReportsBoundaries(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	r := h.run("auth", "revoke", "--json")
	if r.code != 0 || r.data()["remote_status"] != "revoked" || r.data()["boundaries"] == nil {
		t.Fatalf("revoke: %s", r.stdout)
	}
	if r := h.run("orders", "prepare", "--market", "BTC", "--side", "buy", "--type", "limit", "--size", "1", "--price", "1", "--json"); r.code != 0 {
		t.Fatalf("prepare is local and should still work: %s", r.stdout)
	}
}

func TestJSONContractAndStderr(t *testing.T) {
	h := newHarness(t)
	h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL)
	r := h.run("positions", "list", "--json")
	if r.code != 0 || r.env["schema_version"] != "oku.cli/v1" || r.env["request_id"] == "" || r.stderr != "" {
		t.Fatalf("got %q stderr %q", r.stdout, r.stderr)
	}
	if pos := r.data()["positions"]; pos == nil {
		t.Fatal("positions must be [] not null")
	}
	r = h.run("bogus", "--json")
	if r.code != 2 || r.errCode() != "USAGE" {
		t.Fatalf("unknown command: %d %s", r.code, r.stdout)
	}
	r = h.run("account", "summary", "--profile", "nope", "--json")
	if r.errCode() != "PROFILE_NOT_FOUND" {
		t.Fatalf("got %s", r.stdout)
	}
	r = h.run("orders", "prepare", "--market", "BTC", "--side", "buy", "--type", "limit", "--size", "1e-3", "--price", "50000", "--json")
	if r.errCode() != "INVALID_DECIMAL" {
		t.Fatalf("got %s", r.stdout)
	}
}

func TestFrameworkUsageErrorsRemainSingleJSONEnvelope(t *testing.T) {
	h := newHarness(t)
	cases := [][]string{
		{"--json", "--bogus", "version"},
		{"version", "--bogus", "--json"},
		{"--json", "--timeout", "bad", "version"},
		{"--json", "markets", "quote", "BTC", "--timeout", "bad"},
		{"orders", "prepare", "--json", "--slippage-bps", "notint"},
		{"markets", "nosuch", "--json"},
		{"--json", "markets"},
		{"markets", "quote", "--", "-BTC", "--json"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			r := h.run(args...)
			if r.code != 2 || r.errCode() != "USAGE" || r.stderr != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", r.code, r.stdout, r.stderr)
			}
			var single map[string]any
			if err := json.Unmarshal([]byte(r.stdout), &single); err != nil || single["command"] == "" {
				t.Fatalf("stdout is not one error envelope: %q (%v)", r.stdout, err)
			}
		})
	}
	if len(h.fake.calls) != 0 {
		t.Fatalf("parse failures contacted API: %v", h.fake.calls)
	}
}

func TestCompletionNeverRunsCommandHandlers(t *testing.T) {
	h := newHarness(t)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		r := h.run("completion", shell)
		if r.code != 0 || !strings.Contains(r.stdout, "--generate-shell-completion") || r.stderr != "" {
			t.Fatalf("%s script: exit=%d stdout=%q stderr=%q", shell, r.code, r.stdout, r.stderr)
		}
	}
	for _, args := range [][]string{
		{"orders", "submit", "--json", "--generate-shell-completion"},
		{"auth", "setup", "--json", "--generate-shell-completion"},
		{"orders", "cancel", "--generate-shell-completion"},
		{"profiles", "remove", "--generate-shell-completion"},
	} {
		r := h.run(args...)
		if r.code != 0 || strings.Contains(r.stdout, "schema_version") || r.stderr != "" {
			t.Fatalf("completion %v: exit=%d stdout=%q stderr=%q", args, r.code, r.stdout, r.stderr)
		}
	}
	if len(h.fake.calls) != 0 {
		t.Fatalf("completion contacted API: %v", h.fake.calls)
	}
}

func TestBashCompletionDoesNotEvaluateTypedWords(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	h := newHarness(t)
	script := filepath.Join(t.TempDir(), "completion.sh")
	if err := os.WriteFile(script, []byte(h.run("completion", "bash").stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	// Use a spaced executable path to exercise argv quoting, not eval/string joins.
	app := filepath.Join(t.TempDir(), "oku executable")
	if err := os.WriteFile(app, []byte("#!/bin/sh\nprintf 'orders:order commands\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(t.TempDir(), "created.txt")
	for _, payload := range []string{
		"> " + sentinel,
		"$(touch " + created + ")",
		"`touch " + created + "`",
		"; touch " + created,
	} {
		cmd := exec.Command("bash", "--noprofile", "--norc", "-c", `source "$SCRIPT"; COMP_WORDS=("$APP" markets list "$PAYLOAD" ''); COMP_CWORD=4; __oku_bash_autocomplete`)
		cmd.Env = append(os.Environ(), "SCRIPT="+script, "APP="+app, "PAYLOAD="+payload)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("completion %q: %s (%v)", payload, out, err)
		}
		if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "unchanged" {
			t.Fatalf("completion changed sentinel for %q: %q (%v)", payload, raw, err)
		}
		if _, err := os.Stat(created); !os.IsNotExist(err) {
			t.Fatalf("completion executed %q: %v", payload, err)
		}
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-c", `source "$SCRIPT"; COMP_WORDS=("$APP" ord); COMP_CWORD=1; __oku_bash_autocomplete; printf '%s' "${COMPREPLY[0]}"`)
	cmd.Env = append(os.Environ(), "SCRIPT="+script, "APP="+app)
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "orders" {
		t.Fatalf("spaced path: %q (%v)", out, err)
	}
}

// Backend ConfirmAction returns {"status":"submitted"} with no hlOid when the
// exchange outcome is unknown (workflow_main.go handleUnknownSubmission).
// That must stay unknown through confirm and reconcile and block new orders.
func TestSubmittedWithoutOrderIDStaysUnknown(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.confirm = func() (int, string) {
		h.fake.actions["act1"] = "submitted"
		return 200, `{"status":"submitted"}`
	}
	id, dg := prepare(t, h)
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.code != 6 || r.errCode() != "OUTCOME_UNKNOWN" {
		t.Fatalf("confirm submitted without oid: %d %s", r.code, r.stdout)
	}
	r = h.run("orders", "reconcile", "--json")
	if r.code != 0 || r.data()["still_unknown"].(float64) != 1 {
		t.Fatalf("reconcile cleared unknown without oid: %s", r.stdout)
	}
	id2, dg2 := prepare(t, h)
	r = h.run("orders", "submit", "--intent", id2, "--digest", dg2, "--json")
	if r.errCode() != "OUTCOME_UNKNOWN" || h.fake.confirms != 1 || h.fake.creates != 1 {
		t.Fatalf("second exposure not blocked: %s confirms=%d creates=%d", r.stdout, h.fake.confirms, h.fake.creates)
	}
	// Terminal authoritative state resolves it.
	h.fake.actions["act1"] = "cancelled"
	r = h.run("orders", "reconcile", "--json")
	if r.data()["still_unknown"].(float64) != 0 {
		t.Fatalf("terminal state did not resolve: %s", r.stdout)
	}
}

func TestAbandonOnlyNeverSigned(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.confirm = func() (int, string) { return 503, "" }
	id, dg := prepare(t, h)
	h.run("orders", "submit", "--intent", id, "--digest", dg)
	before := len(h.fake.calls)
	if r := h.run("orders", "abandon", "--intent", id, "--json"); r.errCode() != "OUTCOME_UNKNOWN" {
		t.Fatalf("abandoned a signed intent: %s", r.stdout)
	}
	for _, c := range h.fake.calls[before:] {
		if c == "CancelAction" {
			t.Fatal("unsigned CancelAction sent for a signed intent")
		}
	}
	id2, _ := prepare(t, h)
	if r := h.run("orders", "abandon", "--intent", id2, "--json"); r.code != 0 || r.data()["state"] != "abandoned" {
		t.Fatalf("abandon prepared: %s", r.stdout)
	}
}

// Regression for M-03: the executable must parse the Int64 --order-id flag.
func TestCancelAcceptsOrderIDThroughExecutable(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	r := h.run("orders", "cancel", "--market", "BTC", "--order-id", "123", "--json")
	if r.code != 0 {
		t.Fatalf("valid --order-id refused: %s", r.stdout)
	}
	raw := h.fake.lastSigned["signedAction"].(map[string]any)["actionJson"].(string)
	if !strings.Contains(raw, `"o":123`) {
		t.Fatalf("signed cancel does not target oid 123: %s", raw)
	}
}

// Regression for M-02: a case-aliased economic key is never signed or sent.
func TestCaseAliasActionNeverSigned(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.actionJSON = func(o map[string]any) string {
		return strings.Replace(defaultOrderAction(o), `"s":"0.001"`, `"s":"100","S":"0.001"`, 1)
	}
	id, dg := prepare(t, h, "--size", "0.001")
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if h.fake.confirms != 0 || r.errCode() != "PAYLOAD_MISMATCH" {
		t.Fatalf("aliased action signed/sent (confirms=%d): %s", h.fake.confirms, r.stdout)
	}
}

// Regression for M-04: a definite rejection is persisted as rejected, so
// durable state matches the REJECTED result and does not block the account.
func TestDefiniteRejectPersisted(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.confirm = func() (int, string) { return 400, `{"code":"invalid_argument","message":"bad signature"}` }
	id, dg := prepare(t, h)
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "REJECTED" {
		t.Fatalf("submit: %s", r.stdout)
	}
	if strings.Contains(r.stdout, "journal_error") {
		t.Fatalf("journal write failed: %s", r.stdout)
	}
	r = h.run("orders", "status", "--intent", id, "--json")
	if r.data()["state"] != "rejected" || !strings.Contains(fmt.Sprint(r.data()["last_error"]), "invalid_argument") {
		t.Fatalf("definite rejection not persisted: %s", r.stdout)
	}
	h.fake.confirm = nil
	id2, dg2 := prepare(t, h)
	if r = h.run("orders", "submit", "--intent", id2, "--digest", dg2, "--json"); r.code != 0 {
		t.Fatalf("account still blocked after definite rejection: %s", r.stdout)
	}
}

// Ambiguous Connect codes stay unknown.
func TestAmbiguousCodeConfirmStaysUnknown(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.confirm = func() (int, string) { return 500, `{"code":"internal","message":"boom"}` }
	id, dg := prepare(t, h)
	r := h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.errCode() != "OUTCOME_UNKNOWN" {
		t.Fatalf("submit: %s", r.stdout)
	}
	if r = h.run("orders", "status", "--intent", id, "--json"); r.data()["state"] != "unknown" {
		t.Fatalf("state: %s", r.stdout)
	}
}
