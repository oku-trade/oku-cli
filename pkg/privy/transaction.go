package privy

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Transaction is a Privy OAuth eth_sendTransaction request. Privy signs and
// broadcasts the transaction; callers must never retry an ambiguous error.
// Shape follows Privy's published wallet RPC schema (EthereumSendTransactionRpcInput):
// a required top-level caip2 ("eip155:<chain>") plus params.transaction with
// chain_id. Verified live on Arbitrum with a funded agent wallet.
type Transaction struct {
	To             string
	Value          string
	Data           string
	ChainID        uint64
	LegacyGasPrice string // when set, send a type-0 transaction
}

func (s *Session) SendTransaction(ctx context.Context, walletID string, tx Transaction) (string, error) {
	if tx.ChainID == 0 || !validHexAddress(tx.To) || !validHexQuantity(tx.Value) || (tx.Data != "" && !validHexBytes(tx.Data)) {
		return "", errors.New("invalid transaction fields")
	}
	transaction := map[string]any{"to": tx.To, "value": tx.Value, "chain_id": tx.ChainID}
	if tx.Data != "" {
		transaction["data"] = tx.Data
	}
	if tx.LegacyGasPrice != "" {
		if !validHexQuantity(tx.LegacyGasPrice) {
			return "", errors.New("invalid legacy gas price")
		}
		transaction["type"] = 0
		transaction["gas_price"] = tx.LegacyGasPrice
	}
	var response struct {
		Data struct {
			Hash string `json:"hash"`
		} `json:"data"`
	}
	if err := s.SignedRPC(ctx, walletID, map[string]any{"method": "eth_sendTransaction", "caip2": fmt.Sprintf("eip155:%d", tx.ChainID), "params": map[string]any{"transaction": transaction}}, &response); err != nil {
		return "", err
	}
	if !validHash(response.Data.Hash) {
		return "", errors.New("privy returned invalid transaction hash; broadcast outcome unknown")
	}
	return response.Data.Hash, nil
}

// Call is one entry of an atomic wallet_sendCalls batch.
type Call struct {
	To    string
	Value string
	Data  string
}

// SendCalls submits an atomic EIP-7702 batch (Privy upgrades the wallet to a
// Kernel account on first use). It returns Privy's transaction ID, not a chain
// hash. A successful response means the batch was broadcast; callers must
// never retry an ambiguous error.
func (s *Session) SendCalls(ctx context.Context, walletID string, chainID uint64, calls []Call) (string, error) {
	if chainID == 0 || len(calls) == 0 {
		return "", errors.New("invalid batch")
	}
	wire := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		if !validHexAddress(c.To) || !validHexQuantity(c.Value) || (c.Data != "" && !validHexBytes(c.Data)) {
			return "", errors.New("invalid call fields")
		}
		call := map[string]any{"to": c.To, "value": c.Value}
		if c.Data != "" {
			call["data"] = c.Data
		}
		wire = append(wire, call)
	}
	var response struct {
		Data struct {
			TransactionID string `json:"transaction_id"`
			Caip2         string `json:"caip2"`
		} `json:"data"`
	}
	caip2 := fmt.Sprintf("eip155:%d", chainID)
	if err := s.SignedRPC(ctx, walletID, map[string]any{"method": "wallet_sendCalls", "caip2": caip2, "chain_type": "ethereum", "params": map[string]any{"calls": wire}}, &response); err != nil {
		return "", err
	}
	if !validTransactionID(response.Data.TransactionID) || response.Data.Caip2 != caip2 {
		return "", errors.New("privy returned invalid batch transaction ID; broadcast outcome unknown")
	}
	return response.Data.TransactionID, nil
}

func validTransactionID(v string) bool {
	if len(v) < 8 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
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
