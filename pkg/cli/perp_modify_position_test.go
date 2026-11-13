package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/journal"
)

func TestModifyWholePositionRejected(t *testing.T) {
	for _, entry := range []string{"flags", "json", "service"} {
		t.Run(entry, func(t *testing.T) {
			h := newHarness(t)
			if r := h.run("profiles", "add", "main", "--account", acct, "--api-url", h.srv.URL); r.code != 0 {
				t.Fatal(r.stdout)
			}
			reads := 0
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				if method != "GetOpenOrders" {
					return nil, false
				}
				reads++
				if body["address"] != acct || body["skipCache"] != true {
					t.Errorf("order read is not fresh: %v", body)
				}
				return api.GetOpenOrdersResponse{OpenOrders: []api.Order{{Oid: 41, Coin: "BTC", Children: []api.Order{{
					Oid: 42, Coin: "BTC", Side: "A", Sz: "0", OrderType: "Stop Market", IsTrigger: true, ReduceOnly: true, IsPositionTpsl: true,
				}}}}}, true
			}
			var message string
			switch entry {
			case "service":
				svc := testService(h)
				svc.AllowMutations(true)
				_, err := svc.Call(context.Background(), "orders modify", Globals{Profile: "main"}, map[string]any{
					"market": "BTC", "order-id": int64(42), "side": "sell", "size": "0.01", "trigger-price": "49000", "tpsl": "sl", "market-trigger": true,
				}, nil)
				if err == nil {
					t.Fatal("service accepted whole-position modify")
				}
				message = err.Error()
			default:
				args := []string{"orders", "modify", "--market", "BTC", "--order-id", "42", "--trigger-price", "49000", "--json"}
				if entry == "json" {
					h.stdin = `{"market":"BTC","order-id":42,"side":"sell","size":"0.01","trigger-price":"49000","tpsl":"sl","market-trigger":true}`
					args = []string{"orders", "modify", "--input", "-", "--json"}
				}
				r := h.run(args...)
				if r.errCode() != "INVALID_ARGUMENT" {
					t.Fatalf("whole-position modify not rejected: %s %s", r.stdout, r.stderr)
				}
				message = r.stdout
			}
			if !strings.Contains(message, "whole-position TP/SL cannot be modified atomically") || !strings.Contains(message, "--close-position") || !strings.Contains(message, "unprotected interval") {
				t.Fatalf("missing actionable refusal: %s", message)
			}
			if reads != 1 {
				t.Fatalf("order reads = %d", reads)
			}
			for _, call := range h.fake.calls {
				if call == "CreateAction" || call == "ConfirmAction" || call == "CancelAction" || call == "GetPerpPositions" {
					t.Fatalf("refusal performed unnecessary call %s", call)
				}
			}
		})
	}
}

func TestModifySubmissionChecksWholePosition(t *testing.T) {
	const wire = `{"type":"batchModify","modifies":[{"oid":42,"order":{"a":0,"b":false,"p":"44100","s":"0.01","r":true,"t":{"trigger":{"isMarket":true,"triggerPx":"49000","tpsl":"sl"}}}}]}`
	for _, state := range []string{"prepared", "created", "before-signing", "fixed-size"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			reads := 0
			h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
				switch method {
				case "GetOpenOrders":
					reads++
					if body["address"] != acct || body["skipCache"] != true {
						t.Errorf("order read is not fresh: %v", body)
					}
					position := state != "fixed-size" && reads >= 2
					if state == "before-signing" {
						position = reads >= 3
					}
					return api.GetOpenOrdersResponse{OpenOrders: []api.Order{{Oid: 42, Coin: "BTC", Side: "A", Sz: "0.01", OrderType: "Stop Market", IsTrigger: true, ReduceOnly: true, IsPositionTpsl: position}}}, true
				case "CreateAction":
					h.fake.creates++
					return api.ActionPayloadResponse{ActionID: "modify-action", ActionJSON: wire}, true
				}
				return nil, false
			}
			r := h.run("orders", "modify", "--market", "BTC", "--order-id", "42", "--side", "sell", "--size", "0.01", "--trigger-price", "49000", "--tpsl", "sl", "--market-trigger", "--json")
			if r.code != 0 {
				t.Fatal(r.stdout)
			}
			id, digest := r.data()["intent_id"].(string), r.data()["digest"].(string)
			if state == "created" {
				j, err := journal.Open("main")
				if err != nil {
					t.Fatal(err)
				}
				rec, err := j.Get(id)
				if err != nil {
					t.Fatal(err)
				}
				rec.State, rec.ActionID, rec.ActionJSON = journal.StateCreated, "modify-action", wire
				if err := j.Put(rec, ""); err != nil {
					t.Fatal(err)
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
			}
			r = h.run("orders", "submit", "--intent", id, "--digest", digest, "--json")
			if state == "fixed-size" {
				if r.code != 0 || h.fake.creates != 1 || h.fake.confirms != 1 || reads != 3 {
					t.Fatalf("fixed-size modify failed: %s %s", r.stdout, r.stderr)
				}
			} else {
				if r.errCode() != "INVALID_ARGUMENT" || !strings.Contains(r.stdout, "whole-position TP/SL") || h.fake.confirms != 0 || h.fake.lastSigned != nil {
					t.Fatalf("unsafe modify was not refused before signing: %s %s", r.stdout, r.stderr)
				}
				wantCreates := 0
				if state == "before-signing" {
					wantCreates = 1
				}
				if h.fake.creates != wantCreates {
					t.Fatalf("unexpected action creation count %d", h.fake.creates)
				}
			}
			for _, call := range h.fake.calls {
				if call == "CancelAction" {
					t.Fatal("modify cancelled protection")
				}
			}
		})
	}
}
