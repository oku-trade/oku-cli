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
var ErrGas = errors.New("INSUFFICIENT_GAS")
var ErrBalance = errors.New("INSUFFICIENT_BALANCE")
var ErrReverted = errors.New("transaction reverted")

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
		// Conservative fixed reserve (500k gas) prevents signing a transaction the
		// wallet cannot pay for; the wallet/provider chooses the actual gas limit.
		reserve := new(big.Int).Mul(price, big.NewInt(500000))
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
			_ = x.Journal.AdvanceOnchain(in.ID, "sent", "unknown", "")
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
	for {
		status, err := x.RPC.Receipt(ctx, chain, hash)
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrUnknown, err)
		}
		if status != nil {
			return *status, nil
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
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
func (r HTTPRPC) Call(ctx context.Context, c uint64, to, data string) (*big.Int, error) {
	return r.quantity(ctx, c, "eth_call", []any{map[string]string{"to": to, "data": data}, "latest"})
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
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	if receipt.Status != "0x1" && receipt.Status != "0x0" {
		return nil, errors.New("invalid receipt status")
	}
	ok := receipt.Status == "0x1"
	return &ok, nil
}
