//go:build !darwin

package auth

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func setup(t *testing.T) { t.Helper(); t.Setenv("OKU_CONFIG_DIR", t.TempDir()) }

func TestSealRoundTripAndNoPlaintext(t *testing.T) {
	setup(t)
	md := Metadata{Profile: "p", Kind: KindPrivy, AccountAddress: "0x1111111111111111111111111111111111111111"}
	sec := Secrets{RefreshToken: "supersecret-refresh"}
	pass := []byte("correct horse")
	if err := SavePrivy("p", md, sec, pass); err != nil {
		t.Fatal(err)
	}
	p, _ := PrivyPath("p")
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), sec.RefreshToken) {
		t.Fatal("secret stored in plaintext")
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	if _, _, err := UnlockPrivy("p", []byte("wrong pass")); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	_, got, err := UnlockPrivy("p", pass)
	if err != nil || got.RefreshToken != sec.RefreshToken {
		t.Fatalf("unlock: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["metadata"].(map[string]any)["account_address"] = "0x2222222222222222222222222222222222222222"
	tampered, _ := json.Marshal(doc)
	_ = os.WriteFile(p, tampered, 0o600)
	if _, _, err := UnlockPrivy("p", pass); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("tampered metadata accepted: %v", err)
	}
	_ = os.Chmod(p, 0o644)
	if _, err := LoadPrivyMetadata("p"); err == nil {
		t.Fatal("world-readable credential file accepted")
	}
}

func TestCorruptCredentialFilesFailClosed(t *testing.T) {
	setup(t)
	if err := SavePrivy("p", Metadata{Profile: "p"}, Secrets{RefreshToken: "secret"}, []byte("correct horse")); err != nil {
		t.Fatal(err)
	}
	p, _ := PrivyPath("p")
	orig, _ := os.ReadFile(p)
	cases := map[string]func(map[string]any){
		"short nonce": func(d map[string]any) { d["nonce"] = "AAAA" },
		"huge N":      func(d map[string]any) { d["kdf"].(map[string]any)["n"] = 1 << 30 },
		"weak N":      func(d map[string]any) { d["kdf"].(map[string]any)["n"] = 1 << 10 },
		"big p":       func(d map[string]any) { d["kdf"].(map[string]any)["p"] = 1 << 20 },
	}
	for name, mutate := range cases {
		var doc map[string]any
		_ = json.Unmarshal(orig, &doc)
		mutate(doc)
		raw, _ := json.Marshal(doc)
		_ = os.WriteFile(p, raw, 0o600)
		if _, _, err := UnlockPrivy("p", []byte("correct horse")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_ = os.WriteFile(p, make([]byte, maxCredFile+1), 0o600)
	if _, err := LoadPrivyMetadata("p"); err == nil {
		t.Error("oversized file accepted")
	}
}

func TestPrivyRecordRoundTripAndDelete(t *testing.T) {
	setup(t)
	pass := []byte("correct horse")
	md := Metadata{Profile: "p", Network: "mainnet", APIURL: "https://a", WalletAddress: "0x2222222222222222222222222222222222222222", SessionID: "sid"}
	if err := SavePrivy("p", md, Secrets{SessionToken: "sesstok-secret"}, pass); err != nil {
		t.Fatal(err)
	}
	stored, err := LoadPrivyMetadata("p")
	if err != nil || stored.Kind != KindPrivy {
		t.Fatalf("metadata: %v", err)
	}
	loaded, sec, err := UnlockPrivy("p", pass)
	if err != nil || sec.SessionToken != "sesstok-secret" || loaded.SessionID != "sid" {
		t.Fatal(err)
	}
	if err := DeletePrivy("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivyMetadata("p"); !errors.Is(err, ErrNoCredentials) {
		t.Fatal(err)
	}
}
