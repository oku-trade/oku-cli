//go:build !darwin

package auth

import (
	"errors"
	"os"
	"testing"
)

func setup(t *testing.T) { t.Helper(); t.Setenv("OKU_CONFIG_DIR", t.TempDir()) }

// The file store is plain JSON readable only by the owner, and refuses files
// other users can read.
func TestFileStoreRoundTripAndPermissions(t *testing.T) {
	setup(t)
	md := Metadata{Profile: "p", Network: "mainnet", APIURL: "https://a", SessionID: "sid", Generation: 4}
	if err := SavePrivy("p", md, Secrets{SessionToken: "s", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	p, _ := PrivyPath("p")
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	got, sec, err := LoadPrivy("p")
	if err != nil || got.Kind != KindPrivy || got.SessionID != "sid" || got.Generation != 4 || sec.RefreshToken != "r" {
		t.Fatalf("readback: %+v %+v %v", got, sec, err)
	}
	_ = os.Chmod(p, 0o644)
	if _, err := LoadPrivyMetadata("p"); err == nil {
		t.Fatal("world-readable credential file accepted")
	}
	_ = os.WriteFile(p, make([]byte, maxCredFile+1), 0o600)
	if _, err := LoadPrivyMetadata("p"); err == nil {
		t.Fatal("oversized file accepted")
	}
	if err := DeletePrivy("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivyMetadata("p"); !errors.Is(err, ErrNoCredentials) {
		t.Fatal(err)
	}
}

// installLegacy copies a credential written by the previous passphrase-sealed
// release (recorded in testdata) into place for profile "main".
func installLegacy(t *testing.T, name string) string {
	t.Helper()
	setup(t)
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := PrivyPath("main")
	if err := os.MkdirAll(p[:len(p)-len("/main.json")], 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A legacy passphrase-sealed credential keeps its metadata readable, refuses
// secrets until migrated, converts once with the right passphrase, and is a
// plain credential afterwards.
func TestLegacyPassphraseCredentialMigrates(t *testing.T) {
	path := installLegacy(t, "legacy-passphrase.json")
	if md, err := LoadPrivyMetadata("main"); err != nil || md.AgentAddress != "0x3333333333333333333333333333333333333333" || md.Generation != 3 {
		t.Fatalf("legacy metadata: %+v %v", md, err)
	}
	if _, _, err := LoadPrivy("main"); !errors.Is(err, ErrLegacyCredential) {
		t.Fatalf("legacy secrets: %v", err)
	}
	if need, err := LegacyNeedsPassphrase("main"); err != nil || !need {
		t.Fatalf("needs passphrase: %v %v", need, err)
	}
	before, _ := os.ReadFile(path)
	if _, err := MigrateLegacy("main", []byte("wrong passphrase")); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("failed migration modified the file")
	}
	md, err := MigrateLegacy("main", []byte("correct horse battery"))
	if err != nil || md.Generation != 3 {
		t.Fatalf("migrate: %v", err)
	}
	_, sec, err := LoadPrivy("main")
	if err != nil || sec.RefreshToken != "legacy-refresh-token" || sec.SessionToken != "legacy-session-token" {
		t.Fatalf("after migrate: %+v %v", sec, err)
	}
	if _, err := LegacyNeedsPassphrase("main"); err == nil {
		t.Fatal("migrated credential still reported as legacy")
	}
}

// A legacy credential stored without a passphrase migrates with no input, and
// tampering with the sealed metadata or KDF is rejected rather than converted.
func TestLegacyUnprotectedAndTampered(t *testing.T) {
	installLegacy(t, "legacy-unprotected.json")
	if need, err := LegacyNeedsPassphrase("main"); err != nil || need {
		t.Fatalf("needs passphrase: %v %v", need, err)
	}
	if _, err := MigrateLegacy("main", nil); err != nil {
		t.Fatal(err)
	}
	path := installLegacy(t, "legacy-passphrase.json")
	raw, _ := os.ReadFile(path)
	tampered := []byte(string(raw))
	for i := range tampered {
		if string(tampered[i:i+7]) == "0x11111" {
			copy(tampered[i:], "0x22222")
			break
		}
	}
	_ = os.WriteFile(path, tampered, 0o600)
	if _, err := MigrateLegacy("main", []byte("correct horse battery")); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("tampered metadata migrated: %v", err)
	}
}
