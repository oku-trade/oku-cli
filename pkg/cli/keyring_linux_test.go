//go:build linux

package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/config"
)

func keyringHarness(t *testing.T) (*harness, *auth.MemoryKeyring) {
	h := newHarness(t)
	m := &auth.MemoryKeyring{}
	t.Cleanup(auth.UseKeyringForTest(m))
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	return h, m
}

func profileStore(t *testing.T) string {
	t.Helper()
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return f.Profiles["main"].CredentialStore
}

func credFileExists(t *testing.T) bool {
	p, _ := auth.PrivyPath("main")
	_, err := os.Stat(p)
	return err == nil
}

// Opt-in keyring login: no credential file, store recorded in the profile,
// status reports it, unlock is a no-op, logout deletes the item.
func TestKeyringLoginLifecycle(t *testing.T) {
	h, m := keyringHarness(t)
	if r := h.run("auth", "login", "--no-open", "--credential-store", "keyring", "--json"); r.code != 0 {
		t.Fatalf("login: %s %s", r.stdout, r.stderr)
	}
	if m.Len() != 1 || credFileExists(t) || profileStore(t) != "keyring" {
		t.Fatalf("items=%d file=%v store=%q", m.Len(), credFileExists(t), profileStore(t))
	}
	r := h.run("auth", "status", "--verify", "--json")
	if r.code != 0 || r.data()["credential_store"] != "keyring" {
		t.Fatalf("status: %s", r.stdout)
	}
	if r := h.run("auth", "unlock", "--json"); r.code != 0 || r.data()["migrated"] != false || !strings.Contains(r.stdout, "desktop keyring") {
		t.Fatalf("unlock: %s", r.stdout)
	}
	if r := h.run("auth", "logout", "--json"); r.code != 0 || m.Len() != 0 {
		t.Fatalf("logout: %s items=%d", r.stdout, m.Len())
	}
}

// A keyring profile whose item vanished reports that instead of reading a
// file.
func TestKeyringMissingItemReported(t *testing.T) {
	h, m := keyringHarness(t)
	if r := h.run("auth", "login", "--no-open", "--credential-store", "keyring", "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	if m.Len() != 1 {
		t.Fatal("no keyring item")
	}
	if err := auth.DeletePrivyIn(auth.StoreKeyring, "main"); err != nil {
		t.Fatal(err)
	}
	r := h.run("auth", "status", "--verify", "--json")
	if r.errCode() != "AUTH_REQUIRED" || !strings.Contains(r.stdout, "credential item is missing") {
		t.Fatalf("missing: %s", r.stdout)
	}
}

// An unreachable Secret Service fails login before any device grant and
// never silently falls back to the file store (flag or env).
func TestKeyringUnavailableFailsLoginEarly(t *testing.T) {
	h, m := keyringHarness(t)
	m.Unavailable = true
	h.extraEnv = map[string]string{"OKU_CREDENTIAL_STORE": "keyring"}
	r := h.run("auth", "login", "--no-open", "--json")
	if r.code == 0 || !strings.Contains(r.stdout, "file store") {
		t.Fatalf("login: %s", r.stdout)
	}
	if h.privy.authorizations != 0 || credFileExists(t) || profileStore(t) != "" {
		t.Fatal("unavailable keyring started a grant or fell back to the file store")
	}
	if r := h.run("auth", "login", "--no-open", "--credential-store", "vault", "--json"); r.code == 0 {
		t.Fatal("unknown store accepted")
	}
}

// migrate moves the credential file -> keyring -> file, verifying each step
// and leaving exactly one copy.
func TestKeyringMigrateBothWays(t *testing.T) {
	h, m := keyringHarness(t)
	if r := h.run("auth", "login", "--no-open", "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	_, before, err := auth.LoadPrivy("main")
	if err != nil {
		t.Fatal(err)
	}
	if r := h.run("auth", "migrate", "--to", "keyring", "--json"); r.code != 0 {
		t.Fatalf("to keyring: %s %s", r.stdout, r.stderr)
	}
	if m.Len() != 1 || credFileExists(t) || profileStore(t) != "keyring" {
		t.Fatal("migration to keyring incomplete")
	}
	if _, sec, err := auth.LoadPrivy("main"); err != nil || *sec != *before {
		t.Fatalf("keyring copy: %v", err)
	}
	if r := h.run("auth", "migrate", "--to", "keyring", "--json"); r.code != 0 || r.data()["migrated"] != false {
		t.Fatalf("noop: %s", r.stdout)
	}
	if r := h.run("auth", "migrate", "--to", "file", "--json"); r.code != 0 {
		t.Fatalf("to file: %s %s", r.stdout, r.stderr)
	}
	if m.Len() != 0 || !credFileExists(t) || profileStore(t) != "" {
		t.Fatal("migration to file incomplete")
	}
	if _, sec, err := auth.LoadPrivy("main"); err != nil || *sec != *before {
		t.Fatalf("file copy: %v", err)
	}
	if r := h.run("auth", "status", "--verify", "--json"); r.code != 0 || r.data()["credential_store"] != "file" {
		t.Fatalf("status after migrate: %s", r.stdout)
	}
}
