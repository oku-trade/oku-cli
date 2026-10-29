package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	oku "github.com/oku-trade/oku-cli"
	"github.com/oku-trade/oku-cli/pkg/docs"
	"github.com/oku-trade/oku-cli/pkg/mcp"
)

func TestDocsCommandsOffline(t *testing.T) {
	h := newHarness(t)
	r := h.run("docs", "list", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"security"`) {
		t.Fatalf("docs list: %s", r.stdout)
	}
	if r = h.run("docs", "show", "security", "--json"); r.code != 0 || !strings.Contains(r.data()["content"].(string), "# Security") {
		t.Fatalf("docs show: %s", r.stdout)
	}
	if r = h.run("docs", "show", "../go.mod", "--json"); r.code == 0 {
		t.Fatal("path traversal accepted")
	}
	if r = h.run("docs", "search", "reduce-only", "--json"); r.code != 0 || len(r.data()["matches"].([]any)) == 0 {
		t.Fatalf("docs search: %s", r.stdout)
	}
	r = h.run("docs", "commands", "--markdown")
	want, _ := os.ReadFile("../../docs/commands.md")
	if r.code != 0 || r.stdout != string(want) {
		t.Fatal("docs/commands.md is stale relative to the command tree; run make docs")
	}
	if len(h.fake.calls) != 0 {
		t.Fatalf("docs contacted the network: %v", h.fake.calls)
	}
}

// Every command referenced by a bundled skill must exist in this binary.
func TestSkillsReferenceOnlySupportedCommands(t *testing.T) {
	cmds := (&App{}).commands()
	entries, err := os.ReadDir("../../skills")
	if err != nil || len(entries) != 9 {
		t.Fatalf("want 10 skills, got %d (%v)", len(entries), err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("../../skills", e.Name(), "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(b), "---\nname: "+e.Name()+"\ndescription: ") {
			t.Errorf("%s: bad frontmatter", e.Name())
		}
		for _, line := range strings.Split(string(b), "\n") {
			for _, seg := range strings.Split(line, "`") {
				f := strings.Fields(strings.TrimSpace(seg))
				if len(f) < 2 || f[0] != "oku" || strings.HasPrefix(f[1], "<") || strings.HasPrefix(f[1], "-") {
					continue
				}
				if _, ok := cmds[f[1]]; ok {
					continue
				}
				if len(f) >= 4 {
					if _, ok := cmds[f[1]+" "+f[2]+" "+f[3]]; ok {
						continue
					}
				}
				if len(f) >= 3 {
					if _, ok := cmds[f[1]+" "+f[2]]; ok {
						continue
					}
				}
				t.Errorf("%s references unsupported command %q", e.Name(), seg)
			}
		}
	}
}

func TestDraftsRepairIsLocalOnly(t *testing.T) {
	h := newHarness(t)
	h.stdin = "```json\n{market:'BTC', side:'buy', type:'limit', size:'0.01', price:'50000'}\n```"
	r := h.run("drafts", "repair", "--stdin", "--json")
	if r.code != 0 || r.data()["repaired"] != true {
		t.Fatalf("repair: %s %s", r.stdout, r.stderr)
	}
	h.stdin = `{"market":"BTC","side":"buy","type":"limit","size":"0.01"}`
	if r = h.run("drafts", "repair", "--stdin", "--json"); r.code == 0 {
		t.Fatal("missing price accepted")
	}
	if len(h.fake.calls) != 0 {
		t.Fatalf("drafts contacted the network: %v", h.fake.calls)
	}
	if _, err := os.Stat(os.Getenv("OKU_STATE_DIR")); err == nil {
		t.Fatal("drafts created state")
	}
}

func TestRiskPrepareValidatesAndSubmitsWithApprovedAgent(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	if r := h.run("positions", "leverage", "--market", "BTC", "--leverage", "41", "--mode", "cross", "--json"); r.errCode() != "INVALID_ARGUMENT" {
		t.Fatalf("over max: %s", r.stdout)
	}
	if r := h.run("positions", "margin", "--market", "BTC", "--amount", "1.0000001", "--json"); r.code == 0 {
		t.Fatal("7 decimals accepted")
	}
	r := h.run("positions", "leverage", "--market", "BTC", "--leverage", "5", "--mode", "isolated", "--json")
	if r.code != 0 || h.fake.creates != 0 {
		t.Fatalf("prepare: %s", r.stdout)
	}
	id, dg := r.data()["intent_id"].(string), r.data()["digest"].(string)
	h.fake.confirm = completeConfirm
	r = h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.code != 0 || h.fake.creates != 1 || h.fake.confirms != 1 {
		t.Fatalf("approved agent risk submission: %s", r.stdout)
	}
}

func mcpSession(t *testing.T, b mcp.Backend, allow bool) *sdk.ClientSession {
	t.Helper()
	srv := mcp.NewServer(b, mcp.Config{Version: "test", AllowMutations: allow, Docs: docs.Bundled(), Skills: oku.Skills()})
	ct, st := sdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (bool, map[string]any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var env map[string]any
	_ = json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &env)
	return !res.IsError, env
}

