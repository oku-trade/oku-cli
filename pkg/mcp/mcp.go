// Package mcp exposes Oku over the Model Context Protocol (stdio) using the
// official Go SDK. Tools call the same in-process services as the CLI; the
// server never shells out to the oku binary. stdout carries only protocol
// frames; diagnostics go to the configured log writer (stderr).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oku-trade/oku-cli/pkg/docs"
	"github.com/oku-trade/oku-cli/pkg/drafts"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// Option describes one command option for schema generation.
type Option struct {
	Name, Type, Usage string // Type: string | boolean | integer | duration
}

// Command is one callable command of the shared service.
type Command struct {
	Path    string
	Summary string
	Options []Option
	Schema  map[string]any
}

// Globals mirrors the CLI global options accepted by every tool.
type Globals struct {
	Profile string
	Account string
	Timeout time.Duration
}

// Backend is the shared application service (implemented by cli.Service).
type Backend interface {
	Commands() []Command
	Call(ctx context.Context, path string, g Globals, opts map[string]any, args []string) (any, error)
}

// Config controls what the server exposes.
type Config struct {
	Version string
	// AllowMutations exposes prepare/submit/cancel/reconcile tools.
	// Authenticated mutations still require a valid Oku session.
	AllowMutations bool
	Docs           docs.Store
	Skills         fs.FS // skills root, one directory per skill
}

// Tool policy. Read tools are exposed by default; mutation tools only with
// AllowMutations. Anything not listed is never exposed (auth login needs a
// human-operated authorization; profile edits change user state).
var (
	readTools = map[string]bool{
		"version": true, "doctor": true, "auth status": true, "profiles list": true,
		"markets list": true, "markets quote": true, "markets funding": true,
		"account summary": true, "positions list": true, "orders list": true,
		"orders status": true, "fills list": true,
	}
	// docs are served as resources; tool exposure of docs commands is
	// intentionally omitted.
	mutationTools = map[string]bool{
		"orders prepare": true, "positions leverage": true, "positions margin": true,
		"orders submit": true, "orders cancel": true, "orders reconcile": true, "orders abandon": true,
	}
	// positional names commands take as arguments instead of options.
	positional = map[string][]Option{
		"markets quote":   {{Name: "market", Type: "string", Usage: "market, e.g. BTC (required)"}},
		"markets funding": {{Name: "market", Type: "string", Usage: "market, e.g. BTC (required)"}},
	}
)

// ToolName maps a command path to a tool name, e.g. "orders prepare" ->
// "orders_prepare".
func ToolName(path string) string { return strings.ReplaceAll(path, " ", "_") }

// NewServer builds the MCP server.
func NewServer(b Backend, cfg Config) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "oku", Version: cfg.Version}, &sdk.ServerOptions{
		Instructions: "Oku Agent for Hyperliquid mainnet perps. Read the oku://docs and oku://skills resources first. " +
			"Never invent trade parameters. Mutations use prepare, then an explicit user-approved submit bound to the returned digest. " +
			"An unknown outcome is not a failure: reconcile, never resend.",
	})
	cmds := b.Commands()
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Path < cmds[j].Path })
	for _, c := range cmds {
		mut := mutationTools[c.Path]
		if !readTools[c.Path] && !(mut && cfg.AllowMutations) {
			continue
		}
		addCommandTool(s, b, c, mut)
		if c.Schema != nil {
			uri := "oku://schemas/" + strings.ReplaceAll(c.Path, " ", "-")
			data, _ := json.MarshalIndent(c.Schema, "", "  ")
			s.AddResource(&sdk.Resource{URI: uri, Name: "schemas/" + c.Path, MIMEType: "application/schema+json"}, func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
				return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: "application/schema+json", Text: string(data)}}}, nil
			})
		}
	}
	addDraftTool(s)
	addDocs(s, cfg.Docs)
	addSkills(s, cfg.Skills)
	return s
}

func boolp(b bool) *bool { return &b }

