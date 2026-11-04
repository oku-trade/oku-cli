package config

import (
	"os"
	"strings"
	"testing"
)

func TestRPCURLValidation(t *testing.T) {
	for _, s := range []string{"0", "-1", "01", "abc", "", "9007199254740992"} {
		if _, err := ParseChainID(s); err == nil {
			t.Errorf("chain ID %q must be rejected", s)
		}
	}
	if id, err := ParseChainID("42161"); err != nil || id != 42161 {
		t.Fatalf("42161: %d %v", id, err)
	}
	for _, u := range []string{"", "arb.example", "ftp://x.example", "ws://x.example", "https://", "http:///p"} {
		if ValidateRPCURL(u) == nil {
			t.Errorf("URL %q must be rejected", u)
		}
	}
	if err := ValidateRPCURL("https://x.example/v2/key"); err != nil {
		t.Fatal(err)
	}
}

func TestRedactRPCURL(t *testing.T) {
	cases := map[string]string{
		"https://arb.example/v2/SECRET":     "https://arb.example/<redacted>",
		"https://arb.example/?token=SECRET": "https://arb.example/<redacted>",
		"https://user:SECRET@arb.example":   "https://arb.example/<redacted>",
		"https://arb.example":               "https://arb.example",
		"::garbage SECRET":                  "<redacted>",
	}
	for in, want := range cases {
		if got := RedactRPCURL(in); got != want || strings.Contains(got, "SECRET") {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestLoadRejectsBadRPCURLsNamingKey(t *testing.T) {
	t.Setenv("OKU_CONFIG_DIR", t.TempDir())
	p, _ := Path()
	for body, key := range map[string]string{
		`{"version":1,"rpc_urls":{"abc":"https://x.example"}}`: `"abc"`,
		`{"version":1,"rpc_urls":{"1":"ftp://x.example"}}`:     `["1"]`,
	} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: want error naming %s, got %v", body, key, err)
		}
	}
}

func TestSaveRPCURLsMode0600(t *testing.T) {
	t.Setenv("OKU_CONFIG_DIR", t.TempDir())
	f, _ := Load()
	if err := f.SetRPCURL(42161, "https://x.example/key"); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	p, _ := Path()
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v %v", st, err)
	}
	g, err := Load()
	if err != nil || g.RPCURLs["42161"] != "https://x.example/key" {
		t.Fatalf("round trip: %v %v", g, err)
	}
}
