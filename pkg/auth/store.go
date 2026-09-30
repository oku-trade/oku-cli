// Package auth stores the CLI device credential and delegated agent key.
//
// Storage: a single file under the config dir, mode 0600, sealed with
// XChaCha20-Poly1305 using a key derived by scrypt from a user passphrase.
// There is no plaintext fallback: without a passphrase the signer and token
// are unavailable and commands fail closed.
package auth

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"

	"github.com/oku-trade/oku-cli/pkg/config"
)

// ErrNoCredentials means no enrollment is stored for the profile.
var ErrNoCredentials = errors.New("no stored credentials")

// ErrBadPassphrase means decryption failed.
var ErrBadPassphrase = errors.New("credential decryption failed (wrong passphrase or corrupted file)")

// Metadata is non-secret and readable without unlocking.
type Metadata struct {
	Profile        string   `json:"profile"`
	Network        string   `json:"network"`
	APIURL         string   `json:"api_url"`
	DeviceID       string   `json:"device_id"`
	DeviceName     string   `json:"device_name"`
	AgentAddress   string   `json:"agent_address"`
	AccountAddress string   `json:"account_address"`
	Scopes         []string `json:"scopes"`
	CreatedAt      string   `json:"created_at"`
	// Status is "pending" during enrollment and "active" once a device
	// token has been claimed.
	Status       string `json:"status"`
	EnrollmentID string `json:"enrollment_id,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// HasScope reports whether the credential grants scope.
func (m *Metadata) HasScope(scope string) bool {
	for _, s := range m.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Secrets are sealed.
type Secrets struct {
	DeviceToken string `json:"device_token,omitempty"`
	AgentKey    []byte `json:"agent_key"`
	PollSecret  string `json:"poll_secret,omitempty"`
}

type sealed struct {
	Version  int      `json:"version"`
	Metadata Metadata `json:"metadata"`
	KDF      kdf      `json:"kdf"`
	Nonce    []byte   `json:"nonce"`
	Box      []byte   `json:"box"`
}

type kdf struct {
	Name string `json:"name"`
	Salt []byte `json:"salt"`
	N    int    `json:"n"`
	R    int    `json:"r"`
	P    int    `json:"p"`
}

// KDF parameters for credential version 1. Files with any other parameters
// are rejected so a corrupt or hostile file cannot force huge allocations.
const (
	kdfN        = 1 << 17
	kdfR        = 8
	kdfP        = 1
	kdfSaltLen  = 16
	maxCredFile = 64 << 10
)

// Path returns the credential file for a profile.
func Path(profile string) (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "credentials", profile+".json"), nil
}

// Save seals and writes credentials. Metadata binds the ciphertext via AEAD
// additional data so it cannot be swapped.
func fileSave(profile string, md Metadata, s Secrets, passphrase []byte) error {
	if len(passphrase) < 8 {
		return errors.New("passphrase must be at least 8 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	k := kdf{Name: "scrypt", Salt: salt, N: kdfN, R: kdfR, P: kdfP}
	aead, err := deriveAEAD(passphrase, k)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	plain, err := json.Marshal(s)
	if err != nil {
		return err
	}
	ad, err := json.Marshal(md)
	if err != nil {
		return err
	}
	doc := sealed{Version: 1, Metadata: md, KDF: k, Nonce: nonce, Box: aead.Seal(nil, nonce, plain, ad)}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	p, err := Path(profile)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(p, raw, 0o600)
}

func deriveAEAD(pass []byte, k kdf) (interface {
	NonceSize() int
	Seal(dst, nonce, plaintext, ad []byte) []byte
	Open(dst, nonce, ciphertext, ad []byte) ([]byte, error)
}, error) {
	if k.Name != "scrypt" || k.N != kdfN || k.R != kdfR || k.P != kdfP || len(k.Salt) != kdfSaltLen {
		return nil, errors.New("unsupported kdf parameters")
	}
	key, err := scrypt.Key(pass, k.Salt, k.N, k.R, k.P, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.NewX(key)
}

func read(profile string) (*sealed, error) {
	p, err := Path(profile)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoCredentials
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("credential file %s is accessible by other users (mode %o); fix with chmod 600", p, fi.Mode().Perm())
	}
	if fi.Size() > maxCredFile {
		return nil, fmt.Errorf("credential file %s is unexpectedly large", p)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var doc sealed
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if doc.Version != 1 {
		return nil, fmt.Errorf("unsupported credential version %d", doc.Version)
	}
	return &doc, nil
}

// LoadMetadata returns non-secret metadata without unlocking.
func fileLoadMetadata(profile string) (*Metadata, error) {
	doc, err := read(profile)
	if err != nil {
		return nil, err
	}
	return &doc.Metadata, nil
}

// Unlock decrypts secrets.
func fileUnlock(profile string, passphrase []byte) (*Metadata, *Secrets, error) {
	doc, err := read(profile)
	if err != nil {
		return nil, nil, err
	}
	aead, err := deriveAEAD(passphrase, doc.KDF)
	if err != nil {
		return nil, nil, err
	}
	if len(doc.Nonce) != aead.NonceSize() {
		return nil, nil, ErrBadPassphrase
	}
	ad, err := json.Marshal(doc.Metadata)
	if err != nil {
		return nil, nil, err
	}
	plain, err := aead.Open(nil, doc.Nonce, doc.Box, ad)
	if err != nil {
		return nil, nil, ErrBadPassphrase
	}
	var s Secrets
	if err := json.Unmarshal(plain, &s); err != nil {
		return nil, nil, err
	}
	return &doc.Metadata, &s, nil
}

// Delete removes stored credentials for a profile.
func fileDelete(profile string) error {
	p, err := Path(profile)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Has reports whether credentials exist for a profile.
func fileHas(profile string) bool {
	_, err := read(profile)
	return err == nil
}
