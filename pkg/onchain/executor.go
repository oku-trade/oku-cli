// Package onchain executes verified, journaled EVM transaction templates.
package onchain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/privy"
)

var ErrUnknown = errors.New("OUTCOME_UNKNOWN: reconcile transaction receipt; never resubmit")
var ErrReceiptUpstream = errors.New("receipt lookup failed after broadcast; run status to retry")
var ErrGas = errors.New("INSUFFICIENT_GAS")
var ErrBalance = errors.New("INSUFFICIENT_BALANCE")
var ErrReverted = errors.New("transaction reverted")

// dependentStepGas reserves gas for a step that cannot be simulated until the
// preceding step is mined.
const dependentStepGas = 3_000_000

func Allowed(id uint64) bool { return id == 42161 || id == 8453 || id == 1 }
func ValidAddress(a string) bool {
	if len(a) != 42 || !strings.HasPrefix(a, "0x") {
		return false
	}
	_, ok := new(big.Int).SetString(a[2:], 16)
	return ok
}
func Quantity(s string) (*big.Int, error) {
	if !strings.HasPrefix(s, "0x") || len(s) < 3 {
		return nil, errors.New("invalid hex quantity")
	}
	v, ok := new(big.Int).SetString(s[2:], 16)
	if !ok || v.Sign() < 0 {
		return nil, errors.New("invalid hex quantity")
	}
	return v, nil
}
func Hex(v *big.Int) string { return "0x" + v.Text(16) }

// RPC is the chain-read boundary. A missing receipt returns nil without error.
type RPC interface {
	Balance(context.Context, uint64, string) (*big.Int, error)
	Call(context.Context, uint64, string, string) (*big.Int, error)
	GasPrice(context.Context, uint64) (*big.Int, error)
	Receipt(context.Context, uint64, string) (*bool, error)
}
type Sender interface {
	SendTransaction(context.Context, string, privy.Transaction) (string, error)
}
type Journal interface {
	AdvanceOnchain(id, from, to, hash string) error
}
type Executor struct {
	RPC      RPC
	Sender   Sender
	Journal  Journal
	WalletID string
	Poll     time.Duration
}

