package auth

import (
	"encoding/json"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

// Legacy (version 1) credential files were sealed with XChaCha20-Poly1305
// under a scrypt key derived from a user passphrase, or under an all-zero key
// when the user skipped the passphrase ("unprotected"). This file only reads
// them so MigrateLegacy can convert them to the plain format once.

// ErrBadPassphrase means a legacy credential could not be decrypted.
var ErrBadPassphrase = errors.New("credential decryption failed (wrong passphrase or corrupted file)")

const (
	legacyVersion = 1
	legacyKDFN    = 1 << 17
	legacyKDFR    = 8
	legacyKDFP    = 1
	legacySaltLen = 16
	legacyKDFNone = "none"
)

// legacyMetadata is the frozen version 1 metadata layout. Its JSON encoding
// is the AEAD additional data, so its fields and order must never change.
type legacyMetadata struct {
	Profile          string `json:"profile"`
	Network          string `json:"network"`
	APIURL           string `json:"api_url"`
	AgentAddress     string `json:"agent_address"`
	AccountAddress   string `json:"account_address"`
	CreatedAt        string `json:"created_at"`
	Status           string `json:"status"`
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
	Unprotected      bool   `json:"unprotected,omitempty"`
}

func (m legacyMetadata) current() Metadata {
	return Metadata{Profile: m.Profile, Network: m.Network, APIURL: m.APIURL, AgentAddress: m.AgentAddress, AccountAddress: m.AccountAddress, CreatedAt: m.CreatedAt, Status: m.Status, Kind: m.Kind, PrivyUserID: m.PrivyUserID, AccountsUserID: m.AccountsUserID, WalletAddress: m.WalletAddress, SessionID: m.SessionID, PrivyAppID: m.PrivyAppID, PrivyWalletID: m.PrivyWalletID, AccessExpiresAt: m.AccessExpiresAt, SessionExpiresAt: m.SessionExpiresAt, Generation: m.Generation}
}

type legacySealed struct {
	Version  int            `json:"version"`
	Metadata legacyMetadata `json:"metadata"`
	KDF      legacyKDF      `json:"kdf"`
	Nonce    []byte         `json:"nonce"`
	Box      []byte         `json:"box"`
}

type legacyKDF struct {
	Name string `json:"name"`
	Salt []byte `json:"salt"`
	N    int    `json:"n"`
	R    int    `json:"r"`
	P    int    `json:"p"`
}

func readLegacy(k key) (*legacySealed, error) {
	raw, version, err := readFile(k)
	if err != nil {
		return nil, err
	}
	if version != legacyVersion {
		return nil, errors.New("credential is not in the legacy format")
	}
	var doc legacySealed
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// legacyUnprotected reports whether a sealed file needs no passphrase.
func (d *legacySealed) unprotected() bool {
	k := d.KDF
	return k.Name == legacyKDFNone && k.N == 0 && k.R == 0 && k.P == 0 && len(k.Salt) == 0
}

func (d *legacySealed) open(pass []byte) (*Secrets, error) {
	var aeadKey []byte
	switch {
	case d.unprotected():
		// The KDF choice is bound to the authenticated Unprotected flag, so a
		// tampered file cannot downgrade a protected credential.
		if len(pass) != 0 || !d.Metadata.Unprotected {
			return nil, ErrBadPassphrase
		}
		aeadKey = make([]byte, chacha20poly1305.KeySize)
	case d.KDF.Name == "scrypt" && d.KDF.N == legacyKDFN && d.KDF.R == legacyKDFR && d.KDF.P == legacyKDFP && len(d.KDF.Salt) == legacySaltLen:
		if d.Metadata.Unprotected {
			return nil, ErrBadPassphrase
		}
		var err error
		if aeadKey, err = scrypt.Key(pass, d.KDF.Salt, d.KDF.N, d.KDF.R, d.KDF.P, chacha20poly1305.KeySize); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unsupported legacy kdf parameters")
	}
	aead, err := chacha20poly1305.NewX(aeadKey)
	if err != nil {
		return nil, err
	}
	if len(d.Nonce) != aead.NonceSize() {
		return nil, ErrBadPassphrase
	}
	ad, err := json.Marshal(d.Metadata)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, d.Nonce, d.Box, ad)
	if err != nil {
		return nil, ErrBadPassphrase
	}
	var s Secrets
	if err := json.Unmarshal(plain, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// LegacyNeedsPassphrase reports whether a profile's legacy credential was
// sealed with a passphrase. It returns ErrNoCredentials, or an error when the
// credential is not in the legacy format.
func LegacyNeedsPassphrase(profile string) (bool, error) {
	doc, err := readLegacy(privyKey(profile))
	if err != nil {
		return false, err
	}
	return !doc.unprotected(), nil
}

// MigrateLegacy decrypts a profile's legacy file credential with pass (nil for
// an unprotected one) and rewrites it in the plain format at the same path.
// The caller holds LockProfile. A wrong passphrase returns ErrBadPassphrase
// and leaves the file untouched.
func MigrateLegacy(profile string, pass []byte) (*Metadata, error) {
	k := privyKey(profile)
	doc, err := readLegacy(k)
	if err != nil {
		return nil, err
	}
	sec, err := doc.open(pass)
	if err != nil {
		return nil, err
	}
	md := doc.Metadata.current()
	if md.Profile != profile || md.Kind != KindPrivy {
		return nil, errors.New("legacy credential profile or kind mismatch")
	}
	if err := fileSave(k, md, *sec); err != nil {
		return nil, err
	}
	got, gotSec, err := fileRead(k)
	if err != nil || *got != md || *gotSec != *sec {
		return nil, errors.New("converted credential failed read-back verification")
	}
	return got, nil
}
