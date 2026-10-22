//go:build darwin && cgo

package auth

/*
#cgo LDFLAGS: -framework Security
#include <Security/Security.h>
*/
import "C"

import (
	"errors"
	"fmt"

	keychain "github.com/keybase/go-keychain"
)

type systemKeychain struct{}

// keybase/go-keychain does not expose kSecUseAuthenticationUIFail. The
// legacy macOS Keychain API provides process-wide interaction suppression.
// Never restore interaction implicitly: headless commands must fail closed
// instead of displaying an authorization dialog or hanging behind one.
func disallowKeychainInteraction() error {
	if status := C.SecKeychainSetUserInteractionAllowed(C.Boolean(0)); status != 0 {
		return fmt.Errorf("%w: cannot disable Keychain UI (status %d)", ErrKeychainUnavailable, int(status))
	}
	return nil
}

func keychainItem(k key) (keychain.Item, error) {
	item := keychain.NewItem()
	account, err := keychainAccount(k)
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

func (systemKeychain) Get(k key) ([]byte, error) {
	profile := k.profile
	if err := disallowKeychainInteraction(); err != nil {
		return nil, err
	}
	if profile == "" {
		return nil, errors.New("empty keychain profile")
	}
	item, err := keychainItem(k)
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
func (systemKeychain) Put(k key, value []byte) error {
	profile := k.profile
	if err := disallowKeychainInteraction(); err != nil {
		return err
	}
	if profile == "" {
		return errors.New("empty keychain profile")
	}
	item, err := keychainItem(k)
	if err != nil {
		return err
	}
	item.SetData(value)
	// The library documents kSecAttrAccessible support on macOS when
	// kSecAttrSynchronizable is specified (set to false on this item).
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	item.SetLabel("Oku Agent " + k.kind + " credential")
	err = keychain.AddItem(item)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		update := keychain.NewItem()
		update.SetData(value)
		query, queryErr := keychainItem(k)
		if queryErr != nil {
			return queryErr
		}
		err = keychain.UpdateItem(query, update)
	}
	return keychainError(err)
}
func (systemKeychain) Remove(k key) error {
	profile := k.profile
	if err := disallowKeychainInteraction(); err != nil {
		return err
	}
	if profile == "" {
		return errors.New("empty keychain profile")
	}
	item, err := keychainItem(k)
	if err != nil {
		return err
	}
	err = keychain.DeleteItem(item)
	if errors.Is(err, keychain.ErrorItemNotFound) {
		return nil
	}
	return keychainError(err)
}

func nativeStore(st Store) (credentialItemStore, error) {
	if st != StoreKeychain {
		return nil, fmt.Errorf("credential store %q is not available on macOS", st)
	}
	return systemKeychain{}, nil
}
