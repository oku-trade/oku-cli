package onchain

import (
	"errors"
	"strings"
	"testing"
)

func TestRPCResolverPrecedence(t *testing.T) {
	env := map[string]string{"OKU_RPC_URL_10": "https://env.example"}
	loads := 0
	r := NewRPCResolver(func(k string) string { return env[k] }, func() (map[string]string, error) {
		loads++
		return map[string]string{"10": "https://cfg-op.example", "324": "https://cfg-zk.example"}, nil
	})
	if u, src, err := r.Lookup(10); err != nil || u != "https://env.example" || src != RPCSourceEnv {
		t.Fatalf("env must win: %q %q %v", u, src, err)
	}
	if loads != 0 {
		t.Fatal("config must not load when env answers")
	}
	if u, src, err := r.Lookup(324); err != nil || u != "https://cfg-zk.example" || src != RPCSourceConfig {
		t.Fatalf("config fallback: %q %q %v", u, src, err)
	}
	_, _, _ = r.Lookup(324)
	_, err := r.URL(42161)
	if !errors.Is(err, ErrNoRPC) || !strings.Contains(err.Error(), "OKU_RPC_URL_42161") || !strings.Contains(err.Error(), "oku rpc set 42161") {
		t.Fatalf("missing RPC must name both ways: %v", err)
	}
	if loads != 1 {
		t.Fatalf("config loaded %d times, want once", loads)
	}
}

func TestRPCResolverConfigErrorPropagates(t *testing.T) {
	r := NewRPCResolver(func(string) string { return "" }, func() (map[string]string, error) { return nil, errors.New("bad config") })
	if _, err := r.URL(1); err == nil || errors.Is(err, ErrNoRPC) || !strings.Contains(err.Error(), "bad config") {
		t.Fatalf("config error must surface, not read as missing RPC: %v", err)
	}
}
