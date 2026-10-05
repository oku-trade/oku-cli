package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

// Service exposes the command handlers in-process so other front ends (the
// MCP server) share the exact validation, authentication, signing and journal
// code paths of the executable instead of shelling out to it. A Service never
// prompts: it has no terminal, so commands needing a passphrase or pairing
// code fail with a structured auth error.
type Service struct {
	// mu serializes calls: handlers share one App and a journal per call,
	// and financial commands must not interleave within a server.
	mu  sync.Mutex
	app *App
	// allowMutations enables ServiceMutations paths. Off by default.
	allowMutations bool
}

// ServiceReads are commands a Service runs by default. They never sign,
// submit or change user configuration.
var ServiceReads = map[string]bool{
	"version": true, "doctor": true, "auth status": true, "profiles list": true,
	"markets list": true, "markets quote": true, "markets funding": true,
	"account summary": true, "positions list": true, "orders list": true,
	"orders status": true, "fills list": true,
	"docs list": true, "docs show": true, "docs search": true, "docs commands": true,
}

// ServiceMutations are journal/exchange mutations a Service runs only when
// constructed with AllowMutations. Device scopes are still enforced.
// Pairing, revocation, profile, skills, migration and server commands are
// never reachable through a Service.
var ServiceMutations = map[string]bool{
	"orders prepare": true, "positions leverage": true, "positions margin": true,
	"orders submit": true, "orders cancel": true, "orders reconcile": true, "orders abandon": true,
}

// AllowMutations enables the ServiceMutations commands.
func (s *Service) AllowMutations(on bool) { s.allowMutations = on }

// Globals mirrors the executable's global options.
type Globals struct {
	Profile string
	Network string
	Account string
	Timeout time.Duration
}

// NewService returns a non-interactive service. Diagnostics go to stderr;
// nothing is ever written to stdout.
func NewService(version string, stderr io.Writer, getenv func(string) string) *Service {
	a := NewApp(version)
	a.Stdout = io.Discard
	a.Stderr = stderr
	a.Stdin = strings.NewReader("")
	a.StdinIsTTY = false
	a.ReadSecret = func(string) ([]byte, error) { return nil, errNoTTY }
	if getenv != nil {
		a.Getenv = getenv
	}
	return &Service{app: a}
}

// Commands returns the supported command paths and their summaries.
func (s *Service) Commands() map[string]string {
	out := map[string]string{}
	for k, h := range s.app.commands() {
		if !s.allowed(k) {
			continue
		}
		out[k] = h.summary
	}
	return out
}

// Call runs the command at path (e.g. "orders prepare") with typed flag
// values and positional args. It returns the same data the --json envelope
// carries, or an *output.Error.
func (s *Service) Call(ctx context.Context, path string, g Globals, flags map[string]any, args []string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.app.commands()[path]
	if ok && !s.allowed(path) {
		if ServiceMutations[path] {
			return nil, output.Errorf(output.CodeUsage, "%q is a mutation and this server was not started with mutations enabled", path)
		}
		return nil, output.Errorf(output.CodeUsage, "%q is not available in-process; run it interactively with the oku CLI", path)
	}
	if !ok {
		return nil, output.Errorf(output.CodeUsage, "unknown command %q", path)
	}
	src, err := newMapFlags(h, flags)
	if err != nil {
		return nil, err
	}
	if g.Timeout == 0 {
		g.Timeout = defaultTimeout
	}
	if g.Timeout < 0 || g.Timeout > maxTimeout {
		return nil, output.Errorf(output.CodeUsage, "timeout must be between 1ns and %s", maxTimeout)
	}
	if g.Network != "" && g.Network != "mainnet" {
		return nil, output.Errorf(output.CodeNetworkUnsupported, "network %q is not supported: the Oku API currently serves Hyperliquid mainnet only", g.Network)
	}
	if args == nil {
		args = []string{}
	}
	var declared []urfave.Flag
	if h.flags != nil {
		declared = h.flags()
	}
	e := &env{app: s.app, cmd: newCheckedFlags(src, declared), args: args, ctx: ctx,
		g: globals{profile: g.Profile, network: g.Network, account: g.Account, json: true, timeout: g.Timeout}}
	res, err := runChecked(h, e)
	if err != nil {
		return nil, output.As(err)
	}
	return res.data, nil
}

func (s *Service) allowed(path string) bool {
	return ServiceReads[path] || (s.allowMutations && ServiceMutations[path])
}

// mapFlags implements flagSource from values already validated against the
// declared flag types. Accessors never coerce: Call rejects wrong types.
type mapFlags map[string]any

// newMapFlags checks every given value against its declared flag type.
// Strings must be string; booleans bool; integers an exact int, int64 or
// json.Number integer (no floats, no silent truncation, int range checked);
// durations a time.Duration or a Go duration string.
func newMapFlags(h *handler, given map[string]any) (mapFlags, error) {
	m := mapFlags{}
	declared := map[string]urfave.Flag{}
	if h.flags != nil {
		for _, f := range h.flags() {
			name := f.Names()[0]
			declared[name] = f
			switch t := f.(type) {
			case *urfave.StringFlag:
				m[name] = t.Value
			case *urfave.BoolFlag:
				m[name] = t.Value
			case *urfave.IntFlag:
				m[name] = int64(t.Value)
			case *urfave.Int64Flag:
				m[name] = t.Value
			case *urfave.DurationFlag:
				m[name] = t.Value
			}
		}
	}
	for k, v := range given {
		f, ok := declared[k]
		if !ok {
			return nil, output.Errorf(output.CodeUsage, "unknown option %q", k)
		}
		cv, err := checkType(f, v)
		if err != nil {
			return nil, output.Errorf(output.CodeUsage, "option %q: %v", k, err)
		}
		m[k] = cv
	}
	return m, nil
}

func checkType(f urfave.Flag, v any) (any, error) {
	switch f.(type) {
	case *urfave.StringFlag:
		if s, ok := v.(string); ok {
			return s, nil
		}
		return nil, fmt.Errorf("must be a string")
	case *urfave.BoolFlag:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, fmt.Errorf("must be a boolean")
	case *urfave.IntFlag, *urfave.Int64Flag:
		var n int64
		switch x := v.(type) {
		case int:
			n = int64(x)
		case int64:
			n = x
		case json.Number:
			i, err := strconv.ParseInt(string(x), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("must be an integer")
			}
			n = i
		default:
			return nil, fmt.Errorf("must be an integer")
		}
		if _, isInt := f.(*urfave.IntFlag); isInt && (n > math.MaxInt32 || n < math.MinInt32) {
			return nil, fmt.Errorf("out of range")
		}
		return n, nil
	case *urfave.DurationFlag:
		switch x := v.(type) {
		case time.Duration:
			return x, nil
		case string:
			d, err := time.ParseDuration(x)
			if err != nil {
				return nil, fmt.Errorf("must be a duration like 5m")
			}
			return d, nil
		}
		return nil, fmt.Errorf("must be a duration string")
	}
	return nil, fmt.Errorf("unsupported option type")
}

func (m mapFlags) String(n string) string {
	switch v := m[n].(type) {
	case string:
		return v
	}
	return ""
}

func (m mapFlags) Bool(n string) bool { b, _ := m[n].(bool); return b }
func (m mapFlags) Int(n string) int   { i, _ := m[n].(int64); return int(i) }
func (m mapFlags) Int64(n string) int64 {
	i, _ := m[n].(int64)
	return i
}
func (m mapFlags) Duration(n string) time.Duration { d, _ := m[n].(time.Duration); return d }
