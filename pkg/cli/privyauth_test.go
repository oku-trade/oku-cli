package cli

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hpke"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/signing"
)

// fakePrivy exercises the actual device grant, HPKE wallet authentication and
// signed typed-data RPC through the CLI's public command boundary.
type fakePrivy struct {
	t                                            *testing.T
	mu                                           sync.Mutex
	server                                       *httptest.Server
	signer                                       *signing.KeySigner
	authorizations, pending, refreshes, rpcCalls int
	failSign                                     bool
	wrongSigner                                  bool
}

func newFakePrivy(t *testing.T) *fakePrivy {
	t.Helper()
	key, err := signing.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := signing.NewKeySigner(key)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePrivy{t: t, signer: signer}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fakePrivy) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			f.t.Error(err)
		}
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/device_authorization"):
		f.authorizations++
		reply(map[string]any{"device_code": "device-secret", "user_code": "WV225-WZBZM", "verification_uri": "https://oku.trade/agent/authorize", "verification_uri_complete": "https://oku.trade/agent/authorize?user_code=WV225-WZBZM", "expires_in": 600, "interval": 1})
	case strings.HasSuffix(r.URL.Path, "/token"):
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["grant_type"] == "refresh_token" {
			f.refreshes++
			if b["refresh_token"] != fmt.Sprintf("refresh-%d", f.refreshes-1) {
				w.WriteHeader(400)
				reply(map[string]string{"error": "invalid_grant"})
				return
			}
			reply(map[string]any{"access_token": fmt.Sprintf("access-%d", f.refreshes), "refresh_token": fmt.Sprintf("refresh-%d", f.refreshes), "expires_in": 900})
			return
		}
		if f.pending == 0 {
			f.pending++
			w.WriteHeader(400)
			reply(map[string]string{"error": "authorization_pending"})
			return
		}
		reply(map[string]any{"access_token": "access-0", "refresh_token": "refresh-0", "expires_in": 900})
	case strings.HasSuffix(r.URL.Path, "/wallets/authenticate"):
		var input struct {
			RecipientPublicKey string `json:"recipient_public_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			f.t.Error(err)
			return
		}
		der, err := base64.StdEncoding.DecodeString(input.RecipientPublicKey)
		if err != nil {
			f.t.Error(err)
			return
		}
		parsed, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			f.t.Error(err)
			return
		}
		ecPub, err := parsed.(*ecdsa.PublicKey).ECDH()
		if err != nil {
			f.t.Error(err)
			return
		}
		hpkePub, err := hpke.NewDHKEMPublicKey(ecPub)
		if err != nil {
			f.t.Error(err)
			return
		}
		enc, sender, err := hpke.NewSender(hpkePub, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), nil)
		if err != nil {
			f.t.Error(err)
			return
		}
		authKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			f.t.Error(err)
			return
		}
		pkcs8, err := x509.MarshalPKCS8PrivateKey(authKey)
		if err != nil {
			f.t.Error(err)
			return
		}
		ciphertext, err := sender.Seal(nil, []byte("wallet-auth:"+base64.StdEncoding.EncodeToString(pkcs8)))
		if err != nil {
			f.t.Error(err)
			return
		}
		reply(map[string]any{"encrypted_authorization_key": map[string]string{"encapsulated_key": base64.StdEncoding.EncodeToString(enc), "ciphertext": base64.StdEncoding.EncodeToString(ciphertext)}, "expires_at": time.Now().Add(time.Hour).UnixMilli(), "wallets": []map[string]string{{"id": "agent-wallet", "address": f.signer.Address(), "chain_type": "ethereum"}}})
	case strings.HasSuffix(r.URL.Path, "/rpc"):
		f.rpcCalls++
		if f.failSign {
			w.WriteHeader(503)
			reply(map[string]string{"error": "signing_unavailable"})
			return
		}
		var data struct {
			Params struct {
				TypedData struct {
					Message struct {
						ConnectionID string `json:"connectionId"`
					} `json:"message"`
				} `json:"typed_data"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			f.t.Error(err)
			return
		}
		connBytes, err := hex.DecodeString(strings.TrimPrefix(data.Params.TypedData.Message.ConnectionID, "0x"))
		if err != nil || len(connBytes) != 32 {
			f.t.Errorf("bad connection id: %q %v", data.Params.TypedData.Message.ConnectionID, err)
			return
		}
		var conn [32]byte
		copy(conn[:], connBytes)
		sig, err := f.signer.SignDigest(signing.AgentDigest(conn))
		if f.wrongSigner {
			key, keyErr := signing.GenerateKey()
			if keyErr != nil {
				f.t.Error(keyErr)
				return
			}
			other, keyErr := signing.NewKeySigner(key)
			if keyErr != nil {
				f.t.Error(keyErr)
				return
			}
			sig, err = other.SignDigest(signing.AgentDigest(conn))
		}
		if err != nil {
			f.t.Error(err)
			return
		}
		rb, _ := hex.DecodeString(strings.TrimPrefix(sig.R, "0x"))
		sb, _ := hex.DecodeString(strings.TrimPrefix(sig.S, "0x"))
		raw := append(append(rb, sb...), byte(sig.V))
		reply(map[string]any{"signature": "0x" + hex.EncodeToString(raw)})
	case strings.Contains(r.URL.Path, "/grants"):
		reply(map[string]any{"grants": []any{}})
	default:
		w.WriteHeader(404)
		reply(map[string]string{"error": "not_found"})
	}
}

