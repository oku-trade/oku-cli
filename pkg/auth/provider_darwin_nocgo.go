//go:build darwin && !cgo

package auth

// Native macOS Keychain access requires CGO. Never substitute encrypted-file
// storage when the native provider cannot be linked.
func nativeStore(Store) (credentialItemStore, error) { return nil, ErrUnsupportedPlatform }
func interactiveKeychain(bool) credentialItemStore   { return unsupportedInteractiveKeychain{} }

type unsupportedInteractiveKeychain struct{}

func (unsupportedInteractiveKeychain) Get(key) ([]byte, error) { return nil, ErrUnsupportedPlatform }
func (unsupportedInteractiveKeychain) Put(key, []byte) error   { return ErrUnsupportedPlatform }
func (unsupportedInteractiveKeychain) Remove(key) error        { return ErrUnsupportedPlatform }
