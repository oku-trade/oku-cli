package auth

import (
	"errors"
	"testing"
)

func TestRetireLegacyStores(t *testing.T) {
	for _, tc := range []struct {
		name         string
		file, native bool
	}{
		{"file-only", true, false}, {"keychain-only", false, true}, {"coexisting", true, true}, {"absent", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupRetirement(t)
			f := &fakeKeychain{data: map[key][]byte{}}
			md := Metadata{Profile: "p", Network: "mainnet", APIURL: "https://api.example", AccountAddress: "0x111"}
			sec := Secrets{DeviceToken: "device"}
			if tc.file {
				if err := fileSave(deviceKey("p"), md, sec, []byte("correct horse")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.native {
				if err := nativeSave(f, deviceKey("p"), md, sec); err != nil {
					t.Fatal(err)
				}
			}
			present, err := retireLegacy("p", f)
			if err != nil || present != (tc.file || tc.native) {
				t.Fatalf("retire: present=%v err=%v", present, err)
			}
			if _, err := fileLoadMetadata(deviceKey("p")); !errors.Is(err, ErrNoCredentials) {
				t.Fatalf("file remains: %v", err)
			}
			if _, _, err := nativeLoad(f, deviceKey("p")); !errors.Is(err, ErrNoCredentials) {
				t.Fatalf("keychain remains: %v", err)
			}
		})
	}
}
func TestRetireLegacyDoesNotTreatProviderFailureAsAbsence(t *testing.T) {
	setupRetirement(t)
	md := Metadata{Profile: "p", Network: "mainnet", APIURL: "https://api.example", AccountAddress: "0x111"}
	if err := fileSave(deviceKey("p"), md, Secrets{DeviceToken: "device"}, []byte("correct horse")); err != nil {
		t.Fatal(err)
	}
	f := &fakeKeychain{data: map[key][]byte{}, err: ErrKeychainUnavailable}
	if _, err := retireLegacy("p", f); !errors.Is(err, ErrKeychainUnavailable) {
		t.Fatalf("provider failure hidden: %v", err)
	}
	if _, err := fileLoadMetadata(deviceKey("p")); err != nil {
		t.Fatalf("file deleted on provider failure: %v", err)
	}
}
func setupRetirement(t *testing.T) { t.Helper(); t.Setenv("OKU_CONFIG_DIR", t.TempDir()) }
