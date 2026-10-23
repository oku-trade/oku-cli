//go:build darwin

package auth

// macOS always uses the Keychain, never the file store.
const fileStoreSupported = false
