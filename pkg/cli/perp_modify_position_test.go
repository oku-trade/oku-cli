package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/trading"
)

func TestModifyPositionTpsl(t *testing.T) {
	for _, tc := range []struct {
		name, szi, side, orderType, wantError string
		flags                                 []string
	}{
		{name: "long stop", szi: "0.012349", side: "A", orderType: "Stop Market"},
		{name: "short take profit", szi: "-0.012340", side: "B", orderType: "Take Profit Market", flags: []string{"--size", "0.01234"}},
		{name: "trigger limit", szi: "0.01234", side: "A", orderType: "Stop Limit", flags: []string{"--price", "48900"}},
		{name: "conflicting size", szi: "0.01234", side: "A", orderType: "Stop Market", flags: []string{"--size", "0.01"}, wantError: "--size conflicts"},
		{name: "no position", side: "A", orderType: "Stop Market", wantError: "no open position"},
		{name: "closed position", szi: "0", side: "A", orderType: "Stop Market", wantError: "no open position"},
		{name: "unknown size", szi: "bad", side: "A", orderType: "Stop Market", wantError: "live position size"},
		{name: "conflicting kind", szi: "0.01234", side: "A", orderType: "Stop Market", flags: []string{"--tpsl", "tp"}, wantError: "must retain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL); r.code != 0 {
				t.Fatal(r.stdout)
			}
			orderReads, positionReads := 0, 0
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				switch method {
				case "GetOpenOrders":
					orderReads++
					if body["address"] != acct || body["skipCache"] != true {
						t.Errorf("order read is not fresh: %v", body)
					}
					return map[string]any{"openOrders": []any{map[string]any{"oid": "41", "coin": "BTC", "children": []any{map[string]any{
						"oid": "42", "coin": "BTC", "side": tc.side, "sz": "0", "orderType": tc.orderType, "isTrigger": true, "reduceOnly": true, "isPositionTpsl": true,
					}}}}}, true
				case "GetPerpPositions":
					positionReads++
					positions := []any{}
					if tc.szi != "" {
						positions = append(positions, map[string]any{"coin": "BTC", "szi": tc.szi})
					}
					return map[string]any{"marginSummary": map[string]any{}, "positions": positions}, true
				}
				return nil, false
			}
			r := h.run(append([]string{"orders", "modify", "--market", "BTC", "--order-id", "42", "--trigger-price", "49000", "--json"}, tc.flags...)...)
			if tc.wantError != "" {
				if r.code == 0 || !strings.Contains(r.stdout, tc.wantError) {
					t.Fatalf("expected %q: %s %s", tc.wantError, r.stdout, r.stderr)
				}
				return
			}
			if r.code != 0 {
				t.Fatalf("%s %s", r.stdout, r.stderr)
			}
			raw, err := json.Marshal(r.data()["intent"])
			if err != nil {
				t.Fatal(err)
			}
			var in trading.Intent
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			plan := in.Advanced.Payload().(trading.ModifyPlan)
			item := createRequest(&in).Params.Modify.Modifies[0]
			if !plan.PositionTpsl || plan.AlwaysPlace || item.Size != "0.01234" || int64(item.Oid) != 42 || item.TriggerPx != "49000" || item.IsBuy != (tc.side == "B") || item.IsMarket != strings.HasSuffix(tc.orderType, "Market") {
				t.Fatalf("wrong position modify: %s", raw)
			}
			if orderReads != 1 || positionReads != 1 || h.fake.creates != 0 || h.fake.confirms != 0 {
				t.Fatal("preparation must read live inputs once and send no exchange action")
			}
		})
	}
}
