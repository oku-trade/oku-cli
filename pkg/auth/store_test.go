//go:build !darwin

package auth

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("OKU_CONFIG_DIR", t.TempDir())
}

func TestSealRoundTripAndNoPlaintext(t *testing.T) {
	setup(t)
	md := Metadata{Profile: "p", AccountAddress: "0x1111111111111111111111111111111111111111", Scopes: []string{"read"}}
	sec := Secrets{DeviceToken: "okud_supersecrettoken", AgentKey: []byte("0123456789abcdef0123456789abcdef")}
	if err := Save("p", md, sec, []byte("correct horse")); err != nil {
		t.Fatal(err)
	}
	p, _ := Path("p")
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "supersecret") || strings.Contains(string(raw), "0123456789abcdef") {
		t.Fatal("secret stored in plaintext")
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	if _, _, err := Unlock("p", []byte("wrong pass")); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	_, got, err := Unlock("p", []byte("correct horse"))
	if err != nil || got.DeviceToken != sec.DeviceToken {
		t.Fatalf("unlock: %v", err)
	}

	// Tampering with metadata (e.g. rebinding to another account) must
	// invalidate the ciphertext.
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["metadata"].(map[string]any)["account_address"] = "0x2222222222222222222222222222222222222222"
	tampered, _ := json.Marshal(doc)
	_ = os.WriteFile(p, tampered, 0o600)
	if _, _, err := Unlock("p", []byte("correct horse")); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("tampered metadata accepted: %v", err)
	}

	_ = os.Chmod(p, 0o644)
	if _, err := LoadMetadata("p"); err == nil {
		t.Fatal("world-readable credential file accepted")
	}
}

// Corrupt or hostile files must fail closed without panicking or allocating
// attacker-chosen scrypt memory.
func TestCorruptCredentialFilesFailClosed(t *testing.T) {
	setup(t)
	md := Metadata{Profile: "p"}
	if err := Save("p", md, Secrets{AgentKey: []byte("k")}, []byte("correct horse")); err != nil {
		t.Fatal(err)
	}
	p, _ := Path("p")
	orig, _ := os.ReadFile(p)
	mutate := func(f func(doc map[string]any)) {
		var doc map[string]any
		_ = json.Unmarshal(orig, &doc)
		f(doc)
		raw, _ := json.Marshal(doc)
		_ = os.WriteFile(p, raw, 0o600)
	}
	cases := map[string]func(map[string]any){
		"short nonce": func(d map[string]any) { d["nonce"] = "AAAA" },
		"huge N":      func(d map[string]any) { d["kdf"].(map[string]any)["n"] = 1 << 30 },
		"weak N":      func(d map[string]any) { d["kdf"].(map[string]any)["n"] = 1 << 10 },
		"big p":       func(d map[string]any) { d["kdf"].(map[string]any)["p"] = 1 << 20 },
	}
	for name, f := range cases {
		mutate(f)
		if _, _, err := Unlock("p", []byte("correct horse")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_ = os.WriteFile(p, make([]byte, maxCredFile+1), 0o600)
	if _, err := LoadMetadata("p"); err == nil {
		t.Error("oversized file accepted")
	}
}
