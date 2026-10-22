//go:build darwin

package auth

// macOS never uses the passphrase file store.
const fileStoreSupported = false
