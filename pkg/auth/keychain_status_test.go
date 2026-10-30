package auth

import (
	"errors"
	"testing"
)

func TestKeychainStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		locked bool
		want   error
	}{
		{-25308, true, ErrKeychainLocked}, {-25308, false, ErrKeychainAccessDenied},
		{-25293, true, ErrKeychainLocked}, {-25293, false, ErrKeychainAccessDenied},
		{-25243, false, ErrKeychainAccessDenied}, {-128, false, ErrKeychainCanceled},
		{-25300, false, ErrNoCredentials}, {-25291, false, ErrKeychainUnavailable},
		{-25294, false, ErrKeychainUnavailable},
	} {
		if got := KeychainStatus(tc.status, tc.locked); !errors.Is(got, tc.want) {
			t.Errorf("status %d locked %v: got %v, want %v", tc.status, tc.locked, got, tc.want)
		}
	}
}