// Execute sends ordered already-verified templates, never retrying an ambiguous
// broadcast. Each hash is durably appended before the first receipt read.
func (x Executor) Execute(ctx context.Context, in journal.OnchainIntent) error {
	if !Allowed(in.ChainID) || !ValidAddress(in.Sender) || len(in.Templates) == 0 {
		return errors.New("unsupported or invalid onchain intent")
	}
	// Reserve for every remaining step before the first broadcast.
	if estimator, ok := x.RPC.(interface {
		Estimate(context.Context, uint64, string, []journal.OnchainTemplate) (*big.Int, error)
	}); ok {
		native, err := x.RPC.Balance(ctx, in.ChainID, in.Sender)
		if err != nil {
			return err
		}
		reserve, err := estimator.Estimate(ctx, in.ChainID, in.Sender, in.Templates)
		if err != nil {
			return err
		}
		if native.Cmp(reserve) < 0 {
			return ErrGas
		}
	}
	for i, t := range in.Templates {
		if !Allowed(t.ChainID) || t.ChainID != in.ChainID || !ValidAddress(t.To) {
			return errors.New("transaction chain or target mismatch")
		}
		value, err := Quantity(t.Value)
		if err != nil {
			return err
		}
		native, err := x.RPC.Balance(ctx, t.ChainID, in.Sender)
		if err != nil {
			return err
		}
		price, err := x.RPC.GasPrice(ctx, t.ChainID)
		if err != nil {
			return err
		}
		reserve := new(big.Int).Mul(price, big.NewInt(500000))
		if estimator, ok := x.RPC.(interface {
			Estimate(context.Context, uint64, string, []journal.OnchainTemplate) (*big.Int, error)
		}); ok {
			reserve, err = estimator.Estimate(ctx, t.ChainID, in.Sender, in.Templates[i:])
			if err != nil {
				return err
			}
			value = new(big.Int)
		}
		if native.Cmp(new(big.Int).Add(reserve, value)) < 0 {
			return ErrGas
		}
		if t.Token != "" {
			if !ValidAddress(t.Token) {
				return errors.New("invalid token")
			}
			required, err := Quantity(t.Amount)
			if err != nil {
				return err
			}
			balance, err := x.RPC.Call(ctx, t.ChainID, t.Token, "0x70a08231000000000000000000000000"+strings.ToLower(in.Sender[2:]))
			if err != nil {
				return err
			}
			if balance.Cmp(required) < 0 {
				return ErrBalance
			}
			if t.Spender != "" {
				if !ValidAddress(t.Spender) {
					return errors.New("invalid spender")
				}
				allowance, err := x.RPC.Call(ctx, t.ChainID, t.Token, "0xdd62ed3e000000000000000000000000"+strings.ToLower(in.Sender[2:])+"000000000000000000000000"+strings.ToLower(t.Spender[2:]))
				if err != nil {
					return err
				}
				if allowance.Cmp(required) < 0 {
					return ErrBalance
				}
			}
		} else if native.Cmp(value) < 0 {
			return ErrBalance
		}
		hash, err := x.Sender.SendTransaction(ctx, x.WalletID, privy.Transaction{To: t.To, Data: t.Data, Value: t.Value, ChainID: t.ChainID})
		if err != nil {
			if jerr := x.Journal.AdvanceOnchain(in.ID, "sending", "unknown", ""); jerr != nil {
				return fmt.Errorf("broadcast ambiguous: %v; journal failure: %w", err, jerr)
			}
			return fmt.Errorf("%w: %v", ErrUnknown, err)
		}
		_, hexErr := hex.DecodeString(strings.TrimPrefix(hash, "0x"))
		if len(hash) != 66 || !strings.HasPrefix(hash, "0x") || hexErr != nil {
			_ = x.Journal.AdvanceOnchain(in.ID, "sending", "unknown", "")
			return ErrUnknown
		}
		if err = x.Journal.AdvanceOnchain(in.ID, "sending", "sent", hash); err != nil {
			return fmt.Errorf("hash %s may have broadcast; journal persistence failed: %w", hash, err)
		}
		result, err := x.Wait(ctx, t.ChainID, hash)
		if err != nil {
			return err
		}
		if !result {
			if err = x.Journal.AdvanceOnchain(in.ID, "sent", "failed", ""); err != nil {
				return err
			}
			return ErrReverted
		}
		next := "confirmed"
		if i < len(in.Templates)-1 {
			next = "sending"
		}
		if err = x.Journal.AdvanceOnchain(in.ID, "sent", next, ""); err != nil {
			return err
		}
	}
	return nil
}
func (x Executor) Wait(ctx context.Context, chain uint64, hash string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	poll := x.Poll
	if poll <= 0 {
		poll = 3 * time.Second
	}
	backoff := poll
	if backoff > time.Second {
		backoff = time.Second
	}
	var lastErr error
	for {
		status, err := x.RPC.Receipt(ctx, chain, hash)
		if err != nil {
			lastErr = err
		} else if status != nil {
			return *status, nil
		} else {
			lastErr = nil
		}
		delay := poll
		if lastErr != nil {
			delay = backoff
			if backoff < 8*time.Second {
				backoff *= 2
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return false, fmt.Errorf("%w: %v", ErrReceiptUpstream, lastErr)
			}
			return false, fmt.Errorf("%w: %v", ErrUnknown, ctx.Err())
		case <-timer.C:
		}
	}
}

// HTTPRPC uses public chain endpoints unless OKU_RPC_URL_<CHAINID> overrides.
type HTTPRPC struct {
	Client *http.Client
	Getenv func(string) string
}

