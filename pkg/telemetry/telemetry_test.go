package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestResolvePrecedence(t *testing.T) {
	cases := []struct {
		env map[string]string
		cfg bool
		on  bool
		why Reason
	}{
		{nil, false, false, ReasonDefault},
		{nil, true, true, ReasonConfig},
		{map[string]string{"OKU_TELEMETRY": "0"}, true, false, ReasonEnv},
		{map[string]string{"OKU_TELEMETRY": "1"}, false, true, ReasonEnv},
		{map[string]string{"DO_NOT_TRACK": "1"}, true, false, ReasonDoNotTrack},
		{map[string]string{"DO_NOT_TRACK": "1", "OKU_TELEMETRY": "1"}, true, false, ReasonDoNotTrack},
		{map[string]string{"DO_NOT_TRACK": "0"}, true, true, ReasonConfig},
	}
	for i, c := range cases {
		on, why := Resolve(envOf(c.env), c.cfg)
		if on != c.on || why != c.why {
			t.Errorf("case %d: got %v %v want %v %v", i, on, why, c.on, c.why)
		}
	}
}

var (
	addrRE = regexp.MustCompile(`0x[0-9a-fA-F]{40}`)
	amtRE  = regexp.MustCompile(`"\d+\.\d+"`)
)

func TestBuildAllowlist(t *testing.T) {
	props := Build(map[string]any{
		"command": "swap prepare", "chain_id": "42161", "route": "0x1111111111111111111111111111111111111111",
		"amount": "1.5", "amount_source": "1.25", "recipient": "x", "error_message": "boom",
		"$lib": "spoof", "tx_hash": "0xabc", "nested": map[string]any{"a": 1},
	})
	for k := range props {
		if _, ok := AllowedProperties[k]; !ok {
			t.Fatalf("non-allowlisted key %q", k)
		}
	}
	raw, _ := json.Marshal(props)
	if addrRE.Match(raw) || amtRE.Match(raw) {
		t.Fatalf("leak: %s", raw)
	}
	if props["$lib"] != Source || props["source"] != Source || props["command"] != "swap prepare" {
		t.Fatalf("props %v", props)
	}
}

func TestCategories(t *testing.T) {
	got := Categories(map[string]int{"simulation_failed": 1, "amount_mismatch": 2, "Bad Text!": 3, "zero": 0})
	if got != "amount_mismatch:2,simulation_failed:1" {
		t.Fatal(got)
	}
}

func TestSendPostsOneEvent(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/i/v0/e/" {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}))
	defer srv.Close()
	if err := Send(context.Background(), srv.URL, "k", "id", EventCommand, map[string]any{"command": "version"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if body["event"] != "cli_command" || body["api_key"] != "k" || body["distinct_id"] != "id" {
		t.Fatalf("%v", body)
	}
	if err := Send(context.Background(), srv.URL, "k", "id", "command", nil, time.Now()); err == nil {
		t.Fatal("unprefixed event accepted")
	}
}

func TestSendTimeoutBounded(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-done }))
	defer srv.Close()
	defer close(done)
	start := time.Now()
	if err := Send(context.Background(), srv.URL, "k", "id", EventCommand, nil, time.Now()); err == nil {
		t.Fatal("expected timeout")
	}
	if d := time.Since(start); d > FlushTimeout+time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestInstallID(t *testing.T) {
	dir := t.TempDir()
	if InstallID(dir) != "" {
		t.Fatal("id before enable")
	}
	id, err := EnsureInstallID(dir)
	if err != nil || InstallID(dir) != id {
		t.Fatal(id, err)
	}
	again, _ := EnsureInstallID(dir)
	if again != id {
		t.Fatal("id changed")
	}
	if err := RemoveInstallID(dir); err != nil || InstallID(dir) != "" {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}
