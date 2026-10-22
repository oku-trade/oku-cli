package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

func TestBridgeStatusAllAndReadyIdempotent(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.fake.routeReply = func(method string, body map[string]any) (any, bool) {
		if method == "History" {
			return map[string]any{"history": []any{
				map[string]any{"orderId": "awaiting", "status": "ORDER_STATUS_AWAITING_CLAIM", "dstChain": "eip155:8453"},
				map[string]any{"orderId": "settled", "status": "ORDER_STATUS_COMPLETED", "dstChain": "eip155:10"},
			}}, true
		}
		return nil, false
	}
	p := &resolved{Name: "main", Network: "mainnet", Account: acct, APIURL: h.srv.URL}
	j, err := openJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range []struct {
		id, order string
		dest      uint64
	}{{"bridge-open", "awaiting", 8453}, {"bridge-done", "settled", 10}} {
		_, err = j.CreateOnchain(journal.OnchainIntent{ID: item.id, Kind: "bridge", ChainID: 42161, OutChain: item.dest, Sender: h.fake.agentAddress, Recipient: h.fake.agentAddress, Token: "native", Amount: "100", OrderID: item.order, CreatedAt: now, Templates: []journal.OnchainTemplate{{ChainID: 42161, To: h.fake.agentAddress, Value: "0x0"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = j.AdvanceOnchain(item.id, "prepared", "confirmed", ""); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := j.CreateOnchain(journal.OnchainIntent{ID: "claim-existing", Kind: "claim", ParentIntent: "bridge-open", OrderID: "awaiting", ChainID: 8453, OutChain: 8453, Sender: h.fake.agentAddress, Recipient: h.fake.agentAddress, Token: "native", Amount: "0", CreatedAt: now, Templates: []journal.OnchainTemplate{{ChainID: 8453, To: h.fake.agentAddress, Value: "0x0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = j.BeginOnchain(claim.ID, claim.Digest, h.fake.agentAddress); err != nil {
		t.Fatal(err)
	}
	if err = j.AdvanceOnchain(claim.ID, "sending", "confirmed", ""); err != nil {
		t.Fatal(err)
	}
	j.Close()
	status := h.run("bridge", "status", "--all", "--json")
	if status.code != 0 {
		t.Fatalf("status: %s %s", status.stdout, status.stderr)
	}
	var envelope struct {
		Data []bridgePending `json:"data"`
	}
	if err = json.Unmarshal([]byte(status.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	entries := envelope.Data
	if len(entries) != 1 || entries[0].IntentID != "bridge-open" || entries[0].ClaimIntentID != "claim-existing" || !entries[0].ClaimRequired {
		t.Fatalf("unexpected pending: %+v", entries)
	}
	withoutFlag := h.run("bridge", "status", "--json")
	if withoutFlag.code != 0 || !strings.Contains(withoutFlag.stdout, "bridge-open") || strings.Contains(withoutFlag.stdout, "bridge-done") {
		t.Fatalf("default status should list unsettled bridges: %s %s", withoutFlag.stdout, withoutFlag.stderr)
	}
	for i := 0; i < 2; i++ {
		ready := h.run("bridge", "claim", "--ready", "--json")
		if ready.code != 0 || !strings.Contains(ready.stdout, "already claimed") || h.privy.rpcCalls != 0 {
			t.Fatalf("ready run %d: %s %s sends %d", i, ready.stdout, ready.stderr, h.privy.rpcCalls)
		}
	}
}
