package onchain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/journal"
)

// ErrSimulationUnsupported means the RPC has no eth_simulateV1, so the calls
// could not be checked. It is not a verdict on the calls.
var ErrSimulationUnsupported = errors.New("RPC does not support eth_simulateV1")

// SimulationRevert is a call that definitely reverts when run in order from
// the sender, with earlier calls' state applied.
type SimulationRevert struct {
	Index  int
	Reason string
}

func (e *SimulationRevert) Error() string {
	return fmt.Sprintf("call %d reverts: %s", e.Index+1, e.Reason)
}

// SimulateSequence runs templates in order from `from` in one eth_simulateV1
// block, carrying state between them, the way a 7702 batch executes them.
// It returns a *SimulationRevert for the first failing call and
// ErrSimulationUnsupported when the RPC cannot simulate.
func (r HTTPRPC) SimulateSequence(ctx context.Context, chain uint64, from string, templates []journal.OnchainTemplate) error {
	calls := make([]map[string]string, len(templates))
	for i, t := range templates {
		c := map[string]string{"from": from, "to": t.To, "value": t.Value}
		if t.Data != "" {
			c["data"] = t.Data
		}
		calls[i] = c
	}
	params := []any{map[string]any{"blockStateCalls": []any{map[string]any{"calls": calls}}, "validation": false}, "latest"}
	var blocks []struct {
		Calls []struct {
			Status     string `json:"status"`
			ReturnData string `json:"returnData"`
			Error      *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"calls"`
	}
	if err := r.call(ctx, chain, "eth_simulateV1", params, &blocks); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "not supported") || strings.Contains(msg, "does not exist") || strings.Contains(msg, "unsupported") || strings.Contains(msg, "not available") {
			return ErrSimulationUnsupported
		}
		return fmt.Errorf("simulating: %w", err)
	}
	if len(blocks) != 1 || len(blocks[0].Calls) != len(templates) {
		return errors.New("simulation returned an unexpected shape")
	}
	for i, c := range blocks[0].Calls {
		if c.Status == "0x1" {
			continue
		}
		reason := "execution reverted"
		if c.Error != nil && c.Error.Message != "" {
			reason = c.Error.Message
		}
		return &SimulationRevert{Index: i, Reason: reason}
	}
	return nil
}
