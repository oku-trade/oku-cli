package api

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	accountv2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
)

// TestWireMatchesGeneratedProto round-trips handwritten request bodies
// through the vendored generated types with strict protojson, so a renamed
// or retyped field in perps.proto fails here instead of at the server.
func TestWireMatchesGeneratedProto(t *testing.T) {
	strict := protojson.UnmarshalOptions{DiscardUnknown: false}
	reqs := []any{
		CreateActionRequest{Address: "0x0000000000000000000000000000000000000001", Params: ActionParams{Leverage: &LeverageActionMetadata{Coin: "BTC", Leverage: 5, IsCross: true}}},
		CreateActionRequest{Address: "0x0000000000000000000000000000000000000001", Params: ActionParams{Margin: &MarginActionMetadata{Coin: "BTC", Amount: "-1.5"}}},
		CreateActionRequest{Params: ActionParams{Order: &OrderActionMetadata{Coin: "BTC", Side: "buy", OrderType: "limit", Sz: "1", LimitPx: "2", Tif: "Gtc", ReduceOnly: true, SlippageBps: 5}}},
		CreateActionRequest{Params: ActionParams{Cancel: &CancelActionMetadata{Coin: "BTC", Oid: 7}}},
	}
	for _, r := range reqs {
		raw, _ := json.Marshal(r)
		var m accountv2.CreateActionRequest
		if err := strict.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	raw, _ := json.Marshal(ConfirmActionRequest{ActionID: "a", SignedAction: SignedAction{ActionJSON: `{"b":1,"a":2}`, Nonce: 9, Signature: EVMSignature{R: "0x1", S: "0x2", V: 27}}})
	var c accountv2.ConfirmActionRequest
	if err := strict.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	// actionJson is an opaque string: key order must survive untouched.
	if c.GetSignedAction().GetActionJson() != `{"b":1,"a":2}` {
		t.Fatalf("actionJson altered: %q", c.GetSignedAction().GetActionJson())
	}
	// Response side: generated protojson output decodes into handwritten types.
	out, _ := protojson.Marshal(&accountv2.ActionPayloadResponse{ActionId: "x", ActionType: "leverage", ActionJson: `{"type":"updateLeverage"}`, AssetId: 3})
	var p ActionPayloadResponse
	if err := json.Unmarshal(out, &p); err != nil || p.ActionID != "x" || p.AssetID != 3 || p.ActionJSON != `{"type":"updateLeverage"}` {
		t.Fatalf("decode %s: %+v %v", out, p, err)
	}
}
