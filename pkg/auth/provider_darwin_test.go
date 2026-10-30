//go:build darwin && cgo

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/output"
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
	if err := (&config.File{Version: 1, DefaultProfile: profile, Profiles: map[string]*config.Profile{
		profile: {Network: config.NetworkMainnet, APIURL: config.DefaultAPIURL, Account: "0x1111111111111111111111111111111111111111"},
	}}).Save(); err != nil {
		t.Fatalf("save disposable profile: %v", err)
	}
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer buildCancel()
	binary := filepath.Join(t.TempDir(), "oku")
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "./cmd/oku")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build test CLI: %v: %s", err, out)
	}
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
	// This separate binary is not in the item's default trusted-app ACL.
	// JSON must report denial without ever showing a Keychain dialog.
	checkCLIStatus := func(want output.Code) {
		t.Helper()
		callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer callCancel()
		call := exec.CommandContext(callCtx, binary, "auth", "status", "--profile", profile, "--json")
		call.Stdin = strings.NewReader("")
		stdout, err := call.Output()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			t.Fatalf("%s JSON status exit: %v", want, err)
		}
		var envelope output.Envelope
		if err := json.Unmarshal(stdout, &envelope); err != nil || envelope.Error == nil || envelope.Error.Code != want {
			t.Fatalf("JSON status should report %s: %v: %s", want, err, stdout)
		}
	}
	checkCLIStatus(output.CodeKeychainAccessDenied)
	if err := exec.Command("security", "lock-keychain", dedicated).Run(); err != nil {
		t.Fatalf("lock disposable Keychain: %v", err)
	}
	if _, err := LoadPrivyMetadata(profile); !errors.Is(err, ErrKeychainLocked) {
		t.Fatalf("locked Keychain should report locked without prompting: %v", err)
	}
	checkCLIStatus(output.CodeKeychainLocked)
}
