package decimal

import "testing"

// Canonical form is part of the signed payload: a non-canonical value such as
// "2.00" hashes differently and recovers a different signer on Hyperliquid.
func TestCanonical(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"0.0100", "0.01", true},
		{"00050000", "50000", true},
		{"2.00", "2", true},
		{"0", "0", true},
		{"0.000", "0", true},
		{"-1", "", false},
		{"1e5", "", false},
		{".5", "", false},
		{"5.", "", false},
		{"", "", false},
		{" 1", "", false},
		{"NaN", "", false},
	}
	for _, c := range cases {
		got, err := Canonical(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("Canonical(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
		if c.ok && !IsCanonical(got) {
			t.Errorf("Canonical(%q) produced non-canonical %q", c.in, got)
		}
	}
}

func TestPerpRules(t *testing.T) {
	cases := []struct {
		px    string
		szDec int
		ok    bool
	}{
		{"50000", 5, true},
		{"123456", 0, true}, // integers allowed beyond 5 sig figs
		{"1234.5", 1, true},
		{"1234.56", 1, false}, // 6 sig figs
		{"0.12345", 1, true},
		{"0.123456", 0, false},
		{"0.1", 5, true},
		{"0.12", 5, false}, // 6-5 = 1 decimal max
	}
	for _, c := range cases {
		if err := ValidatePerpPrice(c.px, c.szDec); (err == nil) != c.ok {
			t.Errorf("ValidatePerpPrice(%s,%d) err=%v want ok=%v", c.px, c.szDec, err, c.ok)
		}
	}
	if ValidateSize("0.001", 2) == nil || ValidateSize("0", 3) == nil || ValidateSize("0.001", 3) != nil {
		t.Fatal("size precision rules violated")
	}
}
