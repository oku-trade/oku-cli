//go:build !linux

package auth

import (
	"errors"
	"time"
)

// CachePassphrase is unsupported off Linux (macOS uses the Keychain).
func CachePassphrase(string, []byte, time.Duration) error {
	return errors.New("unlock cache unsupported on this platform")
}
func CachedPassphrase(string) []byte { return nil }
func ForgetPassphrase(string)        {}
func UnlockCacheSupported() bool     { return false }
