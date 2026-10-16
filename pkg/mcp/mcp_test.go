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
