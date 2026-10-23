package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/auth"
)

func TestSignerRefreshesAndAdoptsAtAuthenticationBoundary(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	md, sec, err := auth.LoadPrivy("main")
	if err != nil {
		t.Fatal(err)
	}
	p, err := (&env{app: NewApp("t")}).profile()
	if err != nil {
		t.Fatal(err)
	}
	expected := "access-1"
	expectedRefresh := "refresh-1"
	checked := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wallets/authenticate") {
			_, persisted, err := auth.LoadPrivy("main")
			if err != nil || persisted.AccessToken != expected || persisted.RefreshToken != expectedRefresh || r.Header.Get("Authorization") != "Bearer "+expected {
				t.Errorf("not persisted before authenticate: %+v %v header=%q", persisted, err, r.Header.Get("Authorization"))
			}
			checked++
		}
		h.privy.serve(w, r)
	}))
	defer proxy.Close()
	// signerFor re-saves the credential (full-cost scrypt) inside this request
	// deadline. 5s was too tight for -race on CI runners.
	e := &env{app: NewApp("t"), ctx: context.Background(), g: globals{timeout: maxTimeout}}
	e.app.Getenv = func(name string) string {
		switch name {
		case "OKU_PRIVY_APP_ID":
			return md.PrivyAppID
		case "OKU_PRIVY_AUTH_URL":
			return proxy.URL
		}
		return ""
	}
	s, err := e.unlock(p, "")
	if err != nil {
		t.Fatal(err)
	}
	md, sec, err = auth.LoadPrivy("main")
	if err != nil {
		t.Fatal(err)
	}
	md.AccessExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	if err := auth.SavePrivy("main", *md, *sec); err != nil {
		t.Fatal(err)
	}
	if _, err := e.signerFor(p, s); err != nil {
		t.Fatal(err)
	}
	if checked != 1 || h.privy.refreshes != 1 {
		t.Fatalf("authenticate=%d refresh=%d", checked, h.privy.refreshes)
	}
	// A second process has already rotated again. Reload must adopt its token,
	// not refresh using the now-invalid previous refresh token.
	md, sec, err = auth.LoadPrivy("main")
	if err != nil {
		t.Fatal(err)
	}
	md.AccessExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	sec.AccessToken, sec.RefreshToken = "access-newer", "refresh-newer"
	md.Generation++
	if err := auth.SavePrivy("main", *md, *sec); err != nil {
		t.Fatal(err)
	}
	expected = "access-newer"
	expectedRefresh = "refresh-newer"
	if _, err := e.signerFor(p, s); err != nil {
		t.Fatal(err)
	}
	if checked != 2 || h.privy.refreshes != 1 {
		t.Fatalf("new credential not adopted: authenticate=%d refresh=%d", checked, h.privy.refreshes)
	}
}
