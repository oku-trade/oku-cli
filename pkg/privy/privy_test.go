package privy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gowebpki/jcs"
	"github.com/oku-trade/oku-cli/pkg/signing"
	"golang.org/x/crypto/sha3"
)

func TestDeviceFlow(t *testing.T) {
	steps := []string{"authorization_pending", "slow_down", "authorization_pending", "success"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("privy-app-id") != "app" {
			t.Error("missing app id")
		}
		if strings.HasSuffix(r.URL.Path, "/device_authorization") {
			fmt.Fprint(w, `{"device_code":"secret-device","user_code":"ABCD","verification_uri":"https://verify","verification_uri_complete":"https://verify/ABCD","expires_in":100,"interval":2}`)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" || body["device_code"] != "secret-device" {
			t.Errorf("device grant mismatch: %v", body)
		}
		step := steps[calls]
		calls++
		if step != "success" {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":%q}`, step)
			return
		}
		fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","expires_in":900}`)
	}))
	defer server.Close()
	c := &Client{AuthURL: server.URL, AppID: "app"}
	da, err := c.StartDeviceAuthorization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var delays []time.Duration
	tokens, err := c.PollDeviceToken(context.Background(), da, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil })
	if err != nil || tokens.AccessToken != "access" {
		t.Fatalf("tokens=%+v error=%v", tokens, err)
	}
	if want := []time.Duration{2 * time.Second, 2 * time.Second, 7 * time.Second, 7 * time.Second}; !reflect.DeepEqual(delays, want) {
		t.Fatalf("delays %v want %v", delays, want)
	}
}
func TestDeviceTerminalAndDeadline(t *testing.T) {
	for _, test := range []struct {
		code     string
		sentinel error
	}{{"access_denied", ErrAccessDenied}, {"expired_token", ErrExpiredToken}} {
		t.Run(test.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error":%q}`, test.code)
			}))
			defer server.Close()
			c := &Client{AuthURL: server.URL}
			_, err := c.PollDeviceToken(context.Background(), &DeviceAuthorization{DeviceCode: "x", ExpiresIn: 30}, func(context.Context, time.Duration) error { return nil })
			if !errors.Is(err, test.sentinel) {
				t.Fatal(err)
			}
		})
	}
	clock := time.Now()
	c := &Client{Now: func() time.Time { return clock }}
	_, err := c.PollDeviceToken(context.Background(), &DeviceAuthorization{DeviceCode: "x", ExpiresIn: 5}, func(context.Context, time.Duration) error { clock = clock.Add(5 * time.Second); return nil })
	if !errors.Is(err, ErrExpiredToken) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.PollDeviceToken(ctx, &DeviceAuthorization{DeviceCode: "x", ExpiresIn: 5}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestRefreshAndErrors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/api/oauth/v2/token" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["grant_type"] != "refresh_token" || body["refresh_token"] != "secret-refresh" {
				t.Errorf("bad refresh body: %v", body)
			}
			fmt.Fprint(w, `{"access_token":"new","refresh_token":"rotated","expires_in":120}`)
			return
		}
		w.Header().Set("Retry-After", "17")
		w.Header().Set("x-request-id", "rid")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"secret-refresh","error_description":"secret-refresh"}`)
	}))
	defer server.Close()
	c := &Client{AuthURL: server.URL}
	tokens, err := c.Refresh(context.Background(), "secret-refresh")
	if err != nil || tokens.RefreshToken != "rotated" {
		t.Fatalf("refresh %+v %v", tokens, err)
	}
	err = c.request(context.Background(), "GET", "/failure", "secret-refresh", nil, false, "", nil)
	var pe *Error
	if !errors.As(err, &pe) || pe.Status != 429 || pe.RetryAfter != "17" || strings.Contains(err.Error(), "secret-refresh") {
		t.Fatalf("unsafe error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("unexpected retries: %d", calls)
	}
	for _, bad := range []tokenResponse{{AccessToken: "a", ExpiresIn: 1}, {RefreshToken: "r", ExpiresIn: 1}, {AccessToken: "a", RefreshToken: "r"}} {
		if _, err := c.tokens(bad); err == nil {
			t.Fatal("accepted incomplete tokens")
		}
	}
}
func TestAuthenticateSignedRPCAndTypedData(t *testing.T) {
	authKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(authKey)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	rpcCalls := 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-access" || r.Header.Get("privy-grant-type") != "device_code" || r.Header.Get("privy-app-id") != "app" {
			t.Error("missing auth headers")
		}
		if strings.HasSuffix(r.URL.Path, "/wallets/authenticate") {
			var request struct {
				EncryptionType     string `json:"encryption_type"`
				RecipientPublicKey string `json:"recipient_public_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			der, err := base64.StdEncoding.DecodeString(request.RecipientPublicKey)
			if err != nil {
				t.Error(err)
			}
			pub, err := x509.ParsePKIXPublicKey(der)
			if err != nil {
				t.Error(err)
			}
			ecdsaPub, ok := pub.(*ecdsa.PublicKey)
			if !ok || request.EncryptionType != "HPKE" {
				t.Error("incorrect recipient")
				return
			}
			ecPub, err := ecdsaPub.ECDH()
			if err != nil {
				t.Error(err)
				return
			}
			hpkePub, err := hpke.NewDHKEMPublicKey(ecPub)
			if err != nil {
				t.Error(err)
			}
			enc, sender, err := hpke.NewSender(hpkePub, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), nil)
			if err != nil {
				t.Error(err)
			}
			ciphertext, err := sender.Seal(nil, []byte("wallet-auth:"+base64.StdEncoding.EncodeToString(pkcs8)))
			if err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"encrypted_authorization_key": map[string]string{"encapsulated_key": base64.StdEncoding.EncodeToString(enc), "ciphertext": base64.StdEncoding.EncodeToString(ciphertext)}, "expires_at": time.Now().Add(time.Hour).UnixMilli(), "wallets": []Wallet{{ID: "wallet", Address: "0xabc", ChainType: "ethereum"}}})
			return
		}
		rpcCalls++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var body any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		signed, _ := json.Marshal(map[string]any{"version": 1, "method": "POST", "url": server.URL + r.URL.String(), "body": body, "headers": map[string]string{"privy-app-id": "app"}})
		canonical, err := jcs.Transform(signed)
		if err != nil {
			t.Error(err)
		}
		digest := sha256.Sum256(canonical)
		sig, err := base64.StdEncoding.DecodeString(r.Header.Get("privy-authorization-signature"))
		if err != nil || !ecdsa.VerifyASN1(&authKey.PublicKey, digest[:], sig) {
			t.Error("signature verification failed")
		}
		var data struct {
			Method string `json:"method"`
			Params struct {
				TypedData TypedData `json:"typed_data"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &data)
		if data.Method != "eth_signTypedData_v4" || data.Params.TypedData.PrimaryType != "Agent" {
			t.Errorf("bad RPC body %s", raw)
		}
		ethereumSig := make([]byte, 65)
		ethereumSig[64] = byte(rpcCalls - 1)
		if rpcCalls == 1 {
			fmt.Fprintf(w, `{"data":{"signature":"0x%s"}}`, hex.EncodeToString(ethereumSig))
		} else {
			fmt.Fprintf(w, `{"signature":"0x%s"}`, hex.EncodeToString(ethereumSig))
		}
	}))
	defer server.Close()
	c := &Client{AuthURL: server.URL, AppID: "app"}
	s, err := c.Authenticate(context.Background(), "secret-access")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Wallets()) != 1 || s.Wallets()[0].ID != "wallet" {
		t.Fatal("wallets missing")
	}
	for i := 0; i < 2; i++ {
		sig, err := s.SignTypedData(context.Background(), "wallet", HyperliquidAgentTypedData("a", [32]byte{}))
		if err != nil || sig[64] != byte(27+i) {
			t.Fatalf("signature v=%d err=%v", sig[64], err)
		}
	}
}
func testKeccak(parts ...[]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		_, _ = h.Write(p)
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}
func TestAgentTypedDataDigest(t *testing.T) {
	var connection [32]byte
	for i := range connection {
		connection[i] = byte(i)
	}
	d := HyperliquidAgentTypedData("a", connection)
	if d.PrimaryType != "Agent" || d.Domain["chainId"] != 1337 || len(d.Types["Agent"]) != 2 {
		t.Fatal("typed data shape")
	}
	domainFields := d.Types["EIP712Domain"]
	agentFields := d.Types["Agent"]
	encodeType := func(name string, fields []TypedField) string {
		var b strings.Builder
		b.WriteString(name + "(")
		for i, f := range fields {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(f.Type + " " + f.Name)
		}
		b.WriteByte(')')
		return b.String()
	}
	chain := make([]byte, 32)
	chain[30] = 0x05
	chain[31] = 0x39
	name := testKeccak([]byte(d.Domain["name"].(string)))
	version := testKeccak([]byte(d.Domain["version"].(string)))
	dt := testKeccak([]byte(encodeType("EIP712Domain", domainFields)))
	at := testKeccak([]byte(encodeType("Agent", agentFields)))
	domain := testKeccak(dt[:], name[:], version[:], chain, make([]byte, 32))
	source := testKeccak([]byte(d.Message["source"].(string)))
	conn, err := hex.DecodeString(strings.TrimPrefix(d.Message["connectionId"].(string), "0x"))
	if err != nil {
		t.Fatal(err)
	}
	message := testKeccak(at[:], source[:], conn)
	digest := testKeccak([]byte{0x19, 0x01}, domain[:], message[:])
	if digest != signing.AgentDigest(connection) {
		t.Fatalf("digest mismatch: %x != %x", digest, signing.AgentDigest(connection))
	}
}
func TestExpiryForms(t *testing.T) {
	for _, raw := range []string{`"2026-10-14T18:00:00Z"`, `1792000800000`, `"1792000800000"`} {
		if _, err := parseExpiry(json.RawMessage(raw)); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func TestRevokeGrantAndRefreshFailure(t *testing.T) {
	deletes := 0
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer access" {
			switch r.Method {
			case http.MethodGet:
				fmt.Fprint(w, `{"data":[{"id":"device-1","grant_type":"device_code"},{"id":"other","grant_type":"other"}]}`)
			case http.MethodDelete:
				if r.URL.Path != "/api/oauth/v2/grants/device-1" {
					t.Error("wrong grant revoked")
				}
				deletes++
				fmt.Fprint(w, `{"success":true}`)
			}
			return
		}
		refreshes++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer server.Close()
	c := &Client{AuthURL: server.URL}
	if err := c.RevokeGrant(context.Background(), "access"); err != nil || deletes != 1 {
		t.Fatalf("revoke: %v, deletes=%d", err, deletes)
	}
	_, err := c.Refresh(context.Background(), "old-refresh")
	if !errors.Is(err, ErrUnauthorized) || refreshes != 1 {
		t.Fatalf("refresh retried or wrong error: %v, requests=%d", err, refreshes)
	}
}
