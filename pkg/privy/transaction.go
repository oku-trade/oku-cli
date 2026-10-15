package privy

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Transaction is a Privy OAuth eth_sendTransaction request. Privy signs and
// broadcasts the transaction; callers must never retry an ambiguous error.
// The OAuth chain_id wire field is from Privy's published recipe and remains
// unverified against a funded wallet in this CLI.
type Transaction struct {
	To      string
	Value   string
	Data    string
	ChainID uint64
}

func (s *Session) SendTransaction(ctx context.Context, walletID string, tx Transaction) (string, error) {
	if tx.ChainID == 0 || !validHexAddress(tx.To) || !validHexQuantity(tx.Value) || (tx.Data != "" && !validHexBytes(tx.Data)) {
		return "", errors.New("invalid transaction fields")
	}
	transaction := map[string]any{"to": tx.To, "value": tx.Value, "chain_id": tx.ChainID}
	if tx.Data != "" {
		transaction["data"] = tx.Data
	}
	var response struct {
		Data struct {
			Hash string `json:"hash"`
		} `json:"data"`
	}
	if err := s.SignedRPC(ctx, walletID, map[string]any{"method": "eth_sendTransaction", "params": map[string]any{"transaction": transaction}}, &response); err != nil {
		return "", err
	}
	if !validHash(response.Data.Hash) {
		return "", errors.New("privy returned invalid transaction hash; broadcast outcome unknown")
	}
	return response.Data.Hash, nil
}

func validHexAddress(v string) bool {
	return len(v) == 42 && validHex(v[2:]) && strings.HasPrefix(v, "0x")
}
func validHash(v string) bool { return len(v) == 66 && strings.HasPrefix(v, "0x") && validHex(v[2:]) }
func validHexQuantity(v string) bool {
	return len(v) >= 3 && strings.HasPrefix(v, "0x") && validHex(v[2:]) && (len(v) == 3 || v[2] != '0')
}
func validHexBytes(v string) bool {
	return strings.HasPrefix(v, "0x") && len(v)%2 == 0 && validHex(v[2:])
}
func validHex(v string) bool {
	for _, c := range v {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// HexQuantity converts a nonnegative uint64 to an Ethereum JSON-RPC quantity.
func HexQuantity(v uint64) string { return fmt.Sprintf("0x%x", v) }
