//go:build !darwin

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/auth"
)

// A passphrase-protected credential written by an older release is refused
// with a pointer to 'oku auth unlock', which converts it once into the plain
// file. The fixture was recorded from the previous sealed format.
func TestLegacyCredentialNeedsUnlockThenWorks(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", "http://127.0.0.1:9", "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	raw, err := os.ReadFile("../auth/testdata/legacy-passphrase.json")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := auth.PrivyPath("main")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run("auth", "status", "--verify", "--json")
	if r.errCode() != "AUTH_REQUIRED" || !strings.Contains(r.stdout, "oku auth unlock") {
		t.Fatalf("legacy credential: %s", r.stdout)
	}
	passFile := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(passFile, []byte("wrong passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.extraEnv = map[string]string{"OKU_PASSPHRASE_FILE": passFile}
	if r := h.run("auth", "unlock", "--json"); r.code == 0 {
		t.Fatalf("wrong passphrase converted: %s", r.stdout)
	}
	if err := os.WriteFile(passFile, []byte("correct horse battery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := h.run("auth", "unlock", "--json"); r.code != 0 || r.data()["migrated"] != true {
		t.Fatalf("unlock: %s %s", r.stdout, r.stderr)
	}
	h.extraEnv = nil
	if _, sec, err := auth.LoadPrivy("main"); err != nil || sec.RefreshToken != "legacy-refresh-token" {
		t.Fatalf("converted credential: %v", err)
	}
	if r := h.run("auth", "status", "--json"); r.code != 0 || r.data()["credential_store"] != "file" {
		t.Fatalf("status after unlock: %s", r.stdout)
	}
	if r := h.run("auth", "unlock", "--json"); r.code != 0 || r.data()["migrated"] != false {
		t.Fatalf("second unlock: %s", r.stdout)
	}
}
