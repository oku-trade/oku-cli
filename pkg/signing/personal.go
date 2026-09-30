package signing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// PersonalDigest returns the EIP-191 personal_sign digest of msg.
func PersonalDigest(msg string) [32]byte {
	prefix := "\x19Ethereum Signed Message:\n" + strconv.Itoa(len(msg))
	return keccak([]byte(prefix), []byte(msg))
}

// NormalizeEnrollmentCode uppercases and strips dashes and whitespace.
func NormalizeEnrollmentCode(code string) string {
	var b strings.Builder
	for _, r := range code {
		if r == '-' || unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// ValidateDeviceName trims and checks a device name (1..64, no controls).
func ValidateDeviceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return "", errors.New("device name must be 1..64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("device name must not contain control characters")
		}
	}
	return name, nil
}

// EnrollmentMessage builds the exact message signed during RedeemEnrollment.
func EnrollmentMessage(code, agentAddress, deviceName string) string {
	sum := sha256.Sum256([]byte(NormalizeEnrollmentCode(code)))
	return fmt.Sprintf("Oku CLI device enrollment v1\ncode: %s\nagent: %s\ndevice: %s",
		hex.EncodeToString(sum[:]), strings.ToLower(agentAddress), deviceName)
}

// SignPersonal returns a 65-byte hex personal_sign signature (v = 27/28).
func SignPersonal(s Signer, msg string) (string, error) {
	sig, err := s.SignDigest(PersonalDigest(msg))
	if err != nil {
		return "", err
	}
	return sig.R + strings.TrimPrefix(sig.S, "0x") + strconv.FormatUint(sig.V, 16), nil
}
