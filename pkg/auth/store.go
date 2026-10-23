// Package auth stores the CLI Privy credential.
//
// File store (Linux and other non-macOS platforms): one JSON file per profile
// under the config dir, mode 0600. The file is not encrypted; it is protected
// by file permissions only, like ~/.ssh keys without a passphrase or the gh
// CLI's fallback token file. Users who want encryption at rest opt into the
// desktop keyring store instead.
//
// Credentials written by older releases were sealed with a passphrase
// (version 1). They are still recognized: metadata stays readable, secrets
// return ErrLegacyCredential until MigrateLegacy converts the file once.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oku-trade/oku-cli/pkg/config"
)

// ErrNoCredentials means no enrollment is stored for the profile.
var ErrNoCredentials = errors.New("no stored credentials")

// ErrLegacyCredential means the credential still uses the passphrase-sealed
// format of older releases and must be converted with MigrateLegacy.
var ErrLegacyCredential = errors.New("credential uses the old passphrase-protected format")

// Metadata is the non-secret part of a credential.
type Metadata struct {
	Profile        string `json:"profile"`
	Network        string `json:"network"`
	APIURL         string `json:"api_url"`
	AgentAddress   string `json:"agent_address"`
	AccountAddress string `json:"account_address"`
	CreatedAt      string `json:"created_at"`
	Status         string `json:"status"`

	Kind             string `json:"kind,omitempty"`
	PrivyUserID      string `json:"privy_user_id,omitempty"`
	AccountsUserID   string `json:"accounts_user_id,omitempty"`
	WalletAddress    string `json:"wallet_address,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	PrivyAppID       string `json:"privy_app_id,omitempty"`
	PrivyWalletID    string `json:"privy_wallet_id,omitempty"`
	AccessExpiresAt  string `json:"access_expires_at,omitempty"`
	SessionExpiresAt string `json:"session_expires_at,omitempty"`
	Generation       uint64 `json:"generation,omitempty"`
}

// KindPrivy marks an Accounts session credential minted from a Privy login.
const KindPrivy = "privy"

// Secrets are the tokens that grant access.
type Secrets struct {
	// SessionToken is the Accounts session token.
	SessionToken string `json:"session_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// key identifies the Privy credential of a profile.
type key struct {
	profile string
	kind    string
}

func privyKey(profile string) key { return key{profile: profile, kind: KindPrivy} }

const (
	// fileVersion is the plain credential file format.
	fileVersion = 2
	maxCredFile = 64 << 10
)

// PrivyPath returns the Privy credential file for a profile. It lives in a
// separate directory so no profile name can collide with it.
func PrivyPath(profile string) (string, error) { return keyPath(privyKey(profile)) }

func keyPath(k key) (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "credentials", k.kind, k.profile+".json"), nil
}

// fileSave writes the credential as plain JSON with mode 0600.
func fileSave(k key, md Metadata, s Secrets) error {
	raw, err := json.MarshalIndent(credentialRecord{Version: fileVersion, Metadata: md, Secrets: s}, "", "  ")
	if err != nil {
		return err
	}
	p, err := keyPath(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return config.WriteFileAtomic(p, append(raw, '\n'), 0o600)
}

// readFile returns the raw credential file after permission and size checks.
func readFile(k key) ([]byte, int, error) {
	p, err := keyPath(k)
	if err != nil {
		return nil, 0, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, ErrNoCredentials
	}
	if err != nil {
		return nil, 0, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, 0, fmt.Errorf("credential file %s is accessible by other users (mode %o); fix with chmod 600", p, fi.Mode().Perm())
	}
	if fi.Size() > maxCredFile {
		return nil, 0, fmt.Errorf("credential file %s is unexpectedly large", p)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, 0, err
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, 0, fmt.Errorf("parse credentials: %w", err)
	}
	return raw, head.Version, nil
}

func fileRead(k key) (*Metadata, *Secrets, error) {
	raw, version, err := readFile(k)
	if err != nil {
		return nil, nil, err
	}
	switch version {
	case fileVersion:
		var rec credentialRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, nil, fmt.Errorf("parse credentials: %w", err)
		}
		if rec.Metadata.Profile != k.profile || rec.Metadata.Kind != k.kind {
			return nil, nil, errors.New("credential file profile or kind mismatch")
		}
		return &rec.Metadata, &rec.Secrets, nil
	case legacyVersion:
		return nil, nil, ErrLegacyCredential
	}
	return nil, nil, fmt.Errorf("unsupported credential version %d", version)
}

// fileLoadMetadata returns metadata. A legacy sealed file still exposes its
// plaintext metadata so status and doctor can describe it.
func fileLoadMetadata(k key) (*Metadata, error) {
	md, _, err := fileRead(k)
	if errors.Is(err, ErrLegacyCredential) {
		doc, lerr := readLegacy(k)
		if lerr != nil {
			return nil, lerr
		}
		m := doc.Metadata.current()
		return &m, nil
	}
	return md, err
}

// fileDelete removes stored credentials for a profile.
func fileDelete(k key) error {
	p, err := keyPath(k)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
