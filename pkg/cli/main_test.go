package cli

import (
	"os"
	"testing"
)

// TestMain keeps the suite away from the developer's real kernel keyring.
// Tests that exercise caching inject an in-memory UnlockCache.
func TestMain(m *testing.M) {
	defaultUnlockCache = func() unlockCache { return nil }
	os.Exit(m.Run())
}
