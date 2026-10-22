package cli

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestHoldingsRequestUsesLegacySingleChainWireField(t *testing.T) {
	for _, chain := range []string{"eip155:42161", "eip155:8453"} {
		request := holdingsRequest("0x1111111111111111111111111111111111111111", chain)
		wire, err := proto.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		// Field 2 is the deployed backend's singular chain; field 3 was added later.
		if !bytes.Contains(wire, append([]byte{0x12, byte(len(chain))}, []byte(chain)...)) || len(request.Chains) != 0 {
			t.Fatalf("chain %s not encoded on legacy field 2: %x", chain, wire)
		}
	}
}
