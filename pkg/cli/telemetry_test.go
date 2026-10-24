package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/telemetry"
)

type telSink struct {
	mu     sync.Mutex
	events []map[string]any
	srv    *httptest.Server
}

func newTelSink(t *testing.T, status int) *telSink {
	s := &telSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var ev map[string]any
		_ = json.Unmarshal(raw, &ev)
		s.mu.Lock()
		s.events = append(s.events, ev)
		s.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *telSink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.events) }

func telApp(t *testing.T, env map[string]string, tty bool) (*App, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &App{
		Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(""), StdinIsTTY: tty,
		ReadSecret: func(string) ([]byte, error) { return nil, errNoTTY },
		Getenv:     func(k string) string { return env[k] },
		Now:        time.Now, Version: "test",
	}, &out, &errb
}

func telDirs(t *testing.T) {
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("OKU_STATE_DIR", filepath.Join(t.TempDir(), "state"))
}

func TestTelemetryOffByDefaultSendsNothing(t *testing.T) {
	telDirs(t)
	sink := newTelSink(t, 200)
	env := map[string]string{"OKU_TELEMETRY_HOST": sink.srv.URL}
	a, _, _ := telApp(t, env, true)
	if code := a.Run([]string{"version", "--json"}); code != 0 {
		t.Fatal(code)
	}
	a, _, _ = telApp(t, env, true)
	a.Run([]string{"nope"})
	if sink.count() != 0 {
		t.Fatalf("sent %d events while off", sink.count())
	}
	state, _ := config.StateDir()
	if telemetry.InstallID(state) != "" {
		t.Fatal("install id created while off")
	}
}

func TestTelemetryDNTBeatsConfigAndEnv(t *testing.T) {
	telDirs(t)
	sink := newTelSink(t, 200)
	a, _, _ := telApp(t, nil, false)
	if a.Run([]string{"telemetry", "enable"}) != 0 {
		t.Fatal("enable failed")
	}
	env := map[string]string{"OKU_TELEMETRY_HOST": sink.srv.URL, "DO_NOT_TRACK": "1", "OKU_TELEMETRY": "1"}
	a, out, _ := telApp(t, env, false)
	a.Run([]string{"telemetry", "status", "--json"})
	if !strings.Contains(out.String(), `"reason": "env DO_NOT_TRACK"`) || !strings.Contains(out.String(), `"enabled": false`) {
		t.Fatal(out.String())
	}
	a, _, _ = telApp(t, env, false)
	a.Run([]string{"version"})
	if sink.count() != 0 {
		t.Fatal("sent despite DO_NOT_TRACK")
	}
	env = map[string]string{"OKU_TELEMETRY_HOST": sink.srv.URL, "OKU_TELEMETRY": "0"}
	a, _, _ = telApp(t, env, false)
	a.Run([]string{"version"})
	if sink.count() != 0 {
		t.Fatal("sent despite OKU_TELEMETRY=0")
	}
}

var (
	telAddrRE = regexp.MustCompile(`0x[0-9a-fA-F]{40}`)
	telAmtRE  = regexp.MustCompile(`\d+\.\d+`)
)

func TestTelemetryEnabledPayloadAndOutputUnchanged(t *testing.T) {
	telDirs(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	f, _ := config.Load()
	f.Profiles["t"] = &config.Profile{Network: "mainnet", APIURL: dead.URL}
	f.DefaultProfile = "t"
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	args := []string{"swap", "quote", "--chain", "42161", "--from", "native", "--to", "0xaf88d065e77c8cC2239327C5EDb3A432268e5831", "--amount", "1.5", "--json"}

	// Baseline with telemetry off.
	a, offOut, _ := telApp(t, map[string]string{"OKU_RPC_URL_42161": dead.URL}, false)
	offCode := a.Run(args)

	sink := newTelSink(t, 500) // failing ingestion must not matter
	env := map[string]string{"OKU_TELEMETRY": "1", "OKU_TELEMETRY_HOST": sink.srv.URL, "OKU_RPC_URL_42161": dead.URL}
	a, onOut, _ := telApp(t, env, false)
	onCode := a.Run(args)
	if onCode != offCode || onCode == 0 {
		t.Fatalf("exit codes off=%d on=%d", offCode, onCode)
	}
	strip := regexp.MustCompile(`"request_id": "[0-9a-f]+"`)
	if strip.ReplaceAllString(onOut.String(), "") != strip.ReplaceAllString(offOut.String(), "") {
		t.Fatalf("stdout changed:\n%s\nvs\n%s", onOut, offOut)
	}
	if sink.count() != 1 {
		t.Fatalf("events %d", sink.count())
	}
	ev := sink.events[0]
	raw, _ := json.Marshal(ev)
	props := ev["properties"].(map[string]any)
	for k := range props {
		if _, ok := telemetry.AllowedProperties[k]; !ok {
			t.Fatalf("non-allowlisted property %q", k)
		}
	}
	for k := range ev {
		switch k {
		case "api_key", "event", "distinct_id", "properties", "timestamp":
		default:
			t.Fatalf("unexpected top-level key %q", k)
		}
	}
	if telAddrRE.Match(raw) {
		t.Fatalf("address leaked: %s", raw)
	}
	propsRaw, _ := json.Marshal(props)
	if telAmtRE.Match(propsRaw) {
		t.Fatalf("decimal leaked: %s", propsRaw)
	}
	if strings.Contains(string(raw), dead.URL) || strings.Contains(string(raw), "127.0.0.1") {
		t.Fatalf("url leaked: %s", raw)
	}
	want := map[string]any{"command": "swap quote", "success": false, "chain_id": float64(42161), "amount_source": "exact",
		"from_kind": "native", "to_kind": "erc20", "api_env": "local", "json": true, "source": "oku-cli", "invoked_by": "non_tty"}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("%s = %v want %v", k, props[k], v)
		}
	}
	if props["error_code"] == nil || ev["event"] != "cli_command" {
		t.Fatalf("%s", raw)
	}
	state, _ := config.StateDir()
	if ev["distinct_id"] != telemetry.InstallID(state) {
		t.Fatal("distinct_id is not the install id")
	}
}

