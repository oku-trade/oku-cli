package onchain

import (
	"fmt"
	"os"
	"strconv"
	"sync"
)

// RPCSource says where an effective RPC URL came from.
type RPCSource string

const (
	RPCSourceEnv    RPCSource = "env"
	RPCSourceConfig RPCSource = "config"
)

// RPCResolver is the single owner of chain RPC lookup. OKU_RPC_URL_<CHAINID>
// wins, then the config file's rpc_urls, else ErrNoRPC. The oku-hosted or
// public default is deliberately absent. The config is loaded at most once,
// and only when the environment has no answer.
type RPCResolver struct {
	getenv func(string) string
	load   func() (map[string]string, error)

	once sync.Once
	cfg  map[string]string
	err  error
}

// NewRPCResolver builds a resolver. getenv defaults to os.Getenv. load may be
// nil for environment-only resolution; it returns validated rpc_urls.
func NewRPCResolver(getenv func(string) string, load func() (map[string]string, error)) *RPCResolver {
	if getenv == nil {
		getenv = os.Getenv
	}
	return &RPCResolver{getenv: getenv, load: load}
}

func envRPCName(chain uint64) string { return "OKU_RPC_URL_" + strconv.FormatUint(chain, 10) }

func (r *RPCResolver) config() (map[string]string, error) {
	r.once.Do(func() {
		if r.load != nil {
			r.cfg, r.err = r.load()
		}
	})
	return r.cfg, r.err
}

// Lookup returns the effective URL for chain and where it came from.
func (r *RPCResolver) Lookup(chain uint64) (string, RPCSource, error) {
	if v := r.getenv(envRPCName(chain)); v != "" {
		return v, RPCSourceEnv, nil
	}
	cfg, err := r.config()
	if err != nil {
		return "", "", fmt.Errorf("loading RPC config: %w", err)
	}
	if v := cfg[strconv.FormatUint(chain, 10)]; v != "" {
		return v, RPCSourceConfig, nil
	}
	return "", "", fmt.Errorf("%w %d: set %s or run oku rpc set %d <url>", ErrNoRPC, chain, envRPCName(chain), chain)
}

// URL returns the effective URL for chain.
func (r *RPCResolver) URL(chain uint64) (string, error) {
	u, _, err := r.Lookup(chain)
	return u, err
}