func TestPrivyRefreshRotationPersistedAndAdopted(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	pass := []byte("correct horse battery")
	md, sec, err := auth.UnlockPrivy("main", pass)
	if err != nil {
		t.Fatal(err)
	}
	md.AccessExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	if err = auth.SavePrivy("main", *md, *sec, pass); err != nil {
		t.Fatal(err)
	}
	if r := h.run("auth", "status", "--verify", "--json"); r.code != 0 {
		t.Fatalf("refresh: %s", r.stdout)
	}
	_, after, err := auth.UnlockPrivy("main", pass)
	if err != nil || after.RefreshToken != "refresh-1" {
		t.Fatalf("rotation not persisted: %v %+v", err, after)
	}
	if r := h.run("auth", "status", "--verify", "--json"); r.code != 0 {
		t.Fatalf("second process: %s", r.stdout)
	}
	if h.privy.refreshes != 1 {
		t.Fatalf("refresh repeated with stale token: %d", h.privy.refreshes)
	}
}

func TestPrivyLoginRejectsAccountFlag(t *testing.T) {
	h := newHarness(t)
	other := "0x2222222222222222222222222222222222222222"
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	if r := h.run("--account", other, "auth", "login", "--no-open", "--json"); r.errCode() != "USAGE" || !strings.Contains(r.stdout, "wallet you sign with") {
		t.Fatalf("login accepted account flag: %s", r.stdout)
	}
}

func TestPrivySessionReexchangeDropsExpiredHeaders(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	pass := []byte("correct horse battery")
	md, sec, err := auth.UnlockPrivy("main", pass)
	if err != nil {
		t.Fatal(err)
	}
	md.SessionExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	if err = auth.SavePrivy("main", *md, *sec, pass); err != nil {
		t.Fatal(err)
	}
	h.fake.rejectExtend = true
	if r := h.run("auth", "status", "--verify", "--json"); r.code != 0 {
		t.Fatalf("reexchange: %s", r.stdout)
	}
}

func TestPrivyLoginRequiresTerminalBeforeDeviceGrant(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	a := NewApp("test")
	var stdout, stderr bytes.Buffer
	a.Stdout, a.Stderr = &stdout, &stderr
	a.StdinIsTTY = false
	a.Getenv = func(k string) string {
		if k == "OKU_PRIVY_AUTH_URL" {
			return h.privy.server.URL
		}
		return ""
	}
	if code := a.Run([]string{"auth", "login", "--no-open", "--json"}); code == 0 {
		t.Fatal("noninteractive login accepted")
	}
	if h.privy.authorizations != 0 {
		t.Fatal("noninteractive login initiated device grant")
	}
}

func TestPrivySignFailureReturnsToCreated(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	id, digest := prepare(t, h)
	h.privy.failSign = true
	r := h.run("orders", "submit", "--intent", id, "--digest", digest, "--json")
	if r.errCode() != "SIGNER_UNAVAILABLE" || h.fake.confirms != 0 {
		t.Fatalf("failed signing sent action: %s", r.stdout)
	}
	j, err := journal.Open("main")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := j.Get(id)
	j.Close()
	if err != nil || rec.State != journal.StateCreated {
		t.Fatalf("record after sign failure: %+v %v", rec, err)
	}
	h.privy.failSign = false
	r = h.run("orders", "submit", "--intent", id, "--digest", digest, "--json")
	if r.code != 0 || h.fake.confirms != 1 {
		t.Fatalf("retry blocked: %s", r.stdout)
	}
}

func TestPrivyWrongSignerNeverReachesConfirm(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	id, digest := prepare(t, h)
	h.privy.wrongSigner = true
	r := h.run("orders", "submit", "--intent", id, "--digest", digest, "--json")
	if r.errCode() != "SIGNER_UNAVAILABLE" || h.fake.confirms != 0 {
		t.Fatalf("wrong signer accepted: %s", r.stdout)
	}
	j, err := journal.Open("main")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := j.Get(id)
	j.Close()
	if err != nil || rec.State != journal.StateCreated {
		t.Fatalf("wrong signer left gate: %+v %v", rec, err)
	}
}
