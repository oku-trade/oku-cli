//go:build linux

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/config"
)

func keyringProfile(t *testing.T, store string) {
	t.Helper()
	t.Setenv("OKU_CONFIG_DIR", t.TempDir())
	f := &config.File{Version: 1, Profiles: map[string]*config.Profile{"p": {Network: "mainnet", APIURL: "https://api.example", CredentialStore: store}}}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
}

// A keyring profile stores, loads and deletes through the Secret Service
// boundary only: no credential file, no passphrase.
func TestKeyringStoreRoundTrip(t *testing.T) {
	keyringProfile(t, "keyring")
	m := &MemoryKeyring{}
	defer UseKeyringForTest(m)()
	if st, err := StoreFor("p"); err != nil || st != StoreKeyring {
		t.Fatalf("store recorded in profile: %v %v", st, err)
	}
	md := Metadata{Profile: "p", Network: "mainnet", APIURL: "https://api.example", AccountAddress: "0x111"}
	if err := SavePrivy("p", md, Secrets{RefreshToken: "r"}, nil); err != nil {
		t.Fatal(err)
	}
	if path, _ := PrivyPath("p"); fileExists(path) {
		t.Fatal("keyring profile wrote a credential file")
	}
	got, sec, err := UnlockPrivy("p", nil)
	if err != nil || got.Kind != KindPrivy || sec.RefreshToken != "r" {
		t.Fatalf("readback: %v", err)
	}
	if err := DeletePrivy("p"); err != nil || m.Len() != 0 {
		t.Fatalf("delete: %v %d", err, m.Len())
	}
	if _, err := LoadPrivyMetadata("p"); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("missing item: %v", err)
	}
}

// An unreachable Secret Service is never treated as missing and never falls
// back to the file store, even when a credential file exists.
func TestKeyringUnavailableNoFallback(t *testing.T) {
	keyringProfile(t, "")
	md := Metadata{Profile: "p", Network: "mainnet", APIURL: "https://api.example", AccountAddress: "0x111", Unprotected: true}
	if err := SavePrivy("p", md, Secrets{RefreshToken: "file"}, nil); err != nil {
		t.Fatal(err)
	}
	keyringProfile(t, "keyring")
	m := &MemoryKeyring{Unavailable: true}
	defer UseKeyringForTest(m)()
	if _, _, err := UnlockPrivy("p", nil); !errors.Is(err, ErrKeychainUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
	if err := CheckStore(StoreKeyring); !errors.Is(err, ErrKeychainUnavailable) {
		t.Fatalf("probe: %v", err)
	}
	if err := SavePrivyIn(StoreKeyring, "p", md, Secrets{}, nil); !errors.Is(err, ErrKeychainUnavailable) {
		t.Fatalf("save: %v", err)
	}
}

func TestParseStore(t *testing.T) {
	for in, want := range map[string]Store{"": StoreFile, "file": StoreFile, "KEYRING": StoreKeyring} {
		if got, err := ParseStore(in); err != nil || got != want {
			t.Fatalf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseStore("vault"); err == nil {
		t.Fatal("unknown store accepted")
	}
}

// Secret Service item attributes are scoped to the config dir like the
// darwin keychain account, so throwaway config dirs never collide.
func TestKeyringAttributesScopedToConfigDir(t *testing.T) {
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "a"))
	a, _ := ssAttributes(privyKey("p"))
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "b"))
	b, _ := ssAttributes(privyKey("p"))
	if a["account"] == b["account"] || a["service"] != keychainService || a["profile"] != "p" || a["kind"] != KindPrivy {
		t.Fatalf("attributes: %v %v", a, b)
	}
}

// TestRealSecretService does one isolated round trip against the session's
// Secret Service. Opt-in only: it writes and deletes a throwaway item.
func TestRealSecretService(t *testing.T) {
	if os.Getenv("OKU_SECRET_SERVICE_TEST") != "1" {
		t.Skip("set OKU_SECRET_SERVICE_TEST=1 to touch the real desktop keyring")
	}
	t.Setenv("OKU_CONFIG_DIR", t.TempDir())
	k := privyKey("oku-test-" + filepath.Base(t.TempDir()))
	ss := secretService{}
	t.Cleanup(func() { _ = ss.Remove(k) })
	md := Metadata{Profile: k.profile, Kind: KindPrivy, Network: "mainnet", APIURL: "https://api.example", AccountAddress: "0x111"}
	if err := nativeSave(ss, k, md, Secrets{RefreshToken: "throwaway"}); err != nil {
		t.Fatal(err)
	}
	if _, sec, err := nativeLoad(ss, k); err != nil || sec.RefreshToken != "throwaway" {
		t.Fatalf("readback: %v", err)
	}
	if err := nativeSave(ss, k, md, Secrets{RefreshToken: "updated"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := ss.Remove(k); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.Get(k); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("after delete: %v", err)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
