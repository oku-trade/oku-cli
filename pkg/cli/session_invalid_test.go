package cli

import (
	"context"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/auth"
)

func TestRevokedFutureSessionReexchangesOnNextOperation(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	pass := []byte("correct horse battery")
	md, sec, err := auth.UnlockPrivy("main", pass)
	if err != nil {
		t.Fatal(err)
	}
	md.SessionExpiresAt = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if err := auth.SavePrivy("main", *md, *sec, pass); err != nil {
		t.Fatal(err)
	}
	p, err := (&env{app: NewApp("t")}).profile()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{app: NewApp("t"), ctx: context.Background(), g: globals{timeout: 5 * time.Second}}
	e.app.Getenv = func(name string) string {
		if name == "OKU_PASSPHRASE_FILE" {
			return h.passFn
		}
		if name == "OKU_PRIVY_APP_ID" {
			return md.PrivyAppID
		}
		if name == "OKU_PRIVY_AUTH_URL" {
			return h.privy.server.URL
		}
		return ""
	}
	s, err := e.unlock(p, "")
	if err != nil {
		t.Fatal(err)
	}
	c := e.client(p)
	s.apply(c)
	h.fake.rejectExtend = true
	// The old session has a distant expiry but is rejected by the server.
	c.SessionToken = "revoked-token"
	// Keep callback's captured session and wire headers aligned with the failed credential.
	s.token = "revoked-token"
	md, sec, err = auth.UnlockPrivy("main", pass)
	if err != nil {
		t.Fatal(err)
	}
	sec.SessionToken = "revoked-token"
	if err := auth.SavePrivy("main", *md, *sec, pass); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = c.Perps(ctx, "CreateAction", map[string]any{}, nil, true)
	if ae, ok := api.AsError(err); !ok || ae.Code != "unauthenticated" {
		t.Fatalf("expected definite rejection: %v", err)
	}
	md, sec, err = auth.UnlockPrivy("main", pass)
	if err != nil || md.SessionID != "" || sec.SessionToken != "" {
		t.Fatalf("session not invalidated: %+v %+v %v", md, sec, err)
	}
	h.fake.rejectExtend = false
	if _, err := e.unlock(p, ""); err != nil {
		t.Fatalf("next operation did not reexchange: %v", err)
	}
}

func TestInvalidationPreservesConcurrentSession(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	pass := []byte("correct horse battery")
	md, sec, err := auth.UnlockPrivy("main", pass)
	if err != nil {
		t.Fatal(err)
	}
	failed := &session{md: md, sessionID: md.SessionID, token: sec.SessionToken}
	md.SessionID, sec.SessionToken = "newer-session", "newer-token"
	md.Generation++
	if err := auth.SavePrivy("main", *md, *sec, pass); err != nil {
		t.Fatal(err)
	}
	e := &env{app: NewApp("t")}
	e.app.Getenv = func(name string) string {
		if name == "OKU_PASSPHRASE_FILE" {
			return h.passFn
		}
		return ""
	}
	e.invalidateSession("main", failed)
	after, secrets, err := auth.UnlockPrivy("main", pass)
	if err != nil || after.SessionID != "newer-session" || secrets.SessionToken != "newer-token" {
		t.Fatalf("newer credential overwritten: %+v %+v %v", after, secrets, err)
	}
}
