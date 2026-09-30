package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrKeychainUnavailable distinguishes a locked, denied, or unavailable native store
// from a missing credential. It must never trigger an encrypted-file fallback.
var ErrKeychainUnavailable = errors.New("credential keychain unavailable")

var ErrUnsupportedPlatform = errors.New("native Keychain migration requires macOS with CGO")

type credentialItemStore interface {
	Get(profile string) ([]byte, error)
	Put(profile string, value []byte) error
	Remove(profile string) error
}

type credentialRecord struct {
	Version  int      `json:"version"`
	Metadata Metadata `json:"metadata"`
	Secrets  Secrets  `json:"secrets"`
}

func nativeLoad(store credentialItemStore, profile string) (*Metadata, *Secrets, error) {
	raw, err := store.Get(profile)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 || len(raw) > maxCredFile {
		return nil, nil, errors.New("invalid keychain credential length")
	}
	var record credentialRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, nil, fmt.Errorf("invalid keychain credential: %w", err)
	}
	if record.Version != 1 || record.Metadata.Profile != profile || profile == "" {
		return nil, nil, errors.New("keychain credential profile mismatch or unsupported version")
	}
	if record.Metadata.APIURL == "" || record.Metadata.Network == "" || record.Metadata.AccountAddress == "" || !strings.HasPrefix(record.Metadata.APIURL, "https://") && !strings.HasPrefix(record.Metadata.APIURL, "http://") {
		return nil, nil, errors.New("keychain credential binding incomplete")
	}
	return &record.Metadata, &record.Secrets, nil
}

func nativeSave(store credentialItemStore, profile string, md Metadata, secrets Secrets) error {
	if profile == "" || md.Profile != profile || md.APIURL == "" || md.Network == "" || md.AccountAddress == "" {
		return errors.New("credential binding incomplete or profile mismatch")
	}
	raw, err := json.Marshal(credentialRecord{Version: 1, Metadata: md, Secrets: secrets})
	if err != nil {
		return err
	}
	if len(raw) > maxCredFile {
		return errors.New("credential exceeds keychain size limit")
	}
	if err := store.Put(profile, raw); err != nil {
		return err
	}
	got, sec, err := nativeLoad(store, profile)
	if err != nil {
		return fmt.Errorf("verify keychain credential: %w", err)
	}
	if !reflect.DeepEqual(got, &md) || !reflect.DeepEqual(sec, &secrets) {
		return errors.New("keychain credential verification mismatch")
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var difference byte
	for i := range a {
		difference |= a[i] ^ b[i]
	}
	return difference == 0
}
