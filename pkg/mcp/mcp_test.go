package mcp

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type schemaBackend struct{}

func (schemaBackend) Commands() []Command {
	schema := map[string]any{"$id": "https://oku.trade/schemas/oku.cli/v1/orders-prepare.input.json", "type": "object"}
	// "orders prepare" is a prepare-style command that is not exposed as a tool without --allow-mutations.
	return []Command{{Path: "orders prepare", Schema: schema}}
}

func (schemaBackend) Call(context.Context, string, Globals, map[string]any, []string) (any, error) {
	return nil, nil
}

// An agent drafts --input JSON for commands it hands to the user, so input
// schemas must be readable even when the command's tool is not exposed.
func TestSchemaResourcesPublishedWithoutMutationTools(t *testing.T) {
	ctx := context.Background()
	server := NewServer(schemaBackend{}, Config{Version: "test"})
	st, ct := sdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.ReadResource(ctx, &sdk.ReadResourceParams{URI: "oku://schemas/orders-prepare"})
	if err != nil || len(res.Contents) != 1 {
		t.Fatalf("read schema: %v %+v", err, res)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &got); err != nil || got["$id"] != "https://oku.trade/schemas/oku.cli/v1/orders-prepare.input.json" {
		t.Fatalf("schema = %s (%v)", res.Contents[0].Text, err)
	}
}

type pathBackend struct{ paths []string }

func (b pathBackend) Commands() []Command {
	var out []Command
	for _, p := range b.paths {
		out = append(out, Command{Path: p, Summary: p})
	}
	return out
}
func (pathBackend) Call(context.Context, string, Globals, map[string]any, []string) (any, error) {
	return map[string]any{}, nil
}

// Agent-wallet funds are usable over MCP: reads and journal-only prepares by
// default, and Privy-signing submit/reconcile/claim only with mutations on.
func TestFundsToolsExposedAndGated(t *testing.T) {
	paths := []string{"wallet balances", "swap quote", "swap prepare", "swap status", "swap submit", "bridge quote", "bridge prepare", "bridge status", "bridge submit", "bridge claim", "bridge reconcile", "send prepare", "send submit", "deposit prepare", "deposit submit", "swap orders list", "swap orders create", "swap orders cancel", "swap orders watch", "swap pnl", "auth login"}
	list := func(allow bool) map[string]*sdk.Tool {
		ctx := context.Background()
		server := NewServer(pathBackend{paths}, Config{Version: "test", AllowMutations: allow})
		st, ct := sdk.NewInMemoryTransports()
		if _, err := server.Connect(ctx, st, nil); err != nil {
			t.Fatal(err)
		}
		session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		res, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]*sdk.Tool{}
		for _, tl := range res.Tools {
			out[tl.Name] = tl
		}
		return out
	}
	reads := []string{"wallet_balances", "swap_quote", "swap_prepare", "swap_status", "bridge_quote", "bridge_prepare", "bridge_status", "send_prepare", "deposit_prepare", "swap_orders_list", "swap_pnl"}
	mutations := []string{"swap_submit", "bridge_submit", "bridge_claim", "bridge_reconcile", "send_submit", "deposit_submit", "swap_orders_create", "swap_orders_cancel", "swap_orders_watch"}
	gated := list(false)
	for _, n := range reads {
		if gated[n] == nil {
			t.Errorf("%s missing by default", n)
		}
	}
	for _, n := range append(mutations, "auth_login") {
		if gated[n] != nil {
			t.Errorf("%s exposed without mutations", n)
		}
	}
	open := list(true)
	for _, n := range mutations {
		tl := open[n]
		if tl == nil || tl.Annotations == nil || tl.Annotations.ReadOnlyHint {
			t.Errorf("%s must be listed as a mutation with mutations on", n)
		}
	}
	if open["auth_login"] != nil {
		t.Error("auth login must never be a tool")
	}
}
