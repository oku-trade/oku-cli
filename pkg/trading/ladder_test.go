package trading

import "testing"

func TestAggressivePriceBackendTruncation(t *testing.T) {
	for _, tc := range []struct {
		ref      string
		buy      bool
		bps, dec int
		want     string
	}{
		{"100", true, 100, 2, "101"},
		{"100", false, 100, 2, "99"},
		{"0.123457", false, 50, 0, "0.12283"},
		{"123456.7", true, 1, 0, "123460"},
		{"54.852", false, 50, 2, "54.577"},
	} {
		got, err := AggressivePrice(tc.ref, tc.dec, tc.buy, tc.bps)
		if err != nil || got != tc.want {
			t.Errorf("AggressivePrice(%s,%d,%v,%d)=%s,%v want %s", tc.ref, tc.dec, tc.buy, tc.bps, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		ref      string
		dec, bps int
	}{{"0", 0, 100}, {"1", -1, 100}, {"1", 7, 100}, {"1", 0, 0}, {"1", 0, 10000}, {"0.000001", 6, 100}} {
		if _, err := AggressivePrice(tc.ref, tc.dec, true, tc.bps); err == nil {
			t.Errorf("accepted invalid aggressive price: %+v", tc)
		}
	}
}

func TestScaleOrdersBackendLadder(t *testing.T) {
	orders, err := ScaleOrders(7, false, "1", "1.23456", "2.34567", "4", 3, 2, "ALO", true)
	if err != nil {
		t.Fatal(err)
	}
	prices := []string{"1.2345", "1.7901", "2.3456"}
	sizes := []string{"0.14", "0.28", "0.58"}
	for i, o := range orders {
		if o.Price != prices[i] || o.Size != sizes[i] || o.AssetID != 7 || o.IsBuy || !o.ReduceOnly || o.Execution.LimitTIF() != "Alo" {
			t.Errorf("child %d=%+v want %s/%s", i, o, prices[i], sizes[i])
		}
	}
	// Descending price steps truncate toward zero at six decimals before
	// multiplication. The final child is the exact truncated endpoint.
	descending, err := ScaleOrders(7, true, "9", "100", "90", "1", 4, 2, "GTC", false)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"100", "96.666", "93.333", "90"} {
		if descending[i].Price != want {
			t.Errorf("descending child %d price=%s want %s", i, descending[i].Price, want)
		}
	}
	for _, tc := range []struct {
		total, skew string
		count       int
	}{{"0.01", "1", 3}, {"1", "0", 3}, {"1", "NaN", 3}, {"1", "1", 1}, {"1", "1", 101}, {"1.001", "1", 3}} {
		if _, err := ScaleOrders(0, true, tc.total, "90", "100", tc.skew, tc.count, 2, "GTC", false); err == nil {
			t.Errorf("accepted invalid scale %+v", tc)
		}
	}
}
