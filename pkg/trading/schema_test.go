package trading

import (
	"errors"
	"strings"
	"testing"
)

// Regression for M-02: case aliases, nulls and missing fields must never
// pass validation, since the signer preserves every distinct key.
func TestValidateActionRejectsCaseAliasesAndLooseShapes(t *testing.T) {
	order := map[string]string{
		"alias size":        strings.Replace(goodLimit, `"s":"0.001"`, `"s":"100","S":"0.001"`, 1),
		"alias size only":   strings.Replace(goodLimit, `"s":"0.001"`, `"S":"0.001"`, 1),
		"alias side":        strings.Replace(goodLimit, `"b":true`, `"b":false,"B":true`, 1),
		"alias price":       strings.Replace(goodLimit, `"p":"50000"`, `"p":"1","P":"50000"`, 1),
		"alias reduce":      strings.Replace(goodLimit, `"r":false`, `"r":true,"R":false`, 1),
		"alias asset":       strings.Replace(goodLimit, `"a":0`, `"a":5,"A":0`, 1),
		"alias builder fee": strings.Replace(goodLimit, `"f":10`, `"f":500,"F":10`, 1),
		"alias builder":     strings.Replace(goodLimit, `"builder":`, `"Builder":`, 1),
		"alias tif":         strings.Replace(goodLimit, `"tif":"Gtc"`, `"tif":"Ioc","TIF":"Gtc"`, 1),
		"alias limit":       strings.Replace(goodLimit, `"limit":`, `"Limit":`, 1),
		"alias orders":      strings.Replace(goodLimit, `"orders":`, `"Orders":`, 1),
		"alias grouping":    strings.Replace(goodLimit, `"grouping":"na"`, `"grouping":"normalTpsl","Grouping":"na"`, 1),
		"alias type":        strings.Replace(goodLimit, `"type":"order"`, `"Type":"order"`, 1),
		"alias cloid":       strings.Replace(goodLimit, `"c":"0x`, `"C":"0x`, 1),
		"null size":         strings.Replace(goodLimit, `"s":"0.001"`, `"s":null`, 1),
		"missing reduce":    strings.Replace(goodLimit, `"r":false,`, ``, 1),
		"missing asset":     strings.Replace(goodLimit, `"a":0,`, ``, 1),
		"number as string":  strings.Replace(goodLimit, `"a":0`, `"a":"0"`, 1),
	}
	for name, action := range order {
		_, err := ValidateAction(limitIntent(), action)
		var m *Mismatch
		if !errors.As(err, &m) {
			t.Errorf("%s: accepted (err=%v)", name, err)
		}
	}
	cancel := &Intent{Kind: KindCancel, AssetID: 3, OrderID: 7}
	for _, bad := range []string{
		`{"type":"cancel","cancels":[{"a":3,"o":99,"O":7}]}`,
		`{"type":"cancel","cancels":[{"a":9,"A":3,"o":7}]}`,
		`{"type":"cancel","Cancels":[{"a":3,"o":7}]}`,
		`{"type":"cancel","cancels":[{"a":3}]}`,
		`{"type":"cancel","cancels":[{"a":3,"o":null}]}`,
	} {
		if _, err := ValidateAction(cancel, bad); err == nil {
			t.Errorf("cancel accepted %s", bad)
		}
	}
	lev := &Intent{Kind: KindLeverage, AssetID: 0, IsCross: true, Leverage: 5}
	for _, bad := range []string{
		`{"type":"updateLeverage","asset":0,"isCross":true,"leverage":50,"Leverage":5}`,
		`{"type":"updateLeverage","asset":0,"iscross":true,"leverage":5}`,
	} {
		if _, err := ValidateAction(lev, bad); err == nil {
			t.Errorf("leverage accepted %s", bad)
		}
	}
	if _, err := ValidateAction(lev, `{"type":"updateLeverage","asset":0,"isCross":true,"leverage":5}`); err != nil {
		t.Fatalf("valid leverage refused: %v", err)
	}
	mar := &Intent{Kind: KindMargin, AssetID: 0, MarginAmount: "1"}
	if _, err := ValidateAction(mar, `{"type":"updateIsolatedMargin","asset":0,"isBuy":true,"ntli":100000000,"Ntli":1000000}`); err == nil {
		t.Error("margin alias accepted")
	}
	if _, err := ValidateAction(mar, `{"type":"updateIsolatedMargin","asset":0,"isBuy":true,"ntli":1000000}`); err != nil {
		t.Fatalf("valid margin refused: %v", err)
	}
}
