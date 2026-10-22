package cli

import (
	"strings"
	"testing"
)

func TestTokenManifest(t *testing.T) {
	const a = "0xAa00000000000000000000000000000000000001"
	rows, err := parseTokenManifest(strings.NewReader("chain_id,token_address,amount,extra\n8453," + a + ",1.5,x\n8453," + strings.ToLower(a) + ",2,y\n42161," + a + ",3,z\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[8453]) != 1 || len(rows[42161]) != 1 || rows[8453][0].Amount != "1.5" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
	for _, input := range []string{"8453,nope,1\n", "oops," + a + ",1\n", "8453\n"} {
		if _, err := parseTokenManifest(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestNativeSymbolFromChainConfig(t *testing.T) {
	for chain, want := range map[uint64]string{137: "POL", 8453: "ETH", 43114: "AVAX"} {
		if got := nativeSymbol(chain); got != want {
			t.Errorf("chain %d: got %s want %s", chain, got, want)
		}
	}
}
