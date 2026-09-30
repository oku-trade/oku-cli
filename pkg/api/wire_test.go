package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	accountv2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
)

// fill sets every field (recursively, one element per list) to a non-zero
// value so the protojson encoding exercises every field's wire form.
func fill(m protoreflect.Message, depth int) {
	fs := m.Descriptor().Fields()
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.IsMap() || depth > 4 {
			continue
		}
		if f.ContainingOneof() != nil && !f.HasOptionalKeyword() && f.ContainingOneof().Fields().Get(0) != f {
			continue
		}
		if f.IsList() {
			l := m.Mutable(f).List()
			if f.Kind() == protoreflect.MessageKind {
				e := l.NewElement()
				fill(e.Message(), depth+1)
				l.Append(e)
			} else {
				l.Append(scalar(f))
			}
			continue
		}
		if f.Kind() == protoreflect.MessageKind {
			if f.Message().FullName() == "google.protobuf.Timestamp" {
				continue
			}
			fill(m.Mutable(f).Message(), depth+1)
			continue
		}
		m.Set(f, scalar(f))
	}
}

func scalar(f protoreflect.FieldDescriptor) protoreflect.Value {
	switch f.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(true)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString("7")
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte{7})
	case protoreflect.EnumKind:
		vs := f.Enum().Values()
		return protoreflect.ValueOfEnum(vs.Get(vs.Len() - 1).Number())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(7)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(1 << 40)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(7)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(7)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(1.5)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(1.5)
	}
	panic(f.Kind())
}

// TestResponsesDecodeIntoCompatTypes: every generated response the client
// can return, fully populated, must decode into the exported compatibility
// types without type errors (e.g. int64 string vs Go int).
func TestResponsesDecodeIntoCompatTypes(t *testing.T) {
	cases := []struct {
		msg proto.Message
		dst any
	}{
		{&accountv2.GetMarketsResponse{}, &GetMarketsResponse{}},
		{&accountv2.GetMarketPriceResponse{}, &GetMarketPriceResponse{}},
		{&accountv2.GetMarketFundingHistoryResponse{}, &GetMarketFundingHistoryResponse{}},
		{&accountv2.GetPerpPositionsResponse{}, &GetPerpPositionsResponse{}},
		{&accountv2.GetOpenOrdersResponse{}, &GetOpenOrdersResponse{}},
		{&accountv2.GetAccountBalancesResponse{}, &GetAccountBalancesResponse{}},
		{&accountv2.GetBuilderFeeStatusResponse{}, &GetBuilderFeeStatusResponse{}},
		{&accountv2.GetUserFillsResponse{}, &GetUserFillsResponse{}},
		{&accountv2.ActionPayloadResponse{}, &ActionPayloadResponse{}},
		{&accountv2.ConfirmActionResponse{}, &ConfirmActionResponse{}},
		{&accountv2.GetActionResponse{}, &GetActionResponse{}},
		{&accountv2.ReconcileActionsResponse{}, &ReconcileActionsResponse{}},
		{&accountv2.RedeemCliEnrollmentResponse{}, &RedeemEnrollmentResponse{}},
		{&accountv2.PollCliEnrollmentResponse{}, &PollEnrollmentResponse{}},
		{&accountv2.GetCurrentCliDeviceResponse{}, &DeviceResponse{}},
		{&accountv2.RevokeCurrentCliDeviceResponse{}, &DeviceResponse{}},
	}
	for _, c := range cases {
		fill(c.msg.ProtoReflect(), 0)
		raw, err := protojson.Marshal(c.msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, c.dst); err != nil {
			t.Errorf("%T -> %s: %v", c.msg, reflect.TypeOf(c.dst).Elem().Name(), err)
		}
	}
}
