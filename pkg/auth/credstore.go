package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/config"
)

// Store names where a profile's credential lives.
type Store string

const (
	// StoreFile is a plain 0600 file (default on Linux and other non-macOS
	// platforms).
	StoreFile Store = "file"
	// StoreKeyring is the freedesktop Secret Service (GNOME Keyring, KWallet,
	// KeePassXC). Opt-in, Linux only.
	StoreKeyring Store = "keyring"
	// StoreKeychain is the native macOS Keychain (always used on macOS).
	StoreKeychain Store = "keychain"
)

// ErrKeyringUnsupported means the Secret Service store was requested on a
// platform that does not offer it.
var ErrKeyringUnsupported = errors.New("the keyring credential store is only supported on Linux")

// keychainService identifies oku items in the macOS Keychain and in the
// Secret Service. It must stay stable.
const keychainService = "com.oku.trade.cli.credentials.v1"

// keychainAccount scopes an item to the config dir, kind and profile so
// separate OKU_CONFIG_DIRs never share a native credential.
func keychainAccount(k key) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(digest[:]) + ":" + k.kind + ":" + k.profile, nil
}

// ParseStore validates a user-supplied store name (flag or
// OKU_CREDENTIAL_STORE). Empty means the platform default.
func ParseStore(s string) (Store, error) {
	switch Store(strings.ToLower(strings.TrimSpace(s))) {
	case "":
		return DefaultStore(), nil
	case StoreFile:
		if runtime.GOOS == "darwin" {
			return StoreKeychain, nil
		}
		return StoreFile, nil
	case StoreKeyring:
		switch runtime.GOOS {
		case "darwin":
			// macOS always uses the Keychain; the option is ignored.
			return StoreKeychain, nil
		case "linux":
			return StoreKeyring, nil
		}
		return "", ErrKeyringUnsupported
	}
	return "", fmt.Errorf("unknown credential store %q (want file or keyring)", s)
}

// DefaultStore is the store used when a profile records none.
func DefaultStore() Store {
	if runtime.GOOS == "darwin" {
		return StoreKeychain
	}
	return StoreFile
}

// StoreFor returns the store recorded for a profile in config. Profiles
// without a recorded store use the platform default, so existing configs
// keep working unchanged.
func StoreFor(profile string) (Store, error) {
	if runtime.GOOS == "darwin" {
		return StoreKeychain, nil
	}
	f, err := config.Load()
	if err != nil {
		return "", err
	}
	p := f.Profiles[profile]
	if p == nil || p.CredentialStore == "" {
		return StoreFile, nil
	}
	return ParseStore(p.CredentialStore)
}

// SavePrivy stores the Privy (Accounts session) credential in the profile's
// recorded store.
func SavePrivy(profile string, md Metadata, s Secrets) error {
	st, err := StoreFor(profile)
	if err != nil {
		return err
	}
	return SavePrivyIn(st, profile, md, s)
}

// LoadPrivyMetadata returns non-secret metadata. For native stores (Keychain,
// Secret Service) the metadata lives in the same item as the secrets.
func LoadPrivyMetadata(profile string) (*Metadata, error) {
	st, err := StoreFor(profile)
	if err != nil {
		return nil, err
	}
	return LoadPrivyMetadataIn(st, profile)
}

// LoadPrivy loads metadata and secrets from the profile's recorded store. A
// file credential from an older release returns ErrLegacyCredential.
func LoadPrivy(profile string) (*Metadata, *Secrets, error) {
	st, err := StoreFor(profile)
	if err != nil {
		return nil, nil, err
	}
	return LoadPrivyIn(st, profile)
}

// LoadPrivyWithInteraction permits one user-authorized macOS retry.
func LoadPrivyWithInteraction(profile string, interactive, gui bool) (*Metadata, *Secrets, error) {
	if !interactive || DefaultStore() != StoreKeychain {
		return LoadPrivy(profile)
	}
	return nativeLoad(interactiveKeychain(gui), privyKey(profile))
}

func LoadPrivyMetadataWithInteraction(profile string, interactive, gui bool) (*Metadata, error) {
	if DefaultStore() != StoreKeychain {
		return LoadPrivyMetadata(profile)
	}
	md, _, err := LoadPrivyWithInteraction(profile, interactive, gui)
	return md, err
}

// DeletePrivy removes the credential from the profile's recorded store.
func DeletePrivy(profile string) error {
	st, err := StoreFor(profile)
	if err != nil {
		return err
	}
	return DeletePrivyIn(st, profile)
}

// SavePrivyIn writes the credential to an explicit store (login, migrate).
func SavePrivyIn(st Store, profile string, md Metadata, s Secrets) error {
	md.Kind = KindPrivy
	if st == StoreFile {
		if !fileStoreSupported {
			return ErrUnsupportedPlatform
		}
		if md.Profile != profile {
			return errors.New("credential binding incomplete or profile mismatch")
		}
		return fileSave(privyKey(profile), md, s)
	}
	ns, err := nativeStore(st)
	if err != nil {
		return err
	}
	return nativeSave(ns, privyKey(profile), md, s)
}

// LoadPrivyMetadataIn reads non-secret metadata from an explicit store.
func LoadPrivyMetadataIn(st Store, profile string) (*Metadata, error) {
	if st == StoreFile {
		if !fileStoreSupported {
			return nil, ErrUnsupportedPlatform
		}
		return fileLoadMetadata(privyKey(profile))
	}
	md, _, err := LoadPrivyIn(st, profile)
	return md, err
}

// LoadPrivyIn reads metadata and secrets from an explicit store.
func LoadPrivyIn(st Store, profile string) (*Metadata, *Secrets, error) {
	if st == StoreFile {
		if !fileStoreSupported {
			return nil, nil, ErrUnsupportedPlatform
		}
		return fileRead(privyKey(profile))
	}
	ns, err := nativeStore(st)
	if err != nil {
		return nil, nil, err
	}
	return nativeLoad(ns, privyKey(profile))
}

// DeletePrivyIn removes the credential from an explicit store. A missing
// credential is not an error.
func DeletePrivyIn(st Store, profile string) error {
	if st == StoreFile {
		if !fileStoreSupported {
			return ErrUnsupportedPlatform
		}
		return fileDelete(privyKey(profile))
	}
	ns, err := nativeStore(st)
	if err != nil {
		return err
	}
	return ns.Remove(privyKey(profile))
}

// CheckStore verifies that a native store is reachable before anything is
// authorized remotely, so login can fail early with a clear error. The file
// store is always available.
func CheckStore(st Store) error {
	if st == StoreFile {
		if !fileStoreSupported {
			return ErrUnsupportedPlatform
		}
		return nil
	}
	ns, err := nativeStore(st)
	if err != nil {
		return err
	}
	if p, ok := ns.(interface{ probe() error }); ok {
		return p.probe()
	}
	return nil
}
