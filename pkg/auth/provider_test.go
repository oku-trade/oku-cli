package auth

import (
	"errors"
	"testing"
)

type fakeKeychain struct {
	data    map[string][]byte
	err     error
	corrupt bool
}

func (f *fakeKeychain) Get(profile string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.data[profile]
	if !ok {
		return nil, ErrNoCredentials
	}
	if f.corrupt {
		return []byte(`{"version":1,"metadata":{"profile":"other"}}`), nil
	}
	return data, nil
}
func (f *fakeKeychain) Put(profile string, data []byte) error {
	if f.err != nil {
		return f.err
	}
	f.data[profile] = append([]byte(nil), data...)
	return nil
}
func (f *fakeKeychain) Remove(profile string) error { delete(f.data, profile); return nil }

// Primary contract: a stored native credential must survive readback with its
// complete binding, while denied and corrupt reads never become missing items.
func TestNativeProviderBoundary(t *testing.T) {
	md := Metadata{Profile: "p", Network: "mainnet", APIURL: "https://api.example", AccountAddress: "0x111", Scopes: []string{"read"}}
	sec := Secrets{DeviceToken: "secret", AgentKey: []byte("key")}
	f := &fakeKeychain{data: map[string][]byte{}}
	if err := nativeSave(f, "p", md, sec); err != nil {
		t.Fatal(err)
	}
	got, secret, err := nativeLoad(f, "p")
	if err != nil || got.APIURL != md.APIURL || got.Network != md.Network || got.AccountAddress != md.AccountAddress || secret.DeviceToken != sec.DeviceToken {
		t.Fatalf("native readback: %v", err)
	}
	if _, _, err := nativeLoad(f, "other"); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("missing: %v", err)
	}
	f.err = ErrKeychainUnavailable
	if _, _, err := nativeLoad(f, "p"); !errors.Is(err, ErrKeychainUnavailable) {
		t.Fatalf("denied became missing: %v", err)
	}
	f.err = nil
	f.corrupt = true
	if _, _, err := nativeLoad(f, "p"); err == nil || errors.Is(err, ErrNoCredentials) {
		t.Fatalf("mismatched profile accepted: %v", err)
	}
	if err := nativeSave(f, "p", md, sec); err == nil {
		t.Fatal("unverified write accepted")
	}
}