func addCommandTool(s *sdk.Server, b Backend, c Command, mutation bool) {
	props := map[string]any{
		"profile": map[string]any{"type": "string", "description": "profile name (default: OKU_PROFILE or config default)"},
	}
	if !mutation {
		props["account"] = map[string]any{"type": "string", "description": "account address for reads (default: profile account)"}
	}
	var required []string
	pos := positional[c.Path]
	for _, o := range append(append([]Option{}, pos...), c.Options...) {
		t := o.Type
		desc := o.Usage
		if t == "duration" {
			t, desc = "string", desc+" (Go duration, e.g. 5m)"
		}
		props[o.Name] = map[string]any{"type": t, "description": desc}
		if c.Schema == nil && strings.Contains(o.Usage, "(required)") {
			required = append(required, o.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if c.Schema != nil {
		for k, v := range c.Schema["properties"].(map[string]any) {
			props[k] = v
		}
		if names, ok := c.Schema["required"].([]string); ok {
			required = append(required, names...)
		}
		for _, p := range pos {
			if strings.Contains(p.Usage, "(required)") {
				required = append(required, p.Name)
			}
		}
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	ann := &sdk.ToolAnnotations{ReadOnlyHint: !mutation, OpenWorldHint: boolp(true)}
	if mutation {
		ann.DestructiveHint = boolp(c.Path != "orders prepare" && !strings.HasPrefix(c.Path, "positions "))
	}
	desc := c.Summary
	if mutation {
		desc += ". MUTATION: acts on the user's account or local journal; only call when the user explicitly asked."
	}
	s.AddTool(&sdk.Tool{Name: ToolName(c.Path), Description: desc, InputSchema: schema, Annotations: ann},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			args := map[string]any{}
			if len(req.Params.Arguments) > 0 {
				var err error
				if args, err = decodeArgs(req.Params.Arguments); err != nil {
					return errResult(output.Errorf(output.CodeUsage, "arguments: %v", err)), nil
				}
			}
			g := Globals{}
			var err error
			if g.Profile, err = take[string](args, "profile"); err == nil && !mutation {
				g.Account, err = take[string](args, "account")
			}
			var positionalArgs []string
			for _, o := range pos {
				if err != nil {
					break
				}
				var v string
				if v, err = take[string](args, o.Name); err == nil && v != "" {
					positionalArgs = append(positionalArgs, v)
				}
			}
			if err != nil {
				return errResult(output.Errorf(output.CodeUsage, "%v", err)), nil
			}
			opts := map[string]any{}
			types := map[string]string{}
			for _, o := range c.Options {
				types[o.Name] = o.Type
			}
			for k, v := range args {
				t, ok := types[k]
				if !ok {
					return errResult(output.Errorf(output.CodeUsage, "unknown argument %q", k)), nil
				}
				cv, cerr := convert(t, v)
				if cerr != nil {
					return errResult(output.Errorf(output.CodeUsage, "%s: %v", k, cerr)), nil
				}
				opts[k] = cv
			}
			data, err := b.Call(ctx, c.Path, g, opts, positionalArgs)
			if err != nil {
				return errResult(err), nil
			}
			return okResult(data), nil
		})
}

// decodeArgs strictly decodes a flat JSON object of scalar arguments. It
// rejects duplicate keys, nested values and trailing data so a financial
// argument can never be ambiguous. Protocol input is never repaired.
func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("must be a JSON object")
	}
	out := map[string]any{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k := kt.(string)
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("duplicate key %q", k)
		}
		vt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if _, nested := vt.(json.Delim); nested || vt == nil {
			return nil, fmt.Errorf("%s must be a scalar", k)
		}
		out[k] = vt
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after arguments")
	}
	return out, nil
}

func take[T any](m map[string]any, k string) (T, error) {
	var zero T
	v, ok := m[k]
	if !ok {
		return zero, nil
	}
	delete(m, k)
	t, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("%s has the wrong type", k)
	}
	return t, nil
}

func convert(typ string, v any) (any, error) {
	switch typ {
	case "string":
		if s, ok := v.(string); ok {
			return s, nil
		}
	case "boolean":
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case "integer":
		if n, ok := v.(json.Number); ok {
			i, err := n.Int64()
			if err != nil {
				return nil, fmt.Errorf("must be an integer")
			}
			return i, nil
		}
	case "duration":
		if s, ok := v.(string); ok {
			d, err := time.ParseDuration(s)
			if err != nil {
				return nil, err
			}
			return d, nil
		}
	}
	return nil, fmt.Errorf("must be a %s", typ)
}

type envelope struct {
	OK    bool          `json:"ok"`
	Data  any           `json:"data,omitempty"`
	Error *output.Error `json:"error,omitempty"`
}

func okResult(data any) *sdk.CallToolResult {
	env := envelope{OK: true, Data: data}
	b, _ := json.Marshal(env)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}, StructuredContent: env}
}

func errResult(err error) *sdk.CallToolResult {
	env := envelope{Error: output.As(err)}
	b, _ := json.Marshal(env)
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}, StructuredContent: env}
}

// DraftInput is the drafts_repair tool input.
type DraftInput struct {
	Draft string `json:"draft" jsonschema:"malformed order draft JSON text, at most 16 KiB"`
}

func addDraftTool(s *sdk.Server) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "drafts_repair",
		Description: "Repair malformed order-draft JSON syntax into a reviewable candidate. Local only: never prepares, signs or submits, and never infers missing financial values.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolp(false)},
	}, func(_ context.Context, _ *sdk.CallToolRequest, in DraftInput) (*sdk.CallToolResult, any, error) {
		c, err := drafts.Repair(strings.NewReader(in.Draft))
		if err != nil {
			return errResult(output.Wrap(output.CodeInvalidArgument, err, "draft refused")), nil, nil
		}
		return okResult(map[string]any{"repaired": c.Repaired, "candidate": c.Order, "json": c.JSON, "prepare_args": c.PrepareArgs, "input": c.Input}), nil, nil
	})
}

func addDocs(s *sdk.Server, st docs.Store) {
	if st.FS == nil {
		return
	}
	list, err := st.List()
	if err != nil {
		return
	}
	for _, d := range list {
		name := d.Name
		uri := "oku://docs/" + name
		s.AddResource(&sdk.Resource{URI: uri, Name: "docs/" + name, Title: d.Title, MIMEType: "text/markdown"},
			func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
				b, err := st.Show(name)
				if err != nil {
					return nil, err
				}
				return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: "text/markdown", Text: string(b)}}}, nil
			})
	}
}

func addSkills(s *sdk.Server, root fs.FS) {
	if root == nil {
		return
	}
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		b, err := fs.ReadFile(root, name+"/SKILL.md")
		if err != nil {
			continue
		}
		uri := "oku://skills/" + name
		text := string(b)
		s.AddResource(&sdk.Resource{URI: uri, Name: "skills/" + name, Description: "Oku task skill: " + name, MIMEType: "text/markdown"},
			func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
				return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: "text/markdown", Text: text}}}, nil
			})
	}
}

// Serve runs the server over the given stdio streams until the client
// disconnects or ctx is cancelled.
func Serve(ctx context.Context, s *sdk.Server, in io.Reader, out io.Writer) error {
	err := s.Run(ctx, &sdk.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}})
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