func TestTelemetryEnableDisableStatus(t *testing.T) {
	telDirs(t)
	a, out, _ := telApp(t, nil, false)
	a.Run([]string{"telemetry", "status", "--json"})
	if !strings.Contains(out.String(), `"enabled": false`) || !strings.Contains(out.String(), `"reason": "default"`) {
		t.Fatal(out.String())
	}
	a, _, _ = telApp(t, nil, false)
	a.Run([]string{"telemetry", "enable"})
	state, _ := config.StateDir()
	id := telemetry.InstallID(state)
	if id == "" {
		t.Fatal("no id after enable")
	}
	a, out, _ = telApp(t, nil, false)
	a.Run([]string{"telemetry", "status"})
	if !strings.Contains(out.String(), "telemetry: on (config)") || !strings.Contains(out.String(), id) {
		t.Fatal(out.String())
	}
	a, _, _ = telApp(t, nil, false)
	a.Run([]string{"telemetry", "disable"})
	if telemetry.InstallID(state) != "" {
		t.Fatal("id kept after disable")
	}
	raw, _ := os.ReadFile(filepath.Join(os.Getenv("OKU_CONFIG_DIR"), "config.json"))
	if !strings.Contains(string(raw), `"enabled": false`) {
		t.Fatal(string(raw))
	}
}

func TestTelemetryHintOnceTTYOnly(t *testing.T) {
	telDirs(t)
	r := &telemetryRun{command: "auth login"}
	a, _, errb := telApp(t, nil, false)
	a.maybeTelemetryHint(r, 0)
	if errb.Len() != 0 {
		t.Fatal("hint on non-tty")
	}
	a, _, errb = telApp(t, nil, true)
	a.maybeTelemetryHint(&telemetryRun{command: "auth login", json: true}, 0)
	if errb.Len() != 0 {
		t.Fatal("hint in json mode")
	}
	a.maybeTelemetryHint(r, 0)
	if !strings.Contains(errb.String(), "oku telemetry enable") {
		t.Fatal("no hint")
	}
	errb.Reset()
	a.maybeTelemetryHint(r, 0)
	if errb.Len() != 0 {
		t.Fatal("hint repeated")
	}
}

func TestAPIEnvName(t *testing.T) {
	for in, want := range map[string]string{config.DefaultAPIURL: "prod", config.StagingAPIURL + "/": "staging", "http://localhost:8080": "local", "https://example.com": "custom"} {
		if got := apiEnvName(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestTelemetryRejectedRouteDetailsAreCategoriesOnly(t *testing.T) {
	telDirs(t)
	sink := newTelSink(t, 200)
	a, _, _ := telApp(t, map[string]string{"OKU_TELEMETRY": "1", "OKU_TELEMETRY_HOST": sink.srv.URL}, false)
	r := &telemetryRun{start: time.Now(), command: "swap prepare", route: "kyberswap", props: map[string]any{"route_forced": false},
		errCode: "REJECTED", details: map[string]any{"attempts": 2, "routes": map[string]string{
			"kyberswap": "amount_mismatch: got 123 want 456",
			"odos":      "backend_error: some message 0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
			"zerox":     "not_answered",
			"weird":     "brand_new_reason: details",
		}}}
	r.props["route"] = r.route
	a.finishTelemetry(r, 1)
	if sink.count() != 1 {
		t.Fatalf("events %d", sink.count())
	}
	raw, _ := json.Marshal(sink.events[0]["properties"])
	for _, bad := range []string{"123", "456", "0xabc", "some message", "details", "odos", "zerox", "weird"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("%q leaked: %s", bad, raw)
		}
	}
	props := sink.events[0]["properties"].(map[string]any)
	want := map[string]any{"routes_considered": 4.0, "routes_rejected": 4.0, "route_attempts": 2.0, "route": "kyberswap", "route_forced": false,
		"route_rejections": "amount_mismatch:1,backend_error:1,not_answered:1,other:1"}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("%s = %v want %v", k, props[k], v)
		}
	}
}
