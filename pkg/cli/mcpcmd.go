package cli

import (
	"context"
	"os"
	"os/signal"

	oku "github.com/oku-trade/oku-cli"
	"github.com/oku-trade/oku-cli/pkg/docs"
	"github.com/oku-trade/oku-cli/pkg/mcp"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func mcpFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.BoolFlag{Name: "allow-mutations", Usage: "expose prepare/submit/cancel/reconcile tools (requires Oku session)"},
	}
}

// MCPBackend adapts Service to the MCP backend interface. Launch holds the
// globals `oku mcp serve` was started with. When Launch.Profile is set the
// server is pinned to that profile: tools inherit it and a tool asking for a
// different profile is refused, so an agent cannot silently switch accounts.
type MCPBackend struct {
	S      *Service
	Launch Globals
}

// Commands implements mcp.Backend.
func (b MCPBackend) Commands() []mcp.Command {
	out := []mcp.Command{}
	for path, h := range b.S.app.commands() {
		if !b.S.allowed(path) {
			continue
		}
		c := mcp.Command{Path: path, Summary: h.summary}
		if h.flags != nil {
			for _, f := range h.flags() {
				o := mcp.Option{Name: f.Names()[0]}
				switch t := f.(type) {
				case *urfave.StringFlag:
					o.Type, o.Usage = "string", t.Usage
				case *urfave.BoolFlag:
					o.Type, o.Usage = "boolean", t.Usage
				case *urfave.IntFlag:
					o.Type, o.Usage = "integer", t.Usage
				case *urfave.Int64Flag:
					o.Type, o.Usage = "integer", t.Usage
				case *urfave.DurationFlag:
					o.Type, o.Usage = "duration", t.Usage
				default:
					continue
				}
				c.Options = append(c.Options, o)
			}
		}
		out = append(out, c)
	}
	return out
}

// Call implements mcp.Backend.
func (b MCPBackend) Call(ctx context.Context, path string, g mcp.Globals, opts map[string]any, args []string) (any, error) {
	eff := Globals{Profile: g.Profile, Account: g.Account, Network: b.Launch.Network, Timeout: b.Launch.Timeout}
	if g.Timeout != 0 {
		eff.Timeout = g.Timeout
	}
	if b.Launch.Profile != "" {
		if g.Profile != "" && g.Profile != b.Launch.Profile {
			return nil, output.Errorf(output.CodeUsage, "this MCP server is pinned to profile %q; profile %q was requested", b.Launch.Profile, g.Profile).
				WithDetail("hint", "start a separate 'oku --profile NAME mcp serve' for another profile")
		}
		eff.Profile = b.Launch.Profile
	}
	if eff.Account == "" {
		eff.Account = b.Launch.Account
	}
	return b.S.Call(ctx, path, eff, opts, args)
}

// cmdMCPServe runs the stdio MCP server. The process stdout is reserved for
// protocol frames, so this command writes nothing else there.
func (a *App) cmdMCPServe(e *env) (*result, error) {
	if len(e.args) != 0 {
		return nil, output.Errorf(output.CodeUsage, "unexpected arguments %v", e.args)
	}
	if e.g.json {
		return nil, output.Errorf(output.CodeUsage, "--json is not supported by mcp serve; stdout is the MCP protocol stream")
	}
	svc := NewService(a.Version, a.Stderr, a.Getenv)
	svc.AllowMutations(e.cmd.Bool("allow-mutations"))
	launch := Globals{Profile: e.g.profile, Network: e.g.network, Account: e.g.account, Timeout: e.g.timeout}
	srv := mcp.NewServer(MCPBackend{S: svc, Launch: launch}, mcp.Config{
		Version: a.Version, AllowMutations: e.cmd.Bool("allow-mutations"),
		Docs: docs.Bundled(), Skills: oku.Skills(),
	})
	ctx, stop := signal.NotifyContext(e.ctx, os.Interrupt)
	defer stop()
	e.diag("oku mcp: serving on stdio (profile %q, mutations %v)", launch.Profile, e.cmd.Bool("allow-mutations"))
	if err := mcp.Serve(ctx, srv, a.Stdin, a.Stdout); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "mcp serve")
	}
	return &result{}, nil
}
