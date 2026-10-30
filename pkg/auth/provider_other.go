//go:build !darwin && !linux

package auth

func nativeStore(Store) (credentialItemStore, error) { return nil, ErrKeyringUnsupported }
func interactiveKeychain(bool) credentialItemStore   { return nil }