func (r HTTPRPC) call(ctx context.Context, chain uint64, method string, params any, out any) error {
	if method != "eth_chainId" {
		var actual string
		if err := r.call(ctx, chain, "eth_chainId", []any{}, &actual); err != nil {
			return err
		}
		id, err := Quantity(actual)
		if err != nil || !id.IsUint64() || id.Uint64() != chain {
			return errors.New("RPC chain ID differs from requested chain")
		}
	}
	urls := map[uint64]string{42161: "https://arbitrum-one-rpc.publicnode.com", 8453: "https://base-rpc.publicnode.com", 1: "https://ethereum-rpc.publicnode.com"}
	url := urls[chain]
	if !Allowed(chain) {
		return errors.New("unsupported chain")
	}
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if custom := getenv("OKU_RPC_URL_" + strconv.FormatUint(chain, 10)); custom != "" {
		url = custom
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("RPC HTTP %d", resp.StatusCode)
	}
	var wire struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return err
	}
	if wire.Error != nil {
		return errors.New(wire.Error.Message)
	}
	if len(wire.Result) == 0 {
		return errors.New("RPC result missing")
	}
	return json.Unmarshal(wire.Result, out)
}
func (r HTTPRPC) quantity(ctx context.Context, chain uint64, method string, params any) (*big.Int, error) {
	var raw string
	if err := r.call(ctx, chain, method, params, &raw); err != nil {
		return nil, err
	}
	return Quantity(raw)
}
func (r HTTPRPC) Balance(ctx context.Context, c uint64, a string) (*big.Int, error) {
	return r.quantity(ctx, c, "eth_getBalance", []any{a, "latest"})
}
func (r HTTPRPC) GasPrice(ctx context.Context, c uint64) (*big.Int, error) {
	return r.quantity(ctx, c, "eth_gasPrice", []any{})
}
func (r HTTPRPC) Estimate(ctx context.Context, c uint64, from string, templates []journal.OnchainTemplate) (*big.Int, error) {
	fee, err := r.GasPrice(ctx, c)
	if err != nil {
		return nil, err
	}
	var history struct {
		BaseFeePerGas []string `json:"baseFeePerGas"`
	}
	if err = r.call(ctx, c, "eth_feeHistory", []any{"0x1", "latest", []int{50}}, &history); err == nil && len(history.BaseFeePerGas) > 0 {
		base, e := Quantity(history.BaseFeePerGas[len(history.BaseFeePerGas)-1])
		if e == nil {
			priority, e := r.quantity(ctx, c, "eth_maxPriorityFeePerGas", []any{})
			if e == nil {
				cap := new(big.Int).Add(new(big.Int).Mul(base, big.NewInt(2)), priority)
				if cap.Cmp(fee) > 0 {
					fee = cap
				}
			}
		}
	}
	total := new(big.Int)
	for i, t := range templates {
		call := map[string]string{"from": from, "to": t.To, "value": t.Value}
		if t.Data != "" {
			call["data"] = t.Data
		}
		gas, e := r.quantity(ctx, c, "eth_estimateGas", []any{call})
		if e != nil {
			if i == 0 {
				return nil, e
			}
			// Later steps depend on earlier ones (a swap needs its approval mined),
			// so they cannot be simulated yet. Reserve a conservative limit; each
			// step is estimated for real immediately before its own broadcast.
			gas = big.NewInt(dependentStepGas)
		}
		gas.Mul(gas, big.NewInt(130))
		gas.Add(gas, big.NewInt(99))
		gas.Div(gas, big.NewInt(100))
		value, e := Quantity(t.Value)
		if e != nil {
			return nil, e
		}
		total.Add(total, new(big.Int).Add(new(big.Int).Mul(gas, fee), value))
	}
	return total, nil
}
func (r HTTPRPC) Call(ctx context.Context, c uint64, to, data string) (*big.Int, error) {
	return r.quantity(ctx, c, "eth_call", []any{map[string]string{"to": to, "data": data}, "latest"})
}

