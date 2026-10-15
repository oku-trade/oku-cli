//go:build linux

package auth

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestKernelKeyringUnlockCache(t *testing.T) {
	profile := fmt.Sprintf("test-%d-%d", os.Getpid(), time.Now().UnixNano())
	if err := CachePassphrase(profile, []byte("correct horse battery"), time.Minute); err != nil {
		t.Skipf("kernel keyring unavailable: %v", err)
	}
	t.Cleanup(func() { ForgetPassphrase(profile) })
	if got := CachedPassphrase(profile); string(got) != "correct horse battery" {
		t.Fatalf("cached = %q", got)
	}
	ForgetPassphrase(profile)
	if got := CachedPassphrase(profile); got != nil {
		t.Fatalf("forgotten passphrase still readable: %q", got)
	}
	if err := CachePassphrase(profile, []byte("x"), 0); err == nil {
		t.Fatal("zero ttl accepted")
	}
}
