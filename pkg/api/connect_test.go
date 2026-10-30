package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The runtime path is the generated Connect client: correct procedure path,
// protocol headers, session token and user agent, exactly one attempt, and
// actionJson forwarded byte-for-byte.
func TestGeneratedClientWire(t *testing.T) {
	var hits int
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.URL.Path != "/connect/gfxcafe.oku.account.v2.PerpsService/ConfirmAction" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get(SessionHeader) != "session" || r.Header.Get(SessionTokenHeader) != "test-session-token" || r.Header.Get("User-Agent") != "oku-cli/test" ||
			r.Header.Get("Connect-Protocol-Version") != "1" || r.Header.Get("Connect-Timeout-Ms") == "" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers %v", r.Header)
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	cl := &Client{BaseURL: srv.URL + "/", HTTP: NewHTTPClient(5 * time.Second), SessionID: "session", SessionToken: "test-session-token", UserAgent: "oku-cli/test"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const aj = `{"type":"updateLeverage","asset":3,"isCross":false,"leverage":5}`
	err := cl.Perps(ctx, "ConfirmAction", ConfirmActionRequest{ActionID: "a", SignedAction: SignedAction{ActionJSON: aj, Nonce: 1, Signature: EVMSignature{R: "0x1", S: "0x2", V: 27}}}, &ConfirmActionResponse{}, true)
	e, _ := AsError(err)
	if e == nil || !e.Ambiguous || e.Code != "unavailable" {
		t.Fatalf("got %v", err)
	}
	if hits != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (no retries)", hits)
	}
	want, _ := jsonString(aj)
	if !strings.Contains(gotBody, want) {
		t.Fatalf("actionJson not forwarded verbatim: %s", gotBody)
	}
}

func jsonString(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	b.WriteString(strings.ReplaceAll(s, `"`, `\"`))
	b.WriteByte('"')
	return b.String(), nil
}

// Requests that do not match the proto schema, and unknown methods, fail
// locally before anything is sent, are not ambiguous, and on sensitive calls
// do not echo request values.
func TestLocalRejectionSendsNothing(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	cl := &Client{BaseURL: srv.URL, HTTP: NewHTTPClient(5 * time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, err := range []error{
		cl.Perps(ctx, "GetMarkets", map[string]string{"bogus": "1"}, nil, false),
		cl.Perps(ctx, "Withdraw", struct{}{}, nil, false),
		cl.Auth(ctx, "ExtendSession", map[string]string{"nope": "SECRETVALUE"}, nil, false),
	} {
		e, _ := AsError(err)
		if e == nil || e.Ambiguous {
			t.Errorf("got %v", err)
		}
		if strings.Contains(err.Error(), "SECRETVALUE") || strings.Contains(err.Error(), "Zm9v") {
			t.Errorf("secret echoed: %v", err)
		}
	}
	if hits != 0 {
		t.Fatalf("%d requests sent", hits)
	}
}

// Servers that omit Content-Type (Go sniffs text/plain) still decode, but
// schema-violating bodies remain ambiguous invalid_response.
func TestMissingContentTypeStillStrict(t *testing.T) {
	for body, ok := range map[string]bool{`{"withdrawable":"1"}`: true, `{"withdrawable":1,"bogus":true}`: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		cl := &Client{BaseURL: srv.URL, HTTP: NewHTTPClient(5 * time.Second)}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := cl.Perps(ctx, "GetPerpPositions", map[string]string{}, &GetPerpPositionsResponse{}, false)
		cancel()
		srv.Close()
		e, _ := AsError(err)
		if ok != (err == nil) || (!ok && (e == nil || !e.Ambiguous)) {
			t.Errorf("%s: %v", body, err)
		}
	}
}
