package cli

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/auth"
)

// memCache stands in for the kernel keyring so tests never touch it.
type memCache struct {
	mu  sync.Mutex
	m   map[string][]byte
	ttl map[string]time.Duration
}

func newMemCache() *memCache {
	return &memCache{m: map[string][]byte{}, ttl: map[string]time.Duration{}}
}
func (c *memCache) Get(p string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.m[p])
}
func (c *memCache) Put(p string, pass []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[p], c.ttl[p] = bytes.Clone(pass), ttl
	return nil
}
func (c *memCache) Forget(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, p)
}
func (c *memCache) len() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.m) }

// agentApp is a non-interactive caller (a coding agent or MCP): no TTY and
// no OKU_PASSPHRASE_FILE.
func (h *harness) agentApp(cache unlockCache, out *bytes.Buffer) *App {
	return &App{
		Stdout: out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader(""),
		ReadSecret: func(string) ([]byte, error) { h.t.Fatal("agent was prompted"); return nil, nil },
		Getenv: func(k string) string {
			if k == "OKU_PRIVY_AUTH_URL" {
				return h.privy.server.URL
			}
			return ""
		},
		Now: time.Now, Sleep: func(context.Context, time.Duration) error { return nil },
		Version: "test", UnlockCache: cache,
	}
}

// A user unlocks in their terminal; a later non-interactive agent can then
// use the credential without OKU_PASSPHRASE_FILE. Lock removes access.
func TestUnlockCacheLetsAgentsUseCredential(t *testing.T) {
	skipPassphraseOnDarwin(t)
	h := newHarness(t)
	h.enrollProtected()
	cache := newMemCache()

	var out bytes.Buffer
	agent := h.agentApp(cache, &out)
	if code := agent.Run([]string{"auth", "status", "--verify", "--json"}); code == 0 || !strings.Contains(out.String(), "oku auth unlock") {
		t.Fatalf("locked agent should point at auth unlock: %s", out.String())
	}

	var uout, uerr bytes.Buffer
	user := &App{
		Stdout: &uout, Stderr: &uerr, Stdin: strings.NewReader(""), StdinIsTTY: true,
		ReadSecret: func(string) ([]byte, error) { return []byte("correct horse battery"), nil },
		Getenv:     func(string) string { return "" },
		Now:        time.Now, Version: "test", UnlockCache: cache,
	}
	if code := user.Run([]string{"auth", "unlock", "--for", "1h", "--json"}); code != 0 {
		t.Fatalf("unlock: %s %s", uout.String(), uerr.String())
	}
	path, _ := auth.PrivyPath("main")
	if cache.ttl[path] != time.Hour {
		t.Fatalf("ttl = %s", cache.ttl[path])
	}

	out.Reset()
	if code := agent.Run([]string{"auth", "status", "--verify", "--json"}); code != 0 {
		t.Fatalf("agent after unlock: %s", out.String())
	}

	uout.Reset()
	if code := user.Run([]string{"auth", "lock", "--json"}); code != 0 || cache.len() != 0 {
		t.Fatalf("lock: %s", uout.String())
	}
	out.Reset()
	if code := agent.Run([]string{"auth", "status", "--verify", "--json"}); code == 0 {
		t.Fatalf("agent after lock should fail: %s", out.String())
	}
}

// A wrong unlock is never cached, and a stale cached passphrase is dropped.
func TestUnlockCacheRejectsWrongPassphrase(t *testing.T) {
	skipPassphraseOnDarwin(t)
	h := newHarness(t)
	h.enrollProtected()
	cache := newMemCache()
	path, _ := auth.PrivyPath("main")
	_ = cache.Put(path, []byte("stale passphrase"), time.Hour)
	var out bytes.Buffer
	if code := h.agentApp(cache, &out).Run([]string{"auth", "status", "--verify", "--json"}); code == 0 {
		t.Fatalf("stale cache unlocked: %s", out.String())
	}
	if cache.len() != 0 {
		t.Fatal("stale cached passphrase was kept")
	}
	e := &env{app: &App{Stderr: &bytes.Buffer{}, StdinIsTTY: true, Getenv: func(string) string { return "" }, UnlockCache: cache,
		ReadSecret: func(string) ([]byte, error) { return []byte("wrong passphrase"), nil }}}
	if _, _, _, err := e.unlockCredential("main"); err == nil || cache.len() != 0 {
		t.Fatalf("wrong passphrase unlocked or cached: %v", err)
	}
}

// Login caches the new passphrase; logout forgets it.
func TestLoginCachesAndLogoutForgets(t *testing.T) {
	skipPassphraseOnDarwin(t)
	h := newHarness(t)
	h.addProfile()
	cache := newMemCache()
	var out, errb bytes.Buffer
	n := 0
	a := &App{
		Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(""), StdinIsTTY: true,
		ReadSecret: func(string) ([]byte, error) { n++; return []byte("correct horse battery"), nil },
		Getenv: func(k string) string {
			switch k {
			case "OKU_PRIVY_AUTH_URL":
				return h.privy.server.URL
			case "OKU_HYPERLIQUID_INFO_URL":
				return h.srv.URL + "/info"
			}
			return ""
		},
		Now: time.Now, Sleep: func(context.Context, time.Duration) error { return nil }, Version: "test", UnlockCache: cache,
	}
	if code := a.Run([]string{"auth", "login", "--no-open", "--json"}); code != 0 || cache.len() != 1 {
		t.Fatalf("login: %s %s", out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "Unlocked for") {
		t.Fatalf("stderr: %s", errb.String())
	}
	out.Reset()
	before := n
	if code := a.Run([]string{"auth", "logout", "--json"}); code != 0 || cache.len() != 0 || n != before {
		t.Fatalf("logout: code=%d cache=%d prompts=%d %s", code, cache.len(), n-before, out.String())
	}
}
