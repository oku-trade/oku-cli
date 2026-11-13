package api

import (
	"encoding/json"
	"testing"

	accountv2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Exercise every metadata field through the compatibility types in both
// directions, including int64 IDs, enum names and optional string presence.
func TestPerpsActionMetadataRoundTrip(t *testing.T) {
	oneof := (&accountv2.ActionParams{}).ProtoReflect().Descriptor().Oneofs().ByName("metadata")
	for i := 0; i < oneof.Fields().Len(); i++ {
		field := oneof.Fields().Get(i)
		t.Run(string(field.Name()), func(t *testing.T) {
			want := &accountv2.CreateActionRequest{Params: &accountv2.ActionParams{}}
			metadata := want.Params.ProtoReflect().Mutable(field).Message()
			fill(metadata, 0)
			raw, err := protojson.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var compat CreateActionRequest
			if err := json.Unmarshal(raw, &compat); err != nil {
				t.Fatal(err)
			}
			raw, err = json.Marshal(compat)
			if err != nil {
				t.Fatal(err)
			}
			var got accountv2.CreateActionRequest
			if err := strictJSON.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(want, &got) {
				t.Fatalf("metadata fields lost: want %v, got %v", want, &got)
			}
		})
	}
}

func TestModifyOptionalCloid(t *testing.T) {
	for _, value := range []*string{nil, proto.String("")} {
		params := ActionParams{Modify: &ModifyActionMetadata{Modifies: []ModifyOrderMetadata{{Cloid: value}}}}
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		var got accountv2.ActionParams
		if err := strictJSON.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		item := got.GetModify().GetModifies()[0]
		if (item.Cloid == nil) != (value == nil) {
			t.Fatalf("optional cloid presence changed: %s", raw)
		}
	}
}

func TestTriggerOrderTypeWireNames(t *testing.T) {
	for _, value := range []TriggerOrderType{TriggerOrderTypeUnspecified, TriggerOrderTypeTakeProfit, TriggerOrderTypeStopLoss} {
		raw, err := json.Marshal(ModifyOrderMetadata{Tpsl: value})
		if err != nil {
			t.Fatal(err)
		}
		var got accountv2.ModifyOrderMetadata
		if err := strictJSON.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if protoreflect.Name(value) != got.Tpsl.Descriptor().Values().ByNumber(got.Tpsl.Number()).Name() {
			t.Fatalf("wrong enum name: %s", raw)
		}
	}
}
