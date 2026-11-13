package cli

import (
	"fmt"
	"strings"
	"testing"
)

func TestOrdersListDiscovery(t *testing.T) {
	h := newHarness(t)
	if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
		t.Fatalf("profile: %s %s", r.stdout, r.stderr)
	}
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method != "GetOpenOrders" && method != "GetTwapHistory" {
			return nil, false
		}
		if body["address"] != acct || body["skipCache"] != true {
			t.Errorf("%s request: %v", method, body)
		}
		if method == "GetOpenOrders" {
			return map[string]any{"openOrders": []any{map[string]any{
				"oid": "101", "coin": "BTC", "side": "B", "sz": "1", "limitPx": "60000", "orderType": "Limit",
				"children": []any{
					map[string]any{"oid": "102", "coin": "BTC", "side": "A", "sz": "0", "orderType": "Stop Market", "triggerPx": "55000", "triggerCondition": "Below", "isTrigger": true, "isPositionTpsl": true, "reduceOnly": true},
					map[string]any{"oid": "103", "coin": "BTC", "side": "A", "sz": "1", "orderType": "take_limit", "triggerPx": "65000", "limitPx": "64900", "isTrigger": true, "reduceOnly": true},
				},
			}}}, true
		}
		twaps := []any{}
		for i, status := range []string{"activated", "Waiting For Trigger", "future-status", "FINISHED", "Terminated", "Stopped", "Error"} {
			twaps = append(twaps, map[string]any{"twapId": fmt.Sprint(201 + i), "coin": "ETH", "side": "A", "sz": "2", "executedSz": "0.5", "minutes": 30, "status": status, "triggerPx": "3000", "stopPx": "2800", "reduceOnly": true})
		}
		return map[string]any{"twaps": twaps}, true
	}

	r := h.run("orders", "list", "--fresh", "--json")
	if r.code != 0 {
		t.Fatalf("list: %s %s", r.stdout, r.stderr)
	}
	orders := r.data()["orders"].([]any)
	parent := orders[0].(map[string]any)
	children := parent["children"].([]any)
	sl, tp := children[0].(map[string]any), children[1].(map[string]any)
	if parent["orderType"] != "Limit" || sl["tpsl"] != "sl" || sl["triggerPx"] != "55000" || sl["isPositionTpsl"] != true || sl["sz"] != "0" || tp["tpsl"] != "tp" || tp["limitPx"] != "64900" {
		t.Fatalf("lost order semantics: %v", orders)
	}
	twaps := r.data()["active_twaps"].([]any)
	if len(twaps) != 3 || twaps[1].(map[string]any)["status"] != "Waiting For Trigger" || twaps[2].(map[string]any)["status"] != "future-status" {
		t.Fatalf("active TWAPs: %v", twaps)
	}
	if r.data()["meta"].(map[string]any)["source"] != "oku:PerpsService/GetOpenOrders+GetTwapHistory" {
		t.Fatalf("source: %v", r.data()["meta"])
	}

	r = h.run("orders", "list", "--fresh")
	if r.code != 0 {
		t.Fatalf("human list: %s %s", r.stdout, r.stderr)
	}
	for _, text := range []string{"PARENT", "TRIGGER PX", "TP/SL", "POSITION TP/SL", "55000", "65000", "Stop Market", "take_limit", "active TWAPs", "TWAP ID", "Waiting For Trigger", "future-status", "0.5"} {
		if !strings.Contains(r.stdout, text) {
			t.Errorf("human list missing %q: %s", text, r.stdout)
		}
	}
	for _, text := range []string{"FINISHED", "Terminated", "Stopped", "Error"} {
		if strings.Contains(r.stdout, text) {
			t.Errorf("human list includes terminal TWAP %q: %s", text, r.stdout)
		}
	}
}

func TestOrdersListEmptyAndUnavailableHistory(t *testing.T) {
	for _, historyAvailable := range []bool{true, false} {
		t.Run(fmt.Sprint(historyAvailable), func(t *testing.T) {
			h := newHarness(t)
			if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL, "--json"); r.code != 0 {
				t.Fatalf("profile: %s %s", r.stdout, r.stderr)
			}
			h.fake.routeReply = func(method string, _ map[string]any) (any, bool) {
				return map[string]any{}, method == "GetOpenOrders" || method == "GetTwapHistory" && historyAvailable
			}
			r := h.run("orders", "list", "--json")
			if !historyAvailable {
				if r.code == 0 || r.data() != nil {
					t.Fatalf("reported partial discovery as success: %s %s", r.stdout, r.stderr)
				}
				return
			}
			if r.code != 0 {
				t.Fatalf("empty list: %s %s", r.stdout, r.stderr)
			}
			for _, key := range []string{"orders", "active_twaps"} {
				if values, ok := r.data()[key].([]any); !ok || len(values) != 0 {
					t.Errorf("%s must be an empty array: %v", key, r.data()[key])
				}
			}
			r = h.run("orders", "list")
			if r.code != 0 || !strings.Contains(r.stdout, "no open orders") || !strings.Contains(r.stdout, "no active TWAPs") {
				t.Fatalf("empty human list: %s %s", r.stdout, r.stderr)
			}
		})
	}
}
