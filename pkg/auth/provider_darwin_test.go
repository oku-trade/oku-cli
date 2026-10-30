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
	sec := Secrets{RefreshToken: "test-only-token"}
	if err := SavePrivy(profile, md, sec); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPrivyMetadata(profile)
	if err != nil || loaded.Profile != profile {
		t.Fatalf("load metadata: %v", err)
	}
	_, got, err := LoadPrivy(profile)
	if err != nil || got.RefreshToken != sec.RefreshToken {
		t.Fatalf("unlock: %v", err)
	}
	if _, _, err := LoadPrivy(profile + "-other"); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("other profile resolved: %v", err)
	}
	if err := exec.Command("security", "lock-keychain", dedicated).Run(); err != nil {
		t.Fatalf("lock disposable Keychain: %v", err)
	}
	if _, err := LoadPrivyMetadata(profile); !errors.Is(err, ErrKeychainLocked) {
		t.Fatalf("locked Keychain should report locked without prompting: %v", err)
	}
}
