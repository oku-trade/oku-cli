package trading

import (
	"errors"
	"strings"
	"testing"
)

const builder = "0xd637f2A36c1a3b37d57ef4C7022cB183D8922f2c"

func limitIntent() *Intent {
	return &Intent{
		Version: 1, ID: "t1", Kind: KindOrder, Network: "mainnet",
		Account: "0x1111111111111111111111111111111111111111", Market: "BTC", AssetID: 0,
		Side: "buy", OrderType: "limit", Size: "0.001", Price: "50000", TIF: "GTC",
		BuilderAddress: builder, MaxBuilderFee10: 10,
	}
}

// Shape of actions built by account/src/temporal/workflows/perps/activity_build.go.
const goodLimit = `{"type":"order","orders":[{"a":0,"b":true,"p":"50000","s":"0.001","r":false,"t":{"limit":{"tif":"Gtc"}},"c":"0x0123456789abcdef0123456789abcdef"}],"grouping":"na","builder":{"b":"0xd637f2a36c1a3b37d57ef4c7022cb183d8922f2c","f":10}}`

func TestValidateActionAcceptsMatchingOrder(t *testing.T) {
	cloid, err := ValidateAction(limitIntent(), goodLimit)
	if err != nil || cloid != "0x0123456789abcdef0123456789abcdef" {
		t.Fatalf("cloid=%q err=%v", cloid, err)
	}
}

func TestValidateActionRejectsDivergence(t *testing.T) {
	cases := map[string]string{
		"size":          strings.Replace(goodLimit, `"s":"0.001"`, `"s":"0.01"`, 1),
		"price":         strings.Replace(goodLimit, `"p":"50000"`, `"p":"50001"`, 1),
		"side":          strings.Replace(goodLimit, `"b":true`, `"b":false`, 1),
		"asset":         strings.Replace(goodLimit, `"a":0`, `"a":1`, 1),
		"reduce":        strings.Replace(goodLimit, `"r":false`, `"r":true`, 1),
		"tif":           strings.Replace(goodLimit, `Gtc`, `Ioc`, 1),
		"builder fee":   strings.Replace(goodLimit, `"f":10`, `"f":11`, 1),
		"builder addr":  strings.Replace(goodLimit, `0xd637`, `0xd638`, 1),
		"grouping":      strings.Replace(goodLimit, `"na"`, `"normalTpsl"`, 1),
		"unknown field": strings.Replace(goodLimit, `"grouping"`, `"vaultAddress":"0x1","grouping"`, 1),
		"trigger":       strings.Replace(goodLimit, `{"limit":{"tif":"Gtc"}}`, `{"trigger":{"isMarket":true,"triggerPx":"1","tpsl":"sl"}}`, 1),
		"two orders":    strings.Replace(goodLimit, `}],"grouping"`, `},{"a":0,"b":true,"p":"50000","s":"0.001","r":false,"t":{"limit":{"tif":"Gtc"}}}],"grouping"`, 1),
		"family":        `{"type":"usdSend","destination":"0x1","amount":"1","time":1}`,
		"duplicate key": strings.Replace(goodLimit, `"s":"0.001"`, `"s":"0.001","s":"5"`, 1),
		"float":         strings.Replace(goodLimit, `"a":0`, `"a":0.0`, 1),
		"bad cloid":     strings.Replace(goodLimit, `0x0123456789abcdef0123456789abcdef`, `0xZZ`, 1),
	}
	for name, action := range cases {
		_, err := ValidateAction(limitIntent(), action)
		var m *Mismatch
		if !errors.As(err, &m) {
			t.Errorf("%s: accepted (err=%v)", name, err)
		}
	}
}

func TestMarketWorstPriceBound(t *testing.T) {
	in := limitIntent()
	in.OrderType, in.Price, in.SlippageBps, in.ReferencePx = "market", "", 100, "50000"
	worst, err := WorstPrice("50000", true, 100, 1)
	if err != nil || worst != "50500" { // exact, no rounding
		t.Fatalf("worst=%s err=%v", worst, err)
	}
	in.WorstPx = worst
	ioc := strings.Replace(goodLimit, "Gtc", "Ioc", 1)
	if _, err := ValidateAction(in, strings.Replace(ioc, `"p":"50000"`, `"p":"50500"`, 1)); err != nil {
		t.Fatalf("at bound rejected: %v", err)
	}
	if _, err := ValidateAction(in, strings.Replace(ioc, `"p":"50000"`, `"p":"50501"`, 1)); err == nil {
		t.Fatal("above bound accepted")
	}
	// Inexact bounds round toward the reference, never loosening slippage.
	bounds := []struct {
		ref  string
		buy  bool
		bps  int
		dec  int
		want string
	}{
		{"0.123457", false, 50, 6, "0.12284"}, // 0.122839715 -> ceil
		{"0.123457", true, 50, 6, "0.12407"},  // 0.124074285 -> floor, 5 sig figs
		{"3.33333", true, 1, 1, "3.3"},        // 3.3336666 -> floor
		{"3.33333", false, 1, 1, "3.4"},       // 3.3329999 -> ceil
		{"54.852", true, 50, 4, "55.126"},     // 55.12626 -> 5 sig figs, floor
		{"54.852", false, 50, 4, "54.578"},    // 54.577740 -> 5 sig figs, ceil
		{"2.10925", true, 50, 5, "2.1197"},    // 2.11979625 -> floor
		{"123456", true, 50, 1, "124073"},     // integral prices are always valid
	}
	for _, b := range bounds {
		got, err := WorstPrice(b.ref, b.buy, b.bps, b.dec)
		if err != nil || got != b.want {
			t.Errorf("WorstPrice(%s,%v,%d,%d)=%s,%v want %s", b.ref, b.buy, b.bps, b.dec, got, err, b.want)
		}
	}
	if _, err := WorstPrice("0.00001", true, 100, 1); err == nil {
		t.Error("unrepresentable buy bound accepted")
	}
}

func TestCancelValidation(t *testing.T) {
	in := &Intent{Kind: KindCancel, AssetID: 3, OrderID: 123456789012}
	if _, err := ValidateAction(in, `{"type":"cancel","cancels":[{"a":3,"o":123456789012}]}`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"type":"cancel","cancels":[{"a":3,"o":1}]}`,
		`{"type":"cancel","cancels":[{"a":3,"o":123456789012},{"a":3,"o":2}]}`,
		`{"type":"order","orders":[],"grouping":"na"}`,
	} {
		if _, err := ValidateAction(in, bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestDigestBindsAccountAndNetwork(t *testing.T) {
	a := limitIntent()
	b := limitIntent()
	b.Account = "0x2222222222222222222222222222222222222222"
	c := limitIntent()
	c.Network = "testnet"
	if a.Digest() == b.Digest() || a.Digest() == c.Digest() || a.Digest() != limitIntent().Digest() {
		t.Fatal("digest does not bind account/network deterministically")
	}
}
