package signing

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// enroll_vector.json comes from viem signMessage in gen_reference_vectors.mjs.
func TestEnrollmentSignatureVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/enroll_vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		RawCode   string `json:"raw_code"`
		Message   string `json:"message"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	s, _ := NewKeySigner(key)
	msg := EnrollmentMessage(v.RawCode, s.Address(), "laptop")
	if msg != v.Message {
		t.Fatalf("message\n got %q\nwant %q", msg, v.Message)
	}
	if strings.HasSuffix(msg, "\n") {
		t.Fatal("message must not end with newline")
	}
	sig, err := SignPersonal(s, msg)
	if err != nil || sig != v.Signature {
		t.Fatalf("signature %s want %s (%v)", sig, v.Signature, err)
	}
}

func TestValidateDeviceName(t *testing.T) {
	for in, ok := range map[string]bool{
		"  laptop ":             true,
		"":                      false,
		"   ":                   false,
		"a\nb":                  false,
		"tab\there":             false,
		strings.Repeat("x", 64): true,
		strings.Repeat("x", 65): false,
	} {
		if _, err := ValidateDeviceName(in); (err == nil) != ok {
			t.Errorf("ValidateDeviceName(%q) err=%v want ok=%v", in, err, ok)
		}
	}
}
