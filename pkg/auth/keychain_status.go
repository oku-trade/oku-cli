package auth

// KeychainStatus classifies the Security framework statuses returned by a
// generic-password query. An interaction failure is ambiguous until the
// default Keychain's lock state has been checked.
func KeychainStatus(status int, locked bool) error {
	switch status {
	case -25300: // errSecItemNotFound
		return ErrNoCredentials
	case -128: // errSecUserCanceled
		return ErrKeychainCanceled
	case -25308, -25293, -25243: // interaction, authentication, no access
		if locked {
			return ErrKeychainLocked
		}
		return ErrKeychainAccessDenied
	case -25291, -25294: // unavailable, no such keychain
		return ErrKeychainUnavailable
	default:
		return nil
	}
}
