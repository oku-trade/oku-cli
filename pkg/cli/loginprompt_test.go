package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/auth"
)

// interactiveLogin runs auth login as a terminal user: secrets come from the
// scripted ReadSecret answers and confirmations from stdin.
func (h *harness) interactiveLogin(secrets []string, stdin string) (run, int) {
	h.t.Helper()
	var out, errb bytes.Buffer
	prompts := 0
	a := &App{
		Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(stdin), StdinIsTTY: true,
		ReadSecret: func(string) ([]byte, error) {
			if prompts >= len(secrets) {
				return nil, errNoTTY
			}
			prompts++
			return []byte(secrets[prompts-1]), nil
		},
		Getenv: func(k string) string {
			switch k {
			case "OKU_PRIVY_AUTH_URL":
				return h.privy.server.URL
			case "OKU_HYPERLIQUID_INFO_URL":
				return h.srv.URL + "/info"
			}
			return ""
		},
		Now:     time.Now,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Version: "test",
	}
	code := a.Run([]string{"auth", "login", "--no-open", "--json"})
	r := run{code: code, stdout: out.String(), stderr: errb.String()}
	_ = json.Unmarshal(out.Bytes(), &r.env)
	return r, prompts
}

func (h *harness) addProfile() {
	h.t.Helper()
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
		h.t.Fatalf("profiles add: %s %s", r.stdout, r.stderr)
	}
}

// A short or mismatched passphrase re-prompts instead of failing login, and
// the prompt happens before any device code is issued.
func TestLoginRepromptsBadPassphrase(t *testing.T) {
	h := newHarness(t)
	h.addProfile()
	r, prompts := h.interactiveLogin([]string{"short", "correct horse battery", "typo horse battery", "correct horse battery", "correct horse battery"}, "")
	if r.code != 0 || prompts != 5 {
		t.Fatalf("login code=%d prompts=%d: %s %s", r.code, prompts, r.stdout, r.stderr)
	}
	for _, want := range []string{"at least 8 characters", "do not match", "expires in"} {
		if !strings.Contains(r.stderr, want) {
			t.Fatalf("stderr missing %q: %s", want, r.stderr)
		}
	}
	if strings.Index(r.stderr, "do not match") > strings.Index(r.stderr, "enter code") {
		t.Fatal("passphrase must be chosen before the device code is shown")
	}
	if _, _, err := auth.UnlockPrivy("main", []byte("correct horse battery")); err != nil {
		t.Fatalf("saved credential: %v", err)
	}
}

// Giving up on the passphrase never starts a device authorization.
func TestLoginPassphraseFailureAuthorizesNothing(t *testing.T) {
	h := newHarness(t)
	h.addProfile()
	r, _ := h.interactiveLogin([]string{"a", "b", "c", "d", "e"}, "")
	if r.code == 0 || h.privy.authorizations != 0 {
		t.Fatalf("code=%d authorizations=%d", r.code, h.privy.authorizations)
	}
}

// Skipping the passphrase requires both confirmations; a hesitant answer
// returns to the passphrase prompt.
func TestLoginSkipPassphraseNeedsDoubleConfirm(t *testing.T) {
	h := newHarness(t)
	h.addProfile()
	r, _ := h.interactiveLogin([]string{"", "correct horse battery", "correct horse battery"}, "yes\nno\n")
	if r.code != 0 {
		t.Fatalf("login: %s %s", r.stdout, r.stderr)
	}
	if md, _ := auth.LoadPrivyMetadata("main"); md.Unprotected {
		t.Fatal("declined second confirmation still stored unprotected")
	}

	h2 := newHarness(t)
	h2.addProfile()
	r, _ = h2.interactiveLogin([]string{""}, "yes\nstore without passphrase\n")
	if r.code != 0 || !strings.Contains(r.stderr, "INSECURE") {
		t.Fatalf("login: %s %s", r.stdout, r.stderr)
	}
	md, err := auth.LoadPrivyMetadata("main")
	if err != nil || !md.Unprotected {
		t.Fatalf("metadata: %+v %v", md, err)
	}
	// Later commands unlock it without any prompt.
	e := &env{app: &App{StdinIsTTY: true, Getenv: func(string) string { return "" }, ReadSecret: func(string) ([]byte, error) {
		t.Fatal("unprotected credential prompted")
		return nil, nil
	}}}
	if _, sec, _, err := e.unlockCredential("main"); err != nil || sec.RefreshToken == "" {
		t.Fatalf("unlock unprotected: %v", err)
	}
}

// A failed login after Privy approval revokes the grant and stores nothing.
func TestFailedLoginRevokesGrant(t *testing.T) {
	h := newHarness(t)
	h.addProfile()
	h.fake.approved = false
	r, _ := h.interactiveLogin([]string{"correct horse battery", "correct horse battery"}, "")
	if r.code == 0 {
		t.Fatalf("login unexpectedly succeeded: %s", r.stdout)
	}
	if h.privy.grantCalls == 0 {
		t.Fatal("grant was not revoked after failed login")
	}
	if _, err := auth.LoadPrivyMetadata("main"); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("credential stored after failed login: %v", err)
	}
}

// A wrong passphrase on unlock re-prompts instead of failing the command.
func TestUnlockRepromptsWrongPassphrase(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	answers := []string{"wrong passphrase", "correct horse battery"}
	n := 0
	var errb bytes.Buffer
	e := &env{app: &App{Stderr: &errb, StdinIsTTY: true, Getenv: func(string) string { return "" }, ReadSecret: func(string) ([]byte, error) {
		n++
		return []byte(answers[n-1]), nil
	}}}
	if _, _, _, err := e.unlockCredential("main"); err != nil || n != 2 {
		t.Fatalf("unlock: %v prompts=%d", err, n)
	}
	if !strings.Contains(errb.String(), "Wrong passphrase") {
		t.Fatalf("stderr: %s", errb.String())
	}
}
