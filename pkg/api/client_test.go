package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The device token must never follow a redirect to another origin.
func TestRedirectNotFollowedAndTokenNotLeaked(t *testing.T) {
	leaked := false
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(DeviceTokenHeader) != "" {
			leaked = true
		}
		w.Write([]byte(`{}`))
	}))
	defer evil.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	c := &Client{BaseURL: origin.URL, HTTP: NewHTTPClient(5 * time.Second), Token: "okud_secret"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.Perps(ctx, "GetOpenOrders", map[string]string{}, &struct{}{}, true)
	e, ok := AsError(err)
	if !ok || e.Code != "redirect_refused" || e.Ambiguous {
		t.Fatalf("err=%v", err)
	}
	if leaked {
		t.Fatal("token forwarded across redirect")
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		code      string
		ambiguous bool
	}{
		{404, `{"code":"not_found","message":"market NOPE not found"}`, "not_found", false},
		{503, `upstream down`, "unavailable", true},
		{500, `{"code":"internal","message":"x"}`, "internal", true},
		{429, ``, "resource_exhausted", false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Connect-Protocol-Version") != "1" || r.Header.Get("Connect-Timeout-Ms") == "" {
				t.Error("missing connect headers")
			}
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		}))
		cl := &Client{BaseURL: srv.URL, HTTP: NewHTTPClient(5 * time.Second)}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		e, _ := AsError(cl.Perps(ctx, "GetMarkets", struct{}{}, nil, false))
		cancel()
		srv.Close()
		if e == nil || e.Code != c.code || e.Ambiguous != c.ambiguous {
			t.Errorf("status %d: got %+v", c.status, e)
		}
	}
}

func TestTimeoutIsAmbiguous(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	cl := &Client{BaseURL: srv.URL, HTTP: NewHTTPClient(5 * time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	e, _ := AsError(cl.Perps(ctx, "ConfirmAction", struct{}{}, nil, false))
	if e == nil || e.Code != "deadline_exceeded" || !e.Ambiguous {
		t.Fatalf("got %+v", e)
	}
}

// Success responses that cannot be decoded, or are truncated/oversized,
// leave the effect of a mutation unknown and must be marked ambiguous.
func TestUndecodableSuccessIsAmbiguous(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":   "<html>",
		"truncated": `{"status":"subm`,
		"oversized": `{"x":"` + strings.Repeat("a", maxBody) + `"}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		cl := &Client{BaseURL: srv.URL, HTTP: NewHTTPClient(5 * time.Second)}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var out struct{ Status string }
		e, _ := AsError(cl.Perps(ctx, "ConfirmAction", struct{}{}, &out, false))
		cancel()
		srv.Close()
		if e == nil || !e.Ambiguous {
			t.Errorf("%s: got %+v", name, e)
		}
	}
}

// Server error text must not reflect credentials into CLI output.
func TestErrorMessagesDoNotReflectSecrets(t *testing.T) {
	const secret = "okud_abcdefghijklmnopqrstuvwxyz0123456789"
	const poll = "Zm9vYmFyYmF6cXV4cXV1eHF1dXhxdXV4cXV1eA"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"code":"invalid_argument","message":"bad token ` + secret + ` poll ` + poll + `"}`))
	}))
	defer srv.Close()
	cl := &Client{BaseURL: srv.URL, HTTP: NewHTTPClient(5 * time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, err := range []error{
		cl.Perps(ctx, "GetOpenOrders", struct{}{}, nil, false),
		cl.Device(ctx, "PollEnrollment", struct{}{}, nil, false),
	} {
		msg := err.Error()
		if strings.Contains(msg, "okud_") || strings.Contains(msg, poll) {
			t.Errorf("secret reflected: %s", msg)
		}
	}
	if m := cl.Device(ctx, "PollEnrollment", struct{}{}, nil, false).Error(); strings.Contains(m, "bad token") {
		t.Errorf("device error text surfaced: %s", m)
	}
}
