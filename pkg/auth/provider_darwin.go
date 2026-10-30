//go:build darwin && cgo

package auth

/*
#cgo LDFLAGS: -framework Security -framework ApplicationServices
#include <Security/Security.h>
#include <ApplicationServices/ApplicationServices.h>
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	keychain "github.com/keybase/go-keychain"
)

type systemKeychain struct{ interactive, gui bool }

var keychainInteractionMu sync.Mutex

func interactiveKeychain(gui bool) credentialItemStore {
	return systemKeychain{interactive: true, gui: gui}
}

func graphicalKeychainSession() bool {
	session := C.CGSessionCopyCurrentDictionary()
	if session == nil {
		return false
	}
	C.CFRelease(C.CFTypeRef(session))
	return true
}

// keychainLocked examines the default keychain, without requesting any item
// data or displaying UI. Status failures retain their original OSStatus.
func keychainLocked() (bool, error) {
	var kc C.SecKeychainRef
	if status := C.SecKeychainCopyDefault(&kc); status != 0 {
		return false, fmt.Errorf("default Keychain status %d", int(status))
	}
	defer C.CFRelease(C.CFTypeRef(kc))
	var state C.SecKeychainStatus
	if status := C.SecKeychainGetStatus(kc, &state); status != 0 {
		return false, fmt.Errorf("Keychain lock status %d", int(status))
	}
	return state&C.kSecUnlockStateStatus == 0, nil
}

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
	var status keychain.Error
	if errors.As(err, &status) {
		locked := false
		if status == keychain.ErrorInteractionNotAllowed || status == keychain.ErrorAuthFailed || status == keychain.ErrorNoAccessForItem {
			var statusErr error
			locked, statusErr = keychainLocked()
			if statusErr != nil {
				return fmt.Errorf("%w: %v; %v", ErrKeychainUnavailable, err, statusErr)
			}
		}
		if kind := KeychainStatus(int(status), locked); kind != nil {
			return fmt.Errorf("%w: %v", kind, err)
		}
	}
	return fmt.Errorf("keychain operation failed: %w", err)
}

func (s systemKeychain) Get(k key) ([]byte, error) {
	keychainInteractionMu.Lock()
	defer keychainInteractionMu.Unlock()
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
	if err != nil && s.interactive {
		classified := keychainError(err)
		if errors.Is(classified, ErrKeychainLocked) {
			// security owns the terminal prompt. The CLI never reads the password.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/usr/bin/security", "unlock-keychain")
			// Route either output stream to terminal stderr so any prompt stays
			// visible without corrupting machine-readable stdout.
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
			if unlockErr := cmd.Run(); unlockErr != nil {
				return nil, fmt.Errorf("%w: unlock-keychain: %v", ErrKeychainLocked, unlockErr)
			}
		} else if !errors.Is(classified, ErrKeychainAccessDenied) {
			return nil, classified
		}
		if s.gui && graphicalKeychainSession() {
			if status := C.SecKeychainSetUserInteractionAllowed(C.Boolean(1)); status != 0 {
				return nil, fmt.Errorf("%w: enabling Keychain authorization (status %d)", ErrKeychainUnavailable, int(status))
			}
			// The same retry also authorizes an updated binary after unlock.
			// Always Allow adds this binary to the item's trusted applications.
			result, err = keychain.QueryItem(item)
			if status := C.SecKeychainSetUserInteractionAllowed(C.Boolean(0)); status != 0 {
				return nil, fmt.Errorf("%w: disabling Keychain UI (status %d)", ErrKeychainUnavailable, int(status))
			}
		} else if errors.Is(classified, ErrKeychainAccessDenied) {
			return nil, classified
		} else {
			result, err = keychain.QueryItem(item)
		}
	}
	if err != nil {
		return nil, keychainError(err)
	}
	return singleKeychainResult(result)
}

func singleKeychainResult(result []keychain.QueryResult) ([]byte, error) {
	if len(result) == 0 {
		return nil, ErrNoCredentials
	}
	if len(result) != 1 {
		return nil, errors.New("ambiguous keychain credential")
	}
	return result[0].Data, nil
}
func (systemKeychain) Put(k key, value []byte) error {
	keychainInteractionMu.Lock()
	defer keychainInteractionMu.Unlock()
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
	keychainInteractionMu.Lock()
	defer keychainInteractionMu.Unlock()
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
