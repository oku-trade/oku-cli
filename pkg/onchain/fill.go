package onchain

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

const (
	transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	// okuRouterSwapTopic is OkuRouter's swap event: topics are the sender and
	// recipient, data is (tokenIn, tokenOut, amountIn, amountOut, fee, target)
	// with the zero address for native.
	okuRouterSwapTopic = "0xfb6c56e214cadb4768595d6c38ec36861d571a297ad429eb15de3b9f2f7fe09f"
)

// Log is one receipt event.
type Log struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

// TxReceipt is the subset of a transaction receipt used to measure a fill.
type TxReceipt struct {
	TransactionHash   string `json:"transactionHash"`
	Status            string `json:"status"`
	GasUsed           string `json:"gasUsed"`
	EffectiveGasPrice string `json:"effectiveGasPrice"`
	// L1Fee is the separately charged data fee of OP Stack chains.
	L1Fee string `json:"l1Fee"`
	Logs  []Log  `json:"logs"`
}

// FullReceipt returns the receipt of hash with its logs, nil if not mined.
func (r HTTPRPC) FullReceipt(ctx context.Context, c uint64, hash string) (*TxReceipt, error) {
	var raw json.RawMessage
	if err := r.call(ctx, c, "eth_getTransactionReceipt", []any{hash}, &raw); err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var receipt TxReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	if !strings.EqualFold(receipt.TransactionHash, hash) {
		return nil, errors.New("receipt identity differs")
	}
	return &receipt, nil
}

// Gas returns the native wei the transaction paid.
func (t *TxReceipt) Gas() (*big.Int, error) {
	used, err := Quantity(t.GasUsed)
	if err != nil {
		return nil, err
	}
	price, err := Quantity(t.EffectiveGasPrice)
	if err != nil {
		return nil, err
	}
	gas := new(big.Int).Mul(used, price)
	if t.L1Fee != "" {
		l1, err := Quantity(t.L1Fee)
		if err != nil {
			return nil, err
		}
		gas.Add(gas, l1)
	}
	return gas, nil
}

// SwapFill is what a swap's receipts show the recipient received.
type SwapFill struct {
	Received *big.Int
	Gas      *big.Int
	// Source is transfer_logs (ERC-20 Transfer events to the recipient) or
	// router_event (OkuRouter's amountOut, used for native output).
	Source string
}

// ErrNoFill reports receipts that do not show what the recipient received.
var ErrNoFill = errors.New("receipts show no fill to the recipient")

// MeasureSwapFill reads the amount of outToken ("native" or an address) that
// recipient received from every successful receipt of a swap, and the total
// gas paid. router is the chain's OkuRouter.
func MeasureSwapFill(receipts []*TxReceipt, router, outToken, recipient string, native bool) (*SwapFill, error) {
	if len(receipts) == 0 || !ValidAddress(recipient) || !ValidAddress(router) || !native && !ValidAddress(outToken) {
		return nil, errors.New("invalid swap fill request")
	}
	f := &SwapFill{Received: new(big.Int), Gas: new(big.Int)}
	found := false
	for _, t := range receipts {
		if t == nil || t.Status != "0x1" {
			return nil, errors.New("swap receipt missing or failed")
		}
		gas, err := t.Gas()
		if err != nil {
			return nil, err
		}
		f.Gas.Add(f.Gas, gas)
		for _, l := range t.Logs {
			if native {
				words := logWords(l.Data)
				if !strings.EqualFold(l.Address, router) || len(l.Topics) != 3 || !strings.EqualFold(l.Topics[0], okuRouterSwapTopic) || !topicIs(l.Topics[2], recipient) || len(words) < 4 || words[1].Sign() != 0 {
					continue
				}
				f.Received.Add(f.Received, words[3])
				f.Source, found = "router_event", true
				continue
			}
			if !strings.EqualFold(l.Address, outToken) || len(l.Topics) != 3 || !strings.EqualFold(l.Topics[0], transferTopic) || !topicIs(l.Topics[2], recipient) {
				continue
			}
			words := logWords(l.Data)
			if len(words) != 1 {
				continue
			}
			f.Received.Add(f.Received, words[0])
			f.Source, found = "transfer_logs", true
		}
	}
	if !found || f.Received.Sign() <= 0 {
		return nil, ErrNoFill
	}
	return f, nil
}

func topicIs(topic, address string) bool {
	return len(topic) == 66 && strings.EqualFold(topic[26:], address[2:]) && strings.Trim(topic[2:26], "0") == ""
}

func logWords(data string) []*big.Int {
	d := strings.TrimPrefix(data, "0x")
	if len(d)%64 != 0 {
		return nil
	}
	out := make([]*big.Int, 0, len(d)/64)
	for i := 0; i < len(d); i += 64 {
		w, ok := new(big.Int).SetString(d[i:i+64], 16)
		if !ok {
			return nil
		}
		out = append(out, w)
	}
	return out
}
