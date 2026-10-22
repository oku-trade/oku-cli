//go:build !darwin && !linux

package auth

func nativeStore(Store) (credentialItemStore, error) { return nil, ErrKeyringUnsupported }
