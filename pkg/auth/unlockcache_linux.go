//go:build linux

package auth

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// The unlock cache keeps a profile's passphrase in the Linux kernel user
// keyring, like ssh-agent or sudo's timestamp. Keys never touch disk, expire
// after a TTL, are readable only by the same uid, and vanish on reboot.
// This lets non-interactive callers (coding agents, MCP) use a credential the
// user unlocked in their own terminal without an OKU_PASSPHRASE_FILE.

// keyPerm grants possessor and user view/read/write/search/setattr only.
const keyPerm = 0x3f3f0000

func cacheDesc(profile string) string { return "oku:passphrase:" + profile }

// CachePassphrase stores the passphrase for profile for ttl.
func CachePassphrase(profile string, pass []byte, ttl time.Duration) error {
	if profile == "" || len(pass) == 0 || ttl <= 0 {
		return errors.New("invalid unlock cache entry")
	}
	id, err := unix.AddKey("user", cacheDesc(profile), pass, unix.KEY_SPEC_USER_KEYRING)
	if err != nil {
		return err
	}
	_ = unix.KeyctlSetperm(id, keyPerm)
	secs := int(ttl / time.Second)
	if secs < 1 {
		secs = 1
	}
	if _, err := unix.KeyctlInt(unix.KEYCTL_SET_TIMEOUT, id, secs, 0, 0); err != nil {
		_, _ = unix.KeyctlInt(unix.KEYCTL_INVALIDATE, id, 0, 0, 0)
		return err
	}
	return nil
}

// CachedPassphrase returns the cached passphrase, or nil when absent/expired.
func CachedPassphrase(profile string) []byte {
	id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", cacheDesc(profile), 0)
	if err != nil {
		return nil
	}
	buf := make([]byte, 1024)
	n, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, buf, 0)
	if err != nil || n <= 0 || n > len(buf) {
		return nil
	}
	return buf[:n]
}

// ForgetPassphrase removes any cached passphrase for profile.
func ForgetPassphrase(profile string) {
	for {
		id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", cacheDesc(profile), 0)
		if err != nil {
			return
		}
		if _, err := unix.KeyctlInt(unix.KEYCTL_INVALIDATE, id, 0, 0, 0); err != nil {
			_, _ = unix.KeyctlInt(unix.KEYCTL_UNLINK, id, unix.KEY_SPEC_USER_KEYRING, 0, 0)
			return
		}
	}
}

// UnlockCacheSupported reports whether this platform has an unlock cache.
func UnlockCacheSupported() bool { return true }
