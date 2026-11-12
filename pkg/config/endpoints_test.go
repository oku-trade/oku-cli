package config

import "testing"

func TestHyperliquidInfoEndpoint(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{"", true}, {"https://api.hyperliquid.xyz/info", true}, {"http://127.0.0.1:1234/info", true},
		{"http://example.com/info", false}, {"https://example.com/info?key=value", false}, {"https://example.com/info#part", false}, {"file:///info", false}, {"https://user@example.com/info", false},
	} {
		got, err := LoadHyperliquidInfoURL(func(string) string { return tc.raw })
		if (err == nil) != tc.ok {
			t.Errorf("endpoint validity mismatch: %v", err)
		}
		if tc.raw == "" && got != DefaultHyperliquidInfoURL {
			t.Errorf("wrong default: %s", got)
		}
	}
}
