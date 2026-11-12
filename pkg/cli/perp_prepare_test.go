package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/trading"
)

func TestPerpPrepareVariants(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL); r.code != 0 {
		t.Fatal(r.stdout)
	}
	cases := []struct {
		name     string
		args     []string
		metadata string
	}{
		{"stop_market", []string{"prepare", "--type", "stop_market", "--trigger-price", "49000"}, "order"},
		{"stop_limit", []string{"prepare", "--type", "stop_limit", "--trigger-price", "49000", "--price", "48900"}, "order"},
		{"take_market", []string{"prepare", "--type", "take_market", "--trigger-price", "51000", "--close-position"}, "order"},
		{"take_limit", []string{"prepare", "--type", "take_limit", "--trigger-price", "51000", "--price", "51100"}, "order"},
		{"entry-tpsl", []string{"prepare", "--type", "limit", "--price", "50000", "--take-profit-price", "51000", "--stop-loss-price", "49000", "--tp-size", "0.005", "--sl-limit-price", "48900"}, "order"},
		{"market-entry-tpsl", []string{"prepare", "--type", "market", "--take-profit-price", "51000", "--stop-loss-price", "49000"}, "order"},
		{"twap", []string{"prepare", "--type", "twap", "--duration-minutes", "10", "--randomize", "--trigger-price", "51000", "--trigger-activate-above", "--stop-price", "48000"}, "twap"},
		{"scale", []string{"prepare", "--type", "scale", "--start-price", "49000", "--end-price", "50000", "--total-orders", "3", "--size-skew", "2"}, "scale"},
		{"trailing-percent", []string{"prepare", "--type", "trailing_stop", "--retracement-pct", "2.5", "--activation-price", "51000"}, "trailingStop"},
		{"trailing-price", []string{"prepare", "--type", "trailing_stop", "--retracement-price", "500"}, "trailingStop"},
		{"modify-limit", []string{"modify", "--order-id", "42", "--price", "50000", "--tif", "alo", "--reduce-only"}, "modify"},
		{"modify-stop", []string{"modify", "--order-id", "42", "--trigger-price", "50000", "--tpsl", "sl", "--market-trigger"}, "modify"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"orders"}, tc.args...)
			args = append(args, "--market", "BTC", "--side", "sell", "--size", "0.01", "--json")
			r := h.run(args...)
			if r.code != 0 {
				t.Fatalf("%s %s", r.stdout, r.stderr)
			}
			b, err := json.Marshal(r.data()["intent"])
			if err != nil {
				t.Fatal(err)
			}
			var in trading.Intent
			if err := json.Unmarshal(b, &in); err != nil {
				t.Fatal(err)
			}
			if in.Advanced == nil || in.Kind != in.Advanced.Kind() {
				t.Fatal("missing typed plan")
			}
			req := createRequest(&in)
			raw, err := json.Marshal(req.Params)
			if err != nil {
				t.Fatal(err)
			}
			var params map[string]json.RawMessage
			if err := json.Unmarshal(raw, &params); err != nil {
				t.Fatal(err)
			}
			if len(params) != 1 || params[tc.metadata] == nil {
				t.Fatalf("wrong metadata: %s", raw)
			}
			switch tc.name {
			case "stop_market":
				if req.Params.Order.LimitPx != "" || req.Params.Order.TriggerPx != "49000" || req.Params.Order.SlippageBps != 50 {
					t.Fatalf("wrong backend market trigger contract: %s", raw)
				}
			case "take_market":
				if !req.Params.Order.ClosePosition || req.Params.Order.Sz != "0" || !in.ReduceOnly {
					t.Fatalf("position TP/SL: %s", raw)
				}
			case "market-entry-tpsl":
				if req.Params.Order.OrderType != "limit" || req.Params.Order.Tif != "IOC" || req.Params.Order.LimitPx != "49750" {
					t.Fatalf("market bound: %s", raw)
				}
			case "modify-stop":
				o := req.Params.Modify.Modifies[0]
				if o.TriggerPx != "50000" || o.LimitPx != "" || !o.IsMarket {
					t.Fatalf("modify trigger metadata: %s", raw)
				}
			}
		})
	}
	for _, args := range [][]string{
		{"cancel-twap", "--twap-id", "42"},
		{"cancel", "--order-ids", "42,43", "--prepare"},
		{"cancel", "--order-id", "42", "--prepare"},
	} {
		r := h.run(append(append([]string{"orders"}, args...), "--market", "BTC", "--json")...)
		if r.code != 0 {
			t.Fatalf("%v: %s", args, r.stdout)
		}
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if h.fake.creates != 0 || h.fake.confirms != 0 {
		t.Fatal("prepare sent an exchange action")
	}
}

func TestPerpPrepareRejectsIllegalCombinations(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL); r.code != 0 {
		t.Fatal(r.stdout)
	}
	for _, flags := range []string{
		"--type stop_market --trigger-price 49000 --price 48000",
		"--type stop_limit --price 48000",
		"--type stop_market --trigger-price 49000 --close-position --reduce-only=false",
		"--type limit --price 50000 --close-position",
		"--type limit --price 50000 --tp-size 0.01",
		"--type twap --duration-minutes 4",
		"--type twap --duration-minutes 10 --trigger-activate-above",
		"--type twap --duration-minutes 10 --price 50000",
		"--type scale --start-price 49000 --end-price 50000 --total-orders 1",
		"--type trailing_stop --retracement-pct 1 --retracement-price 500",
	} {
		args := append([]string{"orders", "prepare", "--market", "BTC", "--side", "sell", "--size", "0.01", "--json"}, strings.Fields(flags)...)
		r := h.run(args...)
		if r.code == 0 || r.errCode() == "INTERNAL" {
			t.Fatalf("%s: %s", flags, r.stdout)
		}
	}
	for _, flags := range []string{
		"--trigger-price 49000 --market-trigger",
		"--trigger-price 49000 --tpsl sl --market-trigger --reduce-only=false",
		"--trigger-price 49000 --tpsl sl --market-trigger --price 48000",
		"--tpsl sl --price 49000",
	} {
		r := h.run(append([]string{"orders", "modify", "--market", "BTC", "--side", "sell", "--size", "0.01", "--order-id", "42", "--json"}, strings.Fields(flags)...)...)
		if r.code == 0 || r.errCode() == "INTERNAL" {
			t.Fatalf("%s: %s", flags, r.stdout)
		}
	}
}

func TestAdvancedOrderInputParity(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL); r.code != 0 {
		t.Fatal(r.stdout)
	}
	h.stdin = `{"market":"BTC","side":"sell","type":"stop_market","size":"0.01","trigger-price":"49000","close-position":true}`
	r := h.run("orders", "prepare", "--input", "-", "--json")
	if r.code != 0 {
		t.Fatal(r.stdout)
	}
	r = h.run("orders", "prepare", "--market", "BTC", "--side", "sell", "--type", "stop_market", "--trigger-price", "49000", "--close-position", "--json")
	if r.code != 0 {
		t.Fatal(r.stdout)
	}
	h.stdin = `{"market":"BTC","side":"buy","type":"limit","size":"0.01","price":"49000","randomize":false}`
	r = h.run("orders", "prepare", "--input", "-", "--json")
	if r.code == 0 || r.errCode() == "INTERNAL" {
		t.Fatal(r.stdout)
	}
}
