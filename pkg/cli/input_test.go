package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The executable dispatch boundary owns strict JSON parsing and flag parity.
func TestJSONInputDispatch(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL); r.code != 0 {
		t.Fatalf("profile: %s %s", r.stdout, r.stderr)
	}
	good := `{"market":"BTC","side":"buy","type":"limit","size":"0.00100","price":"50000","intent-id":"input-test"}`
	file := filepath.Join(t.TempDir(), "order.json")
	if err := os.WriteFile(file, []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	flags := h.run("orders", "prepare", "--market", "BTC", "--side", "buy", "--type", "limit", "--size", "0.00100", "--price", "50000", "--intent-id", "flags-test", "--json")
	fromFile := h.run("orders", "prepare", "--input", file, "--json")
	if flags.code != 0 || fromFile.code != 0 {
		t.Fatalf("flags=%s file=%s", flags.stdout, fromFile.stdout)
	}
	// Intent IDs differ, but financial preview values must match.
	f, g := flags.data()["intent"].(map[string]any), fromFile.data()["intent"].(map[string]any)
	for _, key := range []string{"market", "side", "order_type", "size", "price", "tif", "account", "asset_id"} {
		if !reflect.DeepEqual(f[key], g[key]) {
			t.Errorf("%s: %v vs %v", key, f[key], g[key])
		}
	}
	cases := []struct {
		name, input string
		args        []string
		ok          bool
	}{
		{"stdin", good, nil, true},
		{"wrong type", `{"market":"BTC","side":"buy","type":"limit","size":0.001,"price":"50000"}`, nil, false},
		{"unknown key", `{"market":"BTC","side":"buy","type":"limit","size":"0.001","price":"50000","bogus":1}`, nil, false},
		{"global key", `{"market":"BTC","side":"buy","type":"limit","size":"0.001","price":"50000","profile":"main"}`, nil, false},
		{"trailing", good + ` true`, nil, false},
		{"mixed", good, []string{"--market", "ETH"}, false},
		{"enum", `{"market":"BTC","side":"hold","type":"limit","size":"0.001","price":"50000"}`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.stdin = tc.input
			args := append([]string{"orders", "prepare", "--input", "-", "--json"}, tc.args...)
			r := h.run(args...)
			if tc.ok && r.code != 0 {
				t.Fatalf("%s", r.stdout)
			}
			if !tc.ok && r.errCode() != "USAGE" {
				t.Fatalf("%s", r.stdout)
			}
		})
	}
	r := h.run("docs", "commands", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"input"`) || !strings.Contains(r.stdout, `"market"`) {
		t.Fatalf("missing schema: %s", r.stdout)
	}
}

func TestInputSchemasShareFlagValidation(t *testing.T) {
	for path, h := range (&App{}).commands() {
		if h.flags == nil {
			continue
		}
		schema, err := InputSchema(path)
		if err != nil {
			t.Fatal(err)
		}
		if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("invalid shape: %s", path)
		}
		b, err := json.Marshal(schema)
		if err != nil || !json.Valid(b) {
			t.Fatalf("invalid JSON schema: %s: %v", path, err)
		}
		example := schema["examples"].([]any)[0].(map[string]any)
		if _, err := newMapFlags(h, example); err != nil {
			t.Fatalf("%s example: %v", path, err)
		}
	}
}
