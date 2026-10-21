package cli

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
)

// backendBridgeExecution serializes transaction-based bridge calls exactly as
// supplied by Oku. It validates only the shape needed by the EVM executor, not
// the bridge's economic effects, recipient, approval limits or calldata.
func backendBridgeExecution(chain uint64, info *v2.ExecutionInformation, approvals []*v2.ApprovalPayloadV2) ([]journal.OnchainTemplate, error) {
	if !bridgeInfo(info) {
		return nil, fmt.Errorf("unsupported bridge execution shape")
	}
	calls := make([]journal.OnchainTemplate, 0, len(approvals)+1+len(info.Transactions))
	appendApproval := func(token, spender, amountText string) error {
		if !onchain.ValidAddress(token) || !onchain.ValidAddress(spender) {
			return fmt.Errorf("bridge approval address invalid")
		}
		amount, ok := new(big.Int).SetString(amountText, 10)
		if !ok || amount.Sign() < 0 || amount.BitLen() > 256 {
			return fmt.Errorf("bridge approval amount invalid")
		}
		data := "0x095ea7b3" + strings.Repeat("0", 24) + strings.ToLower(spender[2:]) + fmt.Sprintf("%064x", amount)
		calls = append(calls, journal.OnchainTemplate{ChainID: chain, To: token, Value: "0x0", Data: data})
		return nil
	}
	for _, ap := range approvals {
		if ap == nil || !onchain.ValidAddress(ap.To) || ap.Values == nil || ap.FunctionName != "approve" {
			return nil, fmt.Errorf("unsupported bridge approval encoding")
		}
		values := ap.Values.AsMap()
		spender, _ := values["spender"].(string)
		if err := appendApproval(ap.To, spender, fmt.Sprint(values["amount"])); err != nil {
			return nil, err
		}
	}
	if len(approvals) == 0 {
		for _, ap := range info.Approvals {
			if ap == nil || ap.ChainId != int64(chain) {
				return nil, fmt.Errorf("bridge approval source chain invalid")
			}
			if err := appendApproval(ap.Address, ap.Approvee, ap.Amount); err != nil {
				return nil, err
			}
		}
	}
	appendTx := func(tx *v2.TransactionPayload) error {
		if tx == nil || tx.ChainId != int64(chain) || !onchain.ValidAddress(tx.To) {
			return fmt.Errorf("bridge transaction target or source chain invalid")
		}
		valueText := tx.Value
		if valueText == "" {
			valueText = "0"
		}
		value, err := backendValue(valueText)
		if err != nil {
			return fmt.Errorf("bridge transaction value invalid: %w", err)
		}
		if !strings.HasPrefix(tx.Data, "0x") {
			return fmt.Errorf("bridge calldata must be hex")
		}
		if _, err = hex.DecodeString(tx.Data[2:]); err != nil {
			return fmt.Errorf("bridge calldata invalid: %w", err)
		}
		calls = append(calls, journal.OnchainTemplate{ChainID: chain, To: tx.To, Value: onchain.Hex(value), Data: tx.Data})
		return nil
	}
	if err := appendTx(info.Trade); err != nil {
		return nil, err
	}
	for _, tx := range info.Transactions {
		// Some backend responses mirror Trade in Transactions. It is not an extra call.
		if tx != nil && tx.ChainId == info.Trade.ChainId && strings.EqualFold(tx.To, info.Trade.To) && tx.Data == info.Trade.Data && tx.Value == info.Trade.Value {
			continue
		}
		if err := appendTx(tx); err != nil {
			return nil, err
		}
	}
	return calls, nil
}
