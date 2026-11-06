package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/config"
)

func TestRPCSetListUnsetRoundTripRedacts(t *testing.T) {
	telDirs(t)
	const secret = "SECRETKEY"
	run := func(env map[string]string, args ...string) (int, string, string) {
		a, out, errb := telApp(t, env, false)
		code := a.Run(args)
		return code, out.String(), errb.String()
	}
	if code, out, errb := run(nil, "rpc", "set", "42161", "https://arb.example/v2/"+secret, "--json"); code != 0 || strings.Contains(out+errb, secret) {
		t.Fatalf("set: %d %s %s", code, out, errb)
	}
	code, out, _ := run(nil, "rpc", "list", "--json")
	if code != 0 || strings.Contains(out, secret) || !strings.Contains(out, `"source": "config"`) && !strings.Contains(out, `"source":"config"`) {
		t.Fatalf("list: %d %s", code, out)
	}
	_, out, _ = run(map[string]string{"OKU_RPC_URL_42161": "https://env.example/" + secret}, "rpc", "list", "--json")
	if !strings.Contains(out, "env") || strings.Contains(out, secret) {
		t.Fatalf("env override must show source env, redacted: %s", out)
	}
	_, out, _ = run(nil, "doctor", "--json")
	if strings.Contains(out, secret) || !strings.Contains(out, "arb.example") {
		t.Fatalf("doctor must show redacted rpc: %s", out)
	}
	if code, _, _ := run(nil, "rpc", "set", "abc", "https://x.example"); code == 0 {
		t.Fatal("bad chain ID must fail")
	}
	if code, _, _ := run(nil, "rpc", "set", "1", "ws://x.example"); code == 0 {
		t.Fatal("bad URL must fail")
	}
	if code, _, _ := run(nil, "rpc", "unset", "42161"); code != 0 {
		t.Fatal("unset failed")
	}
	if code, _, _ := run(nil, "rpc", "unset", "42161"); code == 0 {
		t.Fatal("unsetting a missing chain must fail")
	}
}

func TestHTTPRPCUsesConfigURL(t *testing.T) {
	telDirs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xa4b1"}`))
	}))
	t.Cleanup(srv.Close)
	a, _, _ := telApp(t, nil, false)
	if code := a.Run([]string{"rpc", "set", "42161", srv.URL}); code != 0 {
		t.Fatalf("set: %d", code)
	}
	a.rpc = nil
	if _, err := a.httpRPC().Call(context.Background(), 42161, "0x0000000000000000000000000000000000000001", "0x313ce567"); err != nil && strings.Contains(err.Error(), "no RPC endpoint") {
		t.Fatalf("config URL not used: %v", err)
	}
}

func TestConfigShowRedactsRPCURLsButFilePersistsFull(t *testing.T) {
	telDirs(t)
	const full = "https://review-user:FAKE_REVIEW_PASSWORD@rpc.example.invalid/v2/FAKE_REVIEW_KEY?token=FAKE_REVIEW_QUERY"
	a, _, _ := telApp(t, nil, false)
	if code := a.Run([]string{"--json", "rpc", "set", "42161", full}); code != 0 {
		t.Fatalf("set: %d", code)
	}
	for _, args := range [][]string{{"--json", "config", "show"}, {"config", "show"}, {"--json", "profiles", "list"}, {"--json", "doctor"}} {
		a, out, errb := telApp(t, nil, false)
		a.Run(args)
		got := out.String() + errb.String()
		for _, s := range []string{"review-user", "FAKE_REVIEW_PASSWORD", "FAKE_REVIEW_KEY", "FAKE_REVIEW_QUERY"} {
			if strings.Contains(got, s) {
				t.Fatalf("%v leaks %q: %s", args, s, got)
			}
		}
	}
	a, out, _ := telApp(t, nil, false)
	a.Run([]string{"--json", "config", "show"})
	if !strings.Contains(out.String(), "rpc.example.invalid") {
		t.Fatalf("config show should still name the host: %s", out.String())
	}
	f, err := config.Load()
	if err != nil || f.RPCURLs["42161"] != full {
		t.Fatalf("persisted URL changed: %v %v", f.RPCURLs, err)
	}
	p, _ := config.Path()
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st, err)
	}
}
