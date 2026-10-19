package cli

import (
	"encoding/json"
	"os"
	"testing"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"google.golang.org/protobuf/encoding/protojson"
)

// Captured from SimpleSwapServiceV2/GetQuotes, Arbitrum USDC -> WETH, 1 USDC.
func TestVerifyLiveBackendSwapExecution(t *testing.T) {
	raw, err := os.ReadFile("testdata/oku_live_swap_quote.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Intent      journal.OnchainIntent `json:"intent"`
		Info        json.RawMessage       `json:"info"`
		Approvals   []json.RawMessage     `json:"approvals"`
		Legacy      []json.RawMessage     `json:"legacy"`
		PriceImpact float64               `json:"price_impact"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	info := new(v2.ExecutionInformation)
	if err := protojson.Unmarshal(fixture.Info, info); err != nil {
		t.Fatal(err)
	}
	approvals := make([]*v2.ApprovalPayloadV2, 0, len(fixture.Approvals))
	for _, raw := range fixture.Approvals {
		approval := new(v2.ApprovalPayloadV2)
		if err := protojson.Unmarshal(raw, approval); err != nil {
			t.Fatal(err)
		}
		approvals = append(approvals, approval)
	}
	legacy := make([]*v2.ApprovalPayload, 0, len(fixture.Legacy))
	for _, raw := range fixture.Legacy {
		approval := new(v2.ApprovalPayload)
		if err := protojson.Unmarshal(raw, approval); err != nil {
			t.Fatal(err)
		}
		legacy = append(legacy, approval)
	}
	if !acceptablePriceImpact(fixture.PriceImpact) || !consistentSwapApprovals(legacy, approvals, fixture.Intent.ChainID, fixture.Intent.Token, fixture.Intent.Amount) {
		t.Fatal("live backend quote rejected before execution")
	}
	templates, err := verifyExecution(fixture.Intent, fixture.Intent.MinOut, info, approvals)
	if err != nil || len(templates) != 2 {
		t.Fatalf("live backend execution rejected: %v; templates=%v", err, templates)
	}
}
