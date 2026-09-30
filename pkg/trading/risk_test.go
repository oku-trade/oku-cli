package trading

import (
	"errors"
	"testing"
)

func TestMarginUnits(t *testing.T) {
	ok := map[string]int64{"1": 1_000_000, "-2.5": -2_500_000, "0.000001": 1, "123.456789": 123_456_789}
	for in, want := range ok {
		got, err := MarginUnits(in)
		if err != nil || got != want {
			t.Errorf("MarginUnits(%q)=%d,%v want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "-0", "0.0000001", "1.50", "01", "+1", "", "1e3", "abc", "9007199254.740992"} {
		if _, err := MarginUnits(in); err == nil {
			t.Errorf("MarginUnits(%q) accepted", in)
		}
	}
}

func TestValidateLeverage(t *testing.T) {
	if err := ValidateLeverage(5, 20, true, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		lev, max      int
		cross, isoOnl bool
	}{{0, 20, false, false}, {21, 20, false, false}, {3, 0, false, false}, {3, 10, true, true}} {
		if ValidateLeverage(c.lev, c.max, c.cross, c.isoOnl) == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestValidateRiskActions(t *testing.T) {
	lev := &Intent{Kind: KindLeverage, AssetID: 3, Leverage: 5, IsCross: false}
	mar := &Intent{Kind: KindMargin, AssetID: 3, MarginAmount: "-1.25"}
	cases := []struct {
		in   *Intent
		json string
		ok   bool
	}{
		{lev, `{"type":"updateLeverage","asset":3,"isCross":false,"leverage":5}`, true},
		{lev, `{"type":"updateLeverage","asset":4,"isCross":false,"leverage":5}`, false},
		{lev, `{"type":"updateLeverage","asset":3,"isCross":true,"leverage":5}`, false},
		{lev, `{"type":"updateLeverage","asset":3,"isCross":false,"leverage":6}`, false},
		{lev, `{"type":"updateLeverage","asset":3,"leverage":5}`, false},
		{lev, `{"type":"updateLeverage","asset":3,"isCross":false,"leverage":5,"vaultAddress":"0x1"}`, false},
		{lev, `{"type":"updateIsolatedMargin","asset":3,"isBuy":true,"ntli":5}`, false},
		{mar, `{"type":"updateIsolatedMargin","asset":3,"isBuy":true,"ntli":-1250000}`, true},
		{mar, `{"type":"updateIsolatedMargin","asset":3,"isBuy":true,"ntli":1250000}`, false},
		{mar, `{"type":"updateIsolatedMargin","asset":3,"isBuy":false,"ntli":-1250000}`, false},
		{mar, `{"type":"updateIsolatedMargin","asset":3,"isBuy":true,"ntli":-1250000,"ntli":0}`, false},
		{mar, `{"type":"updateLeverage","asset":3,"isCross":false,"leverage":5}`, false},
		{&Intent{Kind: KindOrder}, `{"type":"updateLeverage","asset":3,"isCross":false,"leverage":5}`, false},
	}
	for i, c := range cases {
		_, err := ValidateAction(c.in, c.json)
		if (err == nil) != c.ok {
			t.Errorf("case %d: err=%v want ok=%v", i, err, c.ok)
		}
		var m *Mismatch
		if err != nil && !errors.As(err, &m) {
			t.Errorf("case %d: non-mismatch error %v", i, err)
		}
	}
}

func TestRiskOutcome(t *testing.T) {
	for s, want := range map[string]Outcome{"completed": OutcomeSuccess, "error": OutcomeRejected, "submitted": OutcomeUnknown, "unresolved": OutcomeUnknown, "": OutcomeUnknown, "filled": OutcomeUnknown} {
		if got := RiskOutcome(s); got != want {
			t.Errorf("%q: %s want %s", s, got, want)
		}
	}
}
