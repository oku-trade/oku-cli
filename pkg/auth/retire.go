package auth

import "errors"

// retireLegacy removes both legacy stores after checking both for presence.
// A failed provider read must never be mistaken for absence.
func retireLegacy(profile string, native credentialItemStore) (bool, error) {
	k := deviceKey(profile)
	_, fileErr := fileLoadMetadata(k)
	if fileErr != nil && !errors.Is(fileErr, ErrNoCredentials) {
		return false, fileErr
	}
	_, _, nativeErr := nativeLoad(native, k)
	if nativeErr != nil && !errors.Is(nativeErr, ErrNoCredentials) {
		return false, nativeErr
	}
	filePresent, nativePresent := fileErr == nil, nativeErr == nil
	if !filePresent && !nativePresent {
		return false, nil
	}
	if nativePresent {
		if err := native.Remove(k); err != nil {
			return true, err
		}
	}
	if filePresent {
		if err := fileDelete(k); err != nil {
			return true, err
		}
	}
	return true, nil
}
