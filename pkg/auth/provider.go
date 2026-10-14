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
	Get(k key) ([]byte, error)
	Put(k key, value []byte) error
	Remove(k key) error
}

type credentialRecord struct {
	Version  int      `json:"version"`
	Metadata Metadata `json:"metadata"`
	Secrets  Secrets  `json:"secrets"`
}

func nativeLoad(store credentialItemStore, k key) (*Metadata, *Secrets, error) {
	profile := k.profile
	raw, err := store.Get(k)
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
	if record.Metadata.Kind != k.kind {
		return nil, nil, errors.New("keychain credential kind mismatch")
	}
	if record.Metadata.APIURL == "" || record.Metadata.Network == "" || (k.kind == "" && record.Metadata.AccountAddress == "") || !strings.HasPrefix(record.Metadata.APIURL, "https://") && !strings.HasPrefix(record.Metadata.APIURL, "http://") {
		return nil, nil, errors.New("keychain credential binding incomplete")
	}
	return &record.Metadata, &record.Secrets, nil
}

func nativeSave(store credentialItemStore, k key, md Metadata, secrets Secrets) error {
	profile := k.profile
	if profile == "" || md.Profile != profile || md.Kind != k.kind || md.APIURL == "" || md.Network == "" {
		return errors.New("credential binding incomplete or profile mismatch")
	}
	raw, err := json.Marshal(credentialRecord{Version: 1, Metadata: md, Secrets: secrets})
	if err != nil {
		return err
	}
	if len(raw) > maxCredFile {
		return errors.New("credential exceeds keychain size limit")
	}
	if err := store.Put(k, raw); err != nil {
		return err
	}
	got, sec, err := nativeLoad(store, k)
	if err != nil {
		return fmt.Errorf("verify keychain credential: %w", err)
	}
	if !reflect.DeepEqual(got, &md) || !reflect.DeepEqual(sec, &secrets) {
		return errors.New("keychain credential verification mismatch")
	}
	return nil
}
