//go:build darwin && cgo

package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	keychain "github.com/keybase/go-keychain"
	"github.com/oku-trade/oku-cli/pkg/config"
)

const keychainService = "com.oku.trade.cli.credentials.v1"

type systemKeychain struct{}

func keychainAccount(profile string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(digest[:]) + ":" + profile, nil
}
func keychainItem(profile string) (keychain.Item, error) {
	item := keychain.NewItem()
	account, err := keychainAccount(profile)
	if err != nil {
		return item, err
	}
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService(keychainService)
	item.SetAccount(account)
	item.SetSynchronizable(keychain.SynchronizableNo)
	return item, nil
}

func keychainError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, keychain.ErrorItemNotFound):
		return ErrNoCredentials
	case errors.Is(err, keychain.ErrorInteractionNotAllowed), errors.Is(err, keychain.ErrorAuthFailed), errors.Is(err, keychain.ErrorUserCanceled), errors.Is(err, keychain.ErrorNoAccessForItem), errors.Is(err, keychain.ErrorNotAvailable), errors.Is(err, keychain.ErrorNoSuchKeychain):
		return fmt.Errorf("%w: %v", ErrKeychainUnavailable, err)
	default:
		return fmt.Errorf("keychain operation failed: %w", err)
	}
}

func (systemKeychain) Get(profile string) ([]byte, error) {
	if profile == "" {
		return nil, errors.New("empty keychain profile")
	}
	item, err := keychainItem(profile)
	if err != nil {
		return nil, err
	}
	item.SetMatchLimit(keychain.MatchLimitOne)
	item.SetReturnData(true)
	result, err := keychain.QueryItem(item)
	if err != nil {
		return nil, keychainError(err)
	}
	if len(result) == 0 {
		return nil, ErrNoCredentials
	}
	if len(result) != 1 {
		return nil, errors.New("ambiguous keychain credential")
	}
	return result[0].Data, nil
}
func (systemKeychain) Put(profile string, value []byte) error {
	if profile == "" {
		return errors.New("empty keychain profile")
	}
	item, err := keychainItem(profile)
	if err != nil {
		return err
	}
	item.SetData(value)
	item.SetLabel("Oku CLI device credential")
	err = keychain.AddItem(item)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		update := keychain.NewItem()
		update.SetData(value)
		query, queryErr := keychainItem(profile)
		if queryErr != nil {
			return queryErr
		}
		err = keychain.UpdateItem(query, update)
	}
	return keychainError(err)
}
func (systemKeychain) Remove(profile string) error {
	if profile == "" {
		return errors.New("empty keychain profile")
	}
	item, err := keychainItem(profile)
	if err != nil {
		return err
	}
	err = keychain.DeleteItem(item)
	if errors.Is(err, keychain.ErrorItemNotFound) {
		return nil
	}
	return keychainError(err)
}

func Save(profile string, md Metadata, s Secrets, _ []byte) error {
	// An existing encrypted file is never silently replaced or ignored. Explicit
	// migration verifies the native item before retiring the legacy file.
	if _, err := fileLoadMetadata(profile); err == nil {
		return errors.New("legacy encrypted credential exists; migrate explicitly before writing Keychain")
	} else if !errors.Is(err, ErrNoCredentials) {
		return err
	}
	if prior, _, err := nativeLoad(systemKeychain{}, profile); err == nil {
		if prior.Network != md.Network || prior.APIURL != md.APIURL || !strings.EqualFold(prior.AccountAddress, md.AccountAddress) {
			return errors.New("existing Keychain credential has a different origin/account/network binding")
		}
	} else if !errors.Is(err, ErrNoCredentials) {
		return err
	}
	return nativeSave(systemKeychain{}, profile, md, s)
}
func LoadMetadata(profile string) (*Metadata, error) {
	md, _, err := nativeLoad(systemKeychain{}, profile)
	if errors.Is(err, ErrNoCredentials) {
		if _, legacyErr := fileLoadMetadata(profile); legacyErr == nil {
			return nil, errors.New("legacy encrypted credential exists; explicit Keychain migration required")
		} else if !errors.Is(legacyErr, ErrNoCredentials) {
			return nil, legacyErr
		}
	}
	return md, err
}
func Unlock(profile string, _ []byte) (*Metadata, *Secrets, error) {
	return nativeLoad(systemKeychain{}, profile)
}
func Delete(profile string) error {
	// Prevent accidentally leaving a usable legacy credential behind.
	if _, err := fileLoadMetadata(profile); err == nil {
		return errors.New("legacy encrypted credential exists; retire it explicitly before deleting Keychain item")
	} else if !errors.Is(err, ErrNoCredentials) {
		return err
	}
	return (systemKeychain{}).Remove(profile)
}
func Has(profile string) bool { _, err := LoadMetadata(profile); return err == nil }

// MigrateLegacy explicitly decrypts a legacy file, writes and independently
// reads back the native item, then renames the encrypted source to a restrictive
// non-active backup. On any failure before rename, the original remains active.
// No migration occurs during normal reads or enrollment.
func MigrateLegacy(profile string, passphrase []byte) error {
	md, sec, err := fileUnlock(profile, passphrase)
	if err != nil {
		return err
	}
	if md.Profile != profile || md.APIURL == "" || md.Network == "" || md.AccountAddress == "" || !strings.HasPrefix(md.APIURL, "https://") && !strings.HasPrefix(md.APIURL, "http://") {
		return errors.New("legacy credential binding incomplete")
	}
	path, err := Path(profile)
	if err != nil {
		return err
	}
	backup := path + ".pre-keychain"
	if _, err := os.Lstat(backup); err == nil {
		return errors.New("legacy backup already exists; resolve manually")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if current, currentSecrets, err := nativeLoad(systemKeychain{}, profile); err == nil {
		if !reflect.DeepEqual(current, md) || currentSecrets.DeviceToken != sec.DeviceToken || currentSecrets.PollSecret != sec.PollSecret || !equalBytes(currentSecrets.AgentKey, sec.AgentKey) {
			return errors.New("existing Keychain item differs from legacy credential")
		}
	} else if errors.Is(err, ErrNoCredentials) {
		if err := nativeSave(systemKeychain{}, profile, *md, *sec); err != nil {
			return err
		}
	} else {
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("Keychain item verified, but legacy credential not retired: %w", err)
	}
	if err := os.Chmod(backup, 0o600); err != nil {
		return fmt.Errorf("legacy backup permissions: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync credential migration directory: %w", err)
	}
	return nil
}
