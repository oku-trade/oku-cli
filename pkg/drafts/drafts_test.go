package drafts

import (
	"strings"
	"testing"
)

func TestRepairAcceptsSyntaxOnlyFixes(t *testing.T) {
	c, err := Repair(strings.NewReader("```json\n{market: 'BTC', side: 'buy', type: 'limit', size: '0.01', price: 60000,}\n```"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Repaired || c.Order.Market != "BTC" || c.Order.Size != "0.01" || c.Order.Price != "60000" {
		t.Fatalf("%+v", c)
	}
	want := "orders prepare --market BTC --side buy --type limit --size 0.01 --price 60000"
	if strings.Join(c.PrepareArgs, " ") != want {
		t.Fatalf("args %v", c.PrepareArgs)
	}
	c, err = Repair(strings.NewReader(`{"market":"ETH","side":"sell","type":"market","size":"1","slippage_bps":50,"reduce_only":true}`))
	if err != nil || c.Repaired {
		t.Fatalf("valid draft: %+v %v", c, err)
	}
}

func TestRepairRefusesAmbiguousOrUnsafeDrafts(t *testing.T) {
	cases := map[string]string{
		"duplicate key":    `{"market":"BTC","side":"buy","side":"sell","type":"limit","size":"1","price":"1"}`,
		"unknown field":    `{"market":"BTC","side":"buy","type":"limit","size":"1","price":"1","leverage":50}`,
		"missing size":     `{"market":"BTC","side":"buy","type":"limit","price":"1"}`,
		"missing price":    `{"market":"BTC","side":"buy","type":"limit","size":"1"}`,
		"missing slippage": `{"market":"BTC","side":"buy","type":"market","size":"1"}`,
		"truncated amount": `{"market":"BTC","side":"buy","type":"limit","price":"60000","size":"0.05`,
		"truncated price":  `{"market":"BTC","side":"buy","type":"limit","size":"1","price":600`,
		"scientific":       `{"market":"BTC","side":"buy","type":"limit","size":1e3,"price":"1"}`,
		"negative":         `{"market":"BTC","side":"buy","type":"limit","size":"-1","price":"1"}`,
		"non canonical":    `{"market":"BTC","side":"buy","type":"limit","size":"01.50","price":"1"}`,
		"array":            `[{"market":"BTC"}]`,
		"too deep":         `{"market":{"a":{"b":{"c":{"d":1}}}}}`,
		"two values":       `{"market":"BTC","side":"buy","type":"limit","size":"1","price":"1"} {}`,
		"empty":            "  ",
		"bad utf8":         "{\"market\":\"\xff\"}",
	}
	for name, in := range cases {
		if c, err := Repair(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted %+v", name, c)
		}
	}
	if _, err := Repair(strings.NewReader(`{"market":"` + strings.Repeat("A", MaxBytes) + `"}`)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("size limit: %v", err)
	}
}
