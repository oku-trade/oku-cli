//go:build darwin && cgo

package auth

import (
	"os"
	"strings"
	"testing"
)

// Run only in an isolated macOS user/session with a disposable default
// Keychain. The library targets the default Keychain, not a named keychain.
// Never enable this on an operator's ordinary login session.
func TestDedicatedNativeKeychain(t *testing.T) {
	if os.Getenv("OKU_TEST_DISPOSABLE_DEFAULT_KEYCHAIN") != "I_ACKNOWLEDGE_ISOLATED_KEYCHAIN" {
		t.Skip("requires disposable macOS user and default Keychain")
	}
	t.Setenv("OKU_CONFIG_DIR", t.TempDir())
	profile := "oku-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	md := Metadata{Profile: profile, Network: "testnet", APIURL: "https://api.example", AccountAddress: "0x111"}
	sec := Secrets{DeviceToken: "test-only-token", AgentKey: []byte("test-only-key")}
	if err := Save(profile, md, sec, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Delete(profile); err != nil {
			t.Errorf("delete disposable item: %v", err)
		}
	})
	loaded, err := LoadMetadata(profile)
	if err != nil || loaded.Profile != profile {
		t.Fatalf("load metadata: %v", err)
	}
	_, got, err := Unlock(profile, nil)
	if err != nil || got.DeviceToken != sec.DeviceToken {
		t.Fatalf("unlock: %v", err)
	}
}