// Quiescent reports whether nothing from sender after the transaction hash is
// mined or pending: both the latest and pending nonces sit right after it.
func (r HTTPRPC) Quiescent(ctx context.Context, c uint64, sender, hash string) (bool, error) {
	var tx struct {
		From  string `json:"from"`
		Nonce string `json:"nonce"`
	}
	if err := r.call(ctx, c, "eth_getTransactionByHash", []any{hash}, &tx); err != nil {
		return false, err
	}
	nonce, err := Quantity(tx.Nonce)
	if err != nil || !strings.EqualFold(tx.From, sender) {
		return false, errors.New("transaction sender or nonce mismatch")
	}
	next := new(big.Int).Add(nonce, big.NewInt(1))
	for _, tag := range []string{"latest", "pending"} {
		count, err := r.quantity(ctx, c, "eth_getTransactionCount", []any{sender, tag})
		if err != nil {
			return false, err
		}
		if count.Cmp(next) != 0 {
			return false, nil
		}
	}
	return true, nil
}
func (r HTTPRPC) Receipt(ctx context.Context, c uint64, hash string) (*bool, error) {
	var raw json.RawMessage
	if err := r.call(ctx, c, "eth_getTransactionReceipt", []any{hash}, &raw); err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var receipt struct {
		Status          string `json:"status"`
		TransactionHash string `json:"transactionHash"`
		BlockHash       string `json:"blockHash"`
		BlockNumber     string `json:"blockNumber"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	if receipt.Status != "0x1" && receipt.Status != "0x0" {
		return nil, errors.New("invalid receipt status")
	}
	block, err := Quantity(receipt.BlockNumber)
	if err != nil || block.Sign() == 0 || len(receipt.BlockHash) != 66 || !strings.EqualFold(receipt.TransactionHash, hash) || len(receipt.BlockHash) < 2 {
		return nil, errors.New("receipt identity or block missing")
	}
	if _, err = hex.DecodeString(receipt.BlockHash[2:]); err != nil {
		return nil, errors.New("invalid receipt block hash")
	}
	depth := uint64(20)
	if c == 1 {
		depth = 12
	}
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if custom := getenv("OKU_RPC_CONFIRMATIONS_" + strconv.FormatUint(c, 10)); custom != "" {
		n, e := strconv.ParseUint(custom, 10, 64)
		if e != nil || n == 0 {
			return nil, errors.New("invalid confirmation depth")
		}
		depth = n
	}
	latest, err := r.quantity(ctx, c, "eth_blockNumber", []any{})
	if err != nil {
		return nil, err
	}
	if latest.Cmp(block) < 0 || new(big.Int).Sub(latest, block).Cmp(new(big.Int).SetUint64(depth-1)) < 0 {
		return nil, nil
	}
	var canonical struct {
		Hash string `json:"hash"`
	}
	if err := r.call(ctx, c, "eth_getBlockByNumber", []any{Hex(block), false}, &canonical); err != nil {
		return nil, err
	}
	if !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		return nil, errors.New("receipt block is not canonical")
	}
	ok := receipt.Status == "0x1"
	return &ok, nil
}

// VerifyTransaction rejects hashes that do not identify the exact journaled call.
func (r HTTPRPC) VerifyTransaction(ctx context.Context, chain uint64, hash, sender string, t journal.OnchainTemplate) error {
	var tx struct {
		Hash    string `json:"hash"`
		From    string `json:"from"`
		To      string `json:"to"`
		Input   string `json:"input"`
		Value   string `json:"value"`
		ChainID string `json:"chainId"`
	}
	if err := r.call(ctx, chain, "eth_getTransactionByHash", []any{hash}, &tx); err != nil {
		return err
	}
	value, err := Quantity(tx.Value)
	if err != nil {
		return err
	}
	expected, err := Quantity(t.Value)
	if err != nil {
		return err
	}
	if !strings.EqualFold(tx.Hash, hash) || !strings.EqualFold(tx.From, sender) || !strings.EqualFold(tx.To, t.To) || value.Cmp(expected) != 0 || !strings.EqualFold(tx.Input, t.Data) && !(t.Data == "" && tx.Input == "0x") {
		return errors.New("transaction differs from journaled template")
	}
	if tx.ChainID != "" {
		id, err := Quantity(tx.ChainID)
		if err != nil || !id.IsUint64() || id.Uint64() != chain {
			return errors.New("transaction chain mismatch")
		}
	}
	return nil
}
