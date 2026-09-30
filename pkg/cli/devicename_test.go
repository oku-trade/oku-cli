package cli

import (
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/signing"
)

func TestDefaultDeviceNameBoundsHostname(t *testing.T) {
	long := "runner-" + strings.Repeat("a", 80) + ".github.internal.cloudapp.net"
	cases := map[string]string{
		"laptop":                       "laptop",
		"  host\t\n":                   "host",
		"":                             "oku-cli",
		"\x01\x02":                     "oku-cli",
		long:                           long[:64],
		strings.Repeat("é", 70):        strings.Repeat("é", 64),
		strings.Repeat("a", 63) + " b": strings.Repeat("a", 63),
	}
	for in, want := range cases {
		got := defaultDeviceName(in)
		if got != want {
			t.Errorf("defaultDeviceName(%q) = %q, want %q", in, got, want)
		}
		if _, err := signing.ValidateDeviceName(got); err != nil {
			t.Errorf("default %q invalid: %v", got, err)
		}
	}
}

func TestExplicitInvalidDeviceNameStillRejected(t *testing.T) {
	h := newHarness(t)
	h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL)
	h.stdin = "abcd-efgh jkmn-pqrs\n"
	r := h.run("auth", "setup", "--code-stdin", "--device-name", strings.Repeat("x", 65), "--json")
	if r.errCode() != "INVALID_ARGUMENT" || h.fake.redeemed != nil {
		t.Fatalf("got %s", r.stdout)
	}
}