func testService(h *harness) *Service {
	return NewService("test", os.Stderr, func(k string) string {
		if k == "OKU_PRIVY_AUTH_URL" {
			return h.privy.server.URL
		}
		return ""
	})
}

func TestMCPReadsByDefaultAndGatesMutations(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	svc := testService(h)
	cs := mcpSession(t, MCPBackend{S: svc}, false)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, bad := range []string{"orders_submit", "orders_prepare", "auth_setup", "auth_revoke", "profiles_add", "skills_install", "mcp_serve"} {
		if names[bad] {
			t.Errorf("%s exposed without --allow-mutations", bad)
		}
	}
	if !names["markets_quote"] || !names["drafts_repair"] {
		t.Fatalf("read tools missing: %v", names)
	}
	ok, env := callTool(t, cs, "markets_quote", map[string]any{"market": "BTC"})
	if !ok || env["data"] == nil {
		t.Fatalf("quote: %v", env)
	}
	// The service itself refuses mutations, independent of tool listing.
	if _, err := svc.Call(context.Background(), "orders submit", Globals{}, map[string]any{"intent": "x", "digest": "y"}, nil); err == nil {
		t.Fatal("service ran a mutation while gated")
	}
	if _, err := svc.Call(context.Background(), "auth logout", Globals{}, nil, nil); err == nil {
		t.Fatal("service ran auth logout")
	}
	res, err := cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: "oku://skills/reconcile-hyperliquid-actions"})
	if err != nil || !strings.Contains(res.Contents[0].Text, "never") {
		t.Fatalf("skill resource: %v", err)
	}
	if _, err := cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: "oku://docs/security"}); err != nil {
		t.Fatal(err)
	}
}

func TestMCPPrepareSubmitSharesJournalAndTypes(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	svc := testService(h)
	svc.AllowMutations(true)
	cs := mcpSession(t, MCPBackend{S: svc}, true)
	if ok, _ := callTool(t, cs, "orders_prepare", map[string]any{"market": "BTC", "side": "buy", "type": "limit", "size": "0.001", "price": "40000", "slippage-bps": 1.5}); ok {
		t.Fatal("float accepted for integer option")
	}
	ok, env := callTool(t, cs, "orders_prepare", map[string]any{"market": "BTC", "side": "buy", "type": "limit", "size": "0.001", "price": "40000"})
	if !ok || h.fake.creates != 0 {
		t.Fatalf("prepare: %v", env)
	}
	d := env["data"].(map[string]any)
	// The CLI sees the intent the MCP tool prepared: one shared journal.
	if r := h.run("orders", "status", "--intent", d["intent_id"].(string), "--json"); r.code != 0 {
		t.Fatalf("cli status of mcp intent: %s", r.stdout)
	}
	ok, env = callTool(t, cs, "orders_submit", map[string]any{"intent": d["intent_id"], "digest": d["digest"]})
	if !ok || h.fake.confirms != 1 {
		t.Fatalf("submit: %v", env)
	}
	// Re-submitting is refused, never resent.
	if ok, _ = callTool(t, cs, "orders_submit", map[string]any{"intent": d["intent_id"], "digest": d["digest"]}); ok || h.fake.confirms != 1 {
		t.Fatal("resubmitted")
	}
}

func TestMCPPinnedProfile(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	other := "0x2222222222222222222222222222222222222222"
	if r := h.run("profiles", "add", "other", "--account", other, "--api-url", h.srv.URL, "--default", "--json"); r.code != 0 {
		t.Fatal(r.stdout)
	}
	svc := testService(h)
	cs := mcpSession(t, MCPBackend{S: svc, Launch: Globals{Profile: "main"}}, false)
	ok, env := callTool(t, cs, "auth_status", map[string]any{})
	if !ok || !strings.Contains(strings.ToLower(mustJSON(env)), acct) {
		t.Fatalf("pinned profile not used (default is 'other'): %v", env)
	}
	if ok, _ = callTool(t, cs, "auth_status", map[string]any{"profile": "other"}); ok {
		t.Fatal("pinned server switched profile")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// Spawns the real binary with the official SDK client over stdio.
func TestMCPServeBinaryStdio(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binary")
	}
	bin := filepath.Join(t.TempDir(), "oku")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/oku").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	cmd := exec.Command(bin, "mcp", "serve")
	cmd.Env = append(os.Environ(), "OKU_CONFIG_DIR="+t.TempDir(), "OKU_STATE_DIR="+t.TempDir())
	ctx := context.Background()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "version", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("version: %v %+v", err, res)
	}
	// Missing pairing is a structured tool error, never a prompt.
	res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "auth_status", Arguments: map[string]any{"profile": "nope"}})
	if err != nil || !res.IsError {
		t.Fatalf("auth_status: %v %+v", err, res)
	}
	rs, err := cs.ListResources(ctx, nil)
	if err != nil || len(rs.Resources) < 9 {
		t.Fatalf("resources: %v", err)
	}
}
