//go:build darwin && cgo

package auth

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Only the CI wrapper may enable this test after creating a disposable
// default Keychain on a fresh hosted runner. The wrapper owns restoration and
// deletion, including when this test fails or leaves the Keychain locked.
func TestDedicatedNativeKeychain(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("CI") != "true" || os.Getenv("OKU_KEYCHAIN_TEST_ACK") != "disposable-runner" || os.Getenv("OKU_KEYCHAIN_LOCK_TEST") != "1" {
		t.Skip("requires hosted CI disposable default Keychain wrapper")
	}
	dedicated := os.Getenv("OKU_TEST_KEYCHAIN")
	if dedicated == "" || !filepath.IsAbs(dedicated) || !strings.Contains(filepath.Base(dedicated), "oku-ci-") {
		t.Fatal("dedicated Keychain path required")
	}
	defaultKeychain, err := exec.Command("security", "default-keychain", "-d", "user").Output()
	if err != nil || strings.Trim(strings.TrimSpace(string(defaultKeychain)), "\"") != dedicated {
		t.Fatalf("disposable Keychain is not the user default: %v", err)
	}
	t.Setenv("OKU_CONFIG_DIR", t.TempDir())
	profile := "oku-ci-native-test"
	md := Metadata{Profile: profile, Network: "testnet", APIURL: "https://api.example", AccountAddress: "0x111"}
	sec := Secrets{DeviceToken: "test-only-token", AgentKey: []byte("test-only-key")}
	if err := Save(profile, md, sec, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadMetadata(profile)
	if err != nil || loaded.Profile != profile {
		t.Fatalf("load metadata: %v", err)
	}
	_, got, err := Unlock(profile, nil)
	if err != nil || got.DeviceToken != sec.DeviceToken {
		t.Fatalf("unlock: %v", err)
	}
	if err := Save(profile, Metadata{Profile: profile, Network: "mainnet", APIURL: md.APIURL, AccountAddress: md.AccountAddress}, sec, nil); err == nil {
		t.Fatal("network rebinding accepted")
	}
	if err := Save(profile, Metadata{Profile: profile, Network: md.Network, APIURL: "https://other.example", AccountAddress: md.AccountAddress}, sec, nil); err == nil {
		t.Fatal("origin rebinding accepted")
	}
	if err := Save(profile, Metadata{Profile: profile, Network: md.Network, APIURL: md.APIURL, AccountAddress: "0x222"}, sec, nil); err == nil {
		t.Fatal("account rebinding accepted")
	}
	if _, _, err := Unlock(profile+"-other", nil); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("other profile resolved: %v", err)
	}
	_, got, err = Unlock(profile, nil)
	if err != nil || got.DeviceToken != sec.DeviceToken {
		t.Fatalf("original credential not recoverable: %v", err)
	}
	if err := exec.Command("security", "lock-keychain", dedicated).Run(); err != nil {
		t.Fatalf("lock disposable Keychain: %v", err)
	}
	if _, err := LoadMetadata(profile); err == nil || errors.Is(err, ErrNoCredentials) {
		t.Fatalf("locked Keychain did not fail closed: %v", err)
	}
	// Do not unlock with secret-bearing subprocess arguments. The CI wrapper
	// deletes its owned disposable Keychain after restoring original defaults.
}
