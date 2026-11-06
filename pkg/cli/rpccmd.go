package cli

import (
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// rpcResolver returns the App's single RPC resolver, built from Getenv and the
// config file on first use. Run and Service.Call reset it per invocation so a
// long-lived MCP server sees `oku rpc set` changes.
func (a *App) rpcResolver() *onchain.RPCResolver {
	if a.rpc == nil {
		a.rpc = onchain.NewRPCResolver(a.Getenv, func() (map[string]string, error) {
			f, err := config.Load()
			if err != nil {
				return nil, err
			}
			return f.RPCURLs, nil
		})
	}
	return a.rpc
}

// httpRPC returns the chain RPC client wired to the shared resolver.
func (a *App) httpRPC() onchain.HTTPRPC {
	return onchain.HTTPRPC{Getenv: a.Getenv, Resolver: a.rpcResolver()}
}

// rpcEntry is one chain's effective RPC with the URL redacted.
type rpcEntry struct {
	Chain  uint64 `json:"chain"`
	URL    string `json:"url"`
	Source string `json:"source"`
}

// rpcEntries lists every chain with an effective RPC: each configured chain,
// with source env when OKU_RPC_URL_<id> overrides it. Env-only chains are not
// enumerable (the environment is not scanned) and appear only if configured.
func (a *App) rpcEntries(f *config.File) ([]rpcEntry, error) {
	res := onchain.NewRPCResolver(a.Getenv, func() (map[string]string, error) { return f.RPCURLs, nil })
	out := []rpcEntry{}
	for k := range f.RPCURLs {
		chain, err := config.ParseChainID(k)
		if err != nil {
			return nil, err
		}
		u, src, err := res.Lookup(chain)
		if err != nil {
			return nil, err
		}
		out = append(out, rpcEntry{Chain: chain, URL: config.RedactRPCURL(u), Source: string(src)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Chain < out[j].Chain })
	return out, nil
}

func rpcChainArg(s string) (uint64, error) {
	chain, err := config.ParseChainID(s)
	if err != nil {
		return 0, output.Errorf(output.CodeInvalidArgument, "%v", err)
	}
	return chain, nil
}

func (a *App) cmdRPCSet(e *env) (*result, error) {
	args, err := e.positional(2, "rpc set CHAIN_ID URL")
	if err != nil {
		return nil, err
	}
	chain, err := rpcChainArg(args[0])
	if err != nil {
		return nil, err
	}
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	if err := f.SetRPCURL(chain, args[1]); err != nil {
		return nil, output.Errorf(output.CodeInvalidArgument, "%v", err)
	}
	if err := f.Save(); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "saving config")
	}
	shown := config.RedactRPCURL(args[1])
	return &result{data: map[string]any{"chain": chain, "url": shown, "source": string(onchain.RPCSourceConfig)},
		human: func(w io.Writer) { fmt.Fprintf(w, "set RPC for chain %d: %s\n", chain, shown) }}, nil
}

func (a *App) cmdRPCUnset(e *env) (*result, error) {
	args, err := e.positional(1, "rpc unset CHAIN_ID")
	if err != nil {
		return nil, err
	}
	chain, err := rpcChainArg(args[0])
	if err != nil {
		return nil, err
	}
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	if !f.UnsetRPCURL(chain) {
		return nil, output.Errorf(output.CodeNotFound, "no RPC configured for chain %d", chain)
	}
	if err := f.Save(); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "saving config")
	}
	return &result{data: map[string]any{"removed": chain},
		human: func(w io.Writer) { fmt.Fprintf(w, "removed RPC for chain %d\n", chain) }}, nil
}

func (a *App) cmdRPCList(e *env) (*result, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	rows, err := a.rpcEntries(f)
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "resolving RPC config")
	}
	return &result{data: map[string]any{"rpc": rows}, human: func(w io.Writer) {
		if len(rows) == 0 {
			fmt.Fprintln(w, "no RPC configured; run oku rpc set CHAIN_ID URL or set OKU_RPC_URL_<CHAINID>")
			return
		}
		tw := newTable(w)
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", strconv.FormatUint(r.Chain, 10), r.URL, r.Source)
		}
		tw.Flush()
	}}, nil
}
