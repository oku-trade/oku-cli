package cli

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/auth"
)

// swapDuringPrompt rewrites the credential's plaintext metadata before
// unlock and restores the authentic file at the passphrase prompt, so the
// pre-unlock snapshot and the authenticated metadata disagree.
func swapDuringPrompt(t *testing.T, mutate func(md map[string]any)) (*resolved, *App) {
	t.Helper()
	p, err := (&env{app: NewApp("t")}).profile()
	if err != nil {
		t.Fatal(err)
	}
	path, err := auth.PrivyPath(p.Name)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(original, &doc); err != nil {
		t.Fatal(err)
	}
	mutate(doc["metadata"].(map[string]any))
	changed, _ := json.Marshal(doc)
	if err = os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp("t")
	app.StdinIsTTY = true
	app.Getenv = func(string) string { return "" }
	app.ReadSecret = func(string) ([]byte, error) {
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		return []byte("correct horse battery"), nil
	}
	return p, app
}

// Regression for M-07: binding is checked against authenticated metadata,
// so a token can never be paired with an unauthenticated origin.
func TestUnlockUsesAuthenticatedBinding(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	const evil = "https://different-origin.example"
	p, app := swapDuringPrompt(t, func(md map[string]any) { md["api_url"] = evil })
	p.APIURL = evil
	if s, err := (&env{app: app, ctx: context.Background(), g: globals{timeout: time.Second}}).unlock(p, "trade"); err == nil {
		t.Fatalf("unlocked for unauthenticated origin %s (session origin %s)", evil, s.md.APIURL)
	}
	// Control: the authentic profile still unlocks and the session carries
	// authenticated metadata.
	p2, app2 := swapDuringPrompt(t, func(map[string]any) {})
	s, err := (&env{app: app2, ctx: context.Background(), g: globals{timeout: time.Second}}).unlock(p2, "trade")
	if err != nil || s.md.APIURL != h.srv.URL {
		t.Fatalf("authentic unlock: %v", err)
	}
}

// One command invocation prompts for the passphrase at most once, even though
// unlock and the signing boundary both re-read the credential under the lock.
func TestPassphrasePromptedOncePerInvocation(t *testing.T) {
	prompts := 0
	app := NewApp("t")
	app.StdinIsTTY = true
	app.Getenv = func(string) string { return "" }
	app.ReadSecret = func(string) ([]byte, error) { prompts++; return []byte("correct horse battery"), nil }
	e := &env{app: app}
	for range 3 {
		if p, err := e.passphrase(false); err != nil || string(p) != "correct horse battery" {
			t.Fatalf("passphrase: %q %v", p, err)
		}
	}
	if prompts != 1 {
		t.Fatalf("prompted %d times, want 1", prompts)
	}
	// A new invocation (new env) prompts again; confirmation always prompts.
	if _, err := (&env{app: app}).passphrase(true); err != nil || prompts != 3 {
		t.Fatalf("confirm prompts = %d, err = %v", prompts, err)
	}
}
