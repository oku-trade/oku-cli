//go:build linux

package auth

import "fmt"

// systemKeyring is replaceable so tests never touch the user's real keyring.
var systemKeyring credentialItemStore = secretService{}

func nativeStore(st Store) (credentialItemStore, error) {
	if st != StoreKeyring {
		return nil, fmt.Errorf("credential store %q is not available on Linux", st)
	}
	return systemKeyring, nil
}
