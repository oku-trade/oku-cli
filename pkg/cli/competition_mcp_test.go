package cli

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oku-trade/oku-cli/pkg/mcp"
)

func TestCompetitionMCPDispatchAndMutationGate(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
		switch method {
		case "GetCompetition", "ListLeaderboard", "GetParticipant":
			return map[string]any{}, true
		case "StartRegistration":
			return map[string]any{"alreadyRegistered": map[string]any{}}, true
		default:
			return nil, false
		}
	}
	for _, allow := range []bool{false, true} {
		svc := testService(h)
		svc.AllowMutations(allow)
		cs := mcpSession(t, MCPBackend{S: svc}, allow)
		tools, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{}
		for _, tool := range tools.Tools {
			listed[tool.Name] = true
		}
		for _, tc := range []struct {
			path string
			args map[string]any
			mut  bool
			code string
		}{
			{path: "competition info"},
			{path: "competition leaderboard"},
			{path: "competition status"},
			{path: "competition claim prepare", code: "NOT_FOUND"},
			{path: "competition claim status", args: map[string]any{"intent": "missing"}, code: "NOT_FOUND"},
			{path: "competition register", mut: true},
			{path: "competition claim submit", args: map[string]any{"intent": "missing", "digest": "missing"}, mut: true, code: "NOT_FOUND"},
			{path: "competition claim reconcile", args: map[string]any{"intent": "missing", "tx-hash": claimTxHash}, mut: true, code: "NOT_FOUND"},
		} {
			name := mcp.ToolName(tc.path)
			if tc.mut && !allow {
				if listed[name] {
					t.Errorf("%s listed with mutations disabled", name)
				}
				if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: tc.args}); err == nil {
					t.Errorf("%s callable with mutations disabled", name)
				}
				if _, err := svc.Call(context.Background(), tc.path, Globals{}, tc.args, nil); err == nil || !strings.Contains(err.Error(), "mutation") {
					t.Errorf("service gate for %s: %v", name, err)
				}
				continue
			}
			if !listed[name] {
				t.Errorf("%s missing with mutations=%v", name, allow)
			}
			if tc.args == nil {
				tc.args = map[string]any{}
			}
			ok, env := callTool(t, cs, name, tc.args)
			failure, _ := env["error"].(map[string]any)
			if (tc.code == "" && (!ok || env["data"] == nil)) || (tc.code != "" && (ok || failure["code"] != tc.code)) {
				t.Errorf("%s did not reach its handler: %v", name, env)
			}
		}
		if err := cs.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
