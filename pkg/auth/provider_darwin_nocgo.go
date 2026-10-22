//go:build darwin && !cgo

package auth

// Native macOS Keychain access requires CGO. Never substitute encrypted-file
// storage when the native provider cannot be linked.
func nativeStore(Store) (credentialItemStore, error) { return nil, ErrUnsupportedPlatform }
