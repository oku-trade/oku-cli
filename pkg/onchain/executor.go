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

// BatchSender submits an atomic wallet_sendCalls batch and returns Privy's
// transaction ID.
type BatchSender interface {
	SendCalls(context.Context, string, uint64, []privy.Call) (string, error)
}

// BatchLocator finds the agent's transaction at a nonce on chain.
type BatchLocator interface {
	Nonces(ctx context.Context, chain uint64, sender string) (latest, pending uint64, err error)
	BlockNumber(ctx context.Context, chain uint64) (uint64, error)
	FindNonceTx(ctx context.Context, chain uint64, sender string, nonce, fromBlock uint64) (*ChainTx, error)
}

// ChainTx is the subset of an on-chain transaction used to verify a batch.
type ChainTx struct {
	Hash, From, To, Input, Value string
	Nonce                        uint64
}
type Journal interface {
	AdvanceOnchain(id, from, to, hash string) error
	SetOnchainBatch(id, state string, b journal.Batch) error
}
type Executor struct {
	RPC      RPC
	Sender   Sender
	Journal  Journal
	WalletID string
	Poll     time.Duration
}

// ErrNotBroadcast wraps failures that provably happened before the current
// step was handed to Privy. Combined with the journal state it tells callers
// whether nothing (state "failed", no hashes) or only earlier, confirmed steps
// reached the chain.
var ErrNotBroadcast = errors.New("not broadcast")

// preflight wraps a pre-broadcast failure. For the first step the executor
// closes the intent; for later steps the earlier steps already confirmed, so
// the intent is closed as failed with those hashes kept for the record.
func (x Executor) preflight(in journal.OnchainIntent, from string, err error) error {
	if jerr := x.Journal.AdvanceOnchain(in.ID, from, "failed", ""); jerr != nil {
		// The closure was not recorded, so the intent still blocks and callers
		// must not be told it is safe to prepare a new one.
		return fmt.Errorf("%w: preflight failed (%v) but journal update failed: %v", ErrUnknown, err, jerr)
	}
	return fmt.Errorf("%w: %w", ErrNotBroadcast, err)
}

// Execute sends ordered already-verified templates, never retrying an ambiguous
// broadcast. Each hash is durably appended before the first receipt read.
// Every failure before a step is handed to Privy closes the intent as failed
// and is wrapped in ErrNotBroadcast; after a handoff, ambiguity stays unknown.
func (x Executor) Execute(ctx context.Context, in journal.OnchainIntent) error {
	if !Allowed(in.ChainID) || !ValidAddress(in.Sender) || len(in.Templates) == 0 {
		return x.preflight(in, "sending", errors.New("unsupported or invalid onchain intent"))
	}
	// Reserve for every remaining step before the first broadcast.
	if estimator, ok := x.RPC.(interface {
		Estimate(context.Context, uint64, string, []journal.OnchainTemplate) (*big.Int, error)
	}); ok {
		native, err := x.RPC.Balance(ctx, in.ChainID, in.Sender)
		if err != nil {
			return x.preflight(in, "sending", err)
		}
		reserve, err := estimator.Estimate(ctx, in.ChainID, in.Sender, in.Templates)
		if err != nil {
			return x.preflight(in, "sending", err)
		}
		if native.Cmp(reserve) < 0 {
			return x.preflight(in, "sending", ErrGas)
		}
	}
	for i, t := range in.Templates {
		if err := x.checkStep(ctx, in, t, in.Templates[i:]); err != nil {
			return x.preflight(in, "sending", err)
		}
		hash, err := x.Sender.SendTransaction(ctx, x.WalletID, privy.Transaction{To: t.To, Data: t.Data, Value: t.Value, ChainID: t.ChainID})
		if err != nil {
			if jerr := x.Journal.AdvanceOnchain(in.ID, "sending", "unknown", ""); jerr != nil {
				return fmt.Errorf("%w: broadcast ambiguous: %v; journal failure: %v", ErrUnknown, err, jerr)
			}
			return fmt.Errorf("%w: %v", ErrUnknown, err)
		}
		_, hexErr := hex.DecodeString(strings.TrimPrefix(hash, "0x"))
		if len(hash) != 66 || !strings.HasPrefix(hash, "0x") || hexErr != nil {
			_ = x.Journal.AdvanceOnchain(in.ID, "sending", "unknown", "")
			return ErrUnknown
		}
		if err = x.Journal.AdvanceOnchain(in.ID, "sending", "sent", hash); err != nil {
			return fmt.Errorf("%w: hash %s may have broadcast; journal persistence failed: %v", ErrUnknown, hash, err)
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

// batchNonceScan bounds how many agent nonces after the reserved one are
// searched for the batch, covering concurrent sends from the same wallet.
const batchNonceScan = 32

// batchBlockMargin backs the recorded search start off from the observed head
// to tolerate load-balanced RPC nodes that lag each other.
const batchBlockMargin = 64

// ErrNonceBeforeBlock reports that a nonce was consumed before the batch's
// recorded start block, so that transaction cannot be the batch.
var ErrNonceBeforeBlock = errors.New("nonce used before the batch was submitted")

// ExecuteBatch sends every template as one atomic wallet_sendCalls batch
// (EIP-7702). Before the handoff it records the agent nonce and block, so the
// batch can be found on chain later; Privy returns no chain hash. Failures
// before the handoff close the intent (ErrNotBroadcast); afterwards the batch
// is located by nonce, its calldata is checked to contain every journaled
// call, and its receipt decides confirmed or failed.
func (x Executor) ExecuteBatch(ctx context.Context, in journal.OnchainIntent, sender BatchSender, locator BatchLocator) error {
	if !Allowed(in.ChainID) || !ValidAddress(in.Sender) || len(in.Templates) == 0 {
		return x.preflight(in, "sending", errors.New("unsupported or invalid onchain intent"))
	}
	calls := make([]privy.Call, 0, len(in.Templates))
	for _, t := range in.Templates {
		if t.ChainID != in.ChainID || !ValidAddress(t.To) {
			return x.preflight(in, "sending", errors.New("transaction chain or target mismatch"))
		}
		calls = append(calls, privy.Call{To: t.To, Value: t.Value, Data: t.Data})
	}
	if err := x.checkBatch(ctx, in); err != nil {
		return x.preflight(in, "sending", err)
	}
	// Read the block first, then the nonce, and back the block off by a margin
	// for load-balanced RPC lag: the batch can only be mined after both reads,
	// so it is always found at or after FromBlock.
	block, err := locator.BlockNumber(ctx, in.ChainID)
	if err != nil {
		return x.preflight(in, "sending", err)
	}
	latest, pending, err := locator.Nonces(ctx, in.ChainID, in.Sender)
	if err != nil {
		return x.preflight(in, "sending", err)
	}
	if latest != pending {
		return x.preflight(in, "sending", errors.New("agent has a pending transaction; wait for it to confirm"))
	}
	if block > batchBlockMargin {
		block -= batchBlockMargin
	} else {
		block = 0
	}
	batch := journal.Batch{Nonce: latest, FromBlock: block}
	if err = x.Journal.SetOnchainBatch(in.ID, "sending", batch); err != nil {
		return x.preflight(in, "sending", err)
	}
	id, err := sender.SendCalls(ctx, x.WalletID, in.ChainID, calls)
	if err != nil {
		if jerr := x.Journal.AdvanceOnchain(in.ID, "sending", "unknown", ""); jerr != nil {
			return fmt.Errorf("%w: batch ambiguous: %v; journal failure: %v", ErrUnknown, err, jerr)
		}
		return fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	batch.TransactionID = id
	_ = x.Journal.SetOnchainBatch(in.ID, "sending", batch) // informational only
	hash, err := x.WaitBatch(ctx, in, batch, locator)
	if err != nil {
		if jerr := x.Journal.AdvanceOnchain(in.ID, "sending", "unknown", ""); jerr != nil {
			return fmt.Errorf("%w: %v; journal failure: %v", ErrUnknown, err, jerr)
		}
		return err
	}
	if err = x.Journal.AdvanceOnchain(in.ID, "sending", "sent", hash); err != nil {
		return fmt.Errorf("%w: batch %s found; journal persistence failed: %v", ErrUnknown, hash, err)
	}
	ok, err := x.Wait(ctx, in.ChainID, hash)
	if err != nil {
		return err
	}
	next := "confirmed"
	if !ok {
		next = "failed"
	}
	if err = x.Journal.AdvanceOnchain(in.ID, "sent", next, ""); err != nil {
		return err
	}
	if !ok {
		return ErrReverted
	}
	return nil
}

// WaitBatch polls until the agent's batch transaction is mined and returns its
// hash. It never treats absence as failure.
func (x Executor) WaitBatch(ctx context.Context, in journal.OnchainIntent, b journal.Batch, locator BatchLocator) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	poll := x.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		hash, err := LocateBatch(ctx, in, b, locator)
		if err != nil && !errors.Is(err, errBatchNotMined) {
			return "", fmt.Errorf("%w: %v", ErrUnknown, err)
		}
		if err == nil {
			return hash, nil
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("%w: batch not yet mined: %v", ErrUnknown, ctx.Err())
		case <-timer.C:
		}
	}
}

var errBatchNotMined = errors.New("batch not mined")

// IsBatchNotMined reports a lookup that found no matching mined batch yet.
func IsBatchNotMined(err error) bool { return errors.Is(err, errBatchNotMined) }

// VerifyBatchTransaction checks a user-supplied hash is the intent's batch.
func (r HTTPRPC) VerifyBatchTransaction(ctx context.Context, in journal.OnchainIntent, b journal.Batch, hash string) error {
	var tx struct {
		Hash  string `json:"hash"`
		From  string `json:"from"`
		To    string `json:"to"`
		Input string `json:"input"`
		Value string `json:"value"`
		Nonce string `json:"nonce"`
	}
	if err := r.call(ctx, in.ChainID, "eth_getTransactionByHash", []any{hash}, &tx); err != nil {
		return err
	}
	nonce, err := Quantity(tx.Nonce)
	if err != nil || !nonce.IsUint64() || nonce.Uint64() < b.Nonce || nonce.Uint64() >= b.Nonce+batchNonceScan {
		return errors.New("transaction nonce is outside this batch's reserved range")
	}
	if !strings.EqualFold(tx.Hash, hash) || !BatchMatches(in, &ChainTx{Hash: tx.Hash, From: tx.From, To: tx.To, Input: tx.Input, Value: tx.Value, Nonce: nonce.Uint64()}) {
		return errors.New("transaction is not this intent's batch")
	}
	return nil
}

// LocateBatch returns the hash of the mined agent transaction carrying the
// intent's batch, searching from the reserved nonce. errBatchNotMined means
// no matching transaction is mined yet; any other error is a lookup failure
// or a mismatch, both of which must leave the intent unresolved.
func LocateBatch(ctx context.Context, in journal.OnchainIntent, b journal.Batch, locator BatchLocator) (string, error) {
	latest, _, err := locator.Nonces(ctx, in.ChainID, in.Sender)
	if err != nil {
		return "", err
	}
	for n := b.Nonce; n < latest && n < b.Nonce+batchNonceScan; n++ {
		tx, err := locator.FindNonceTx(ctx, in.ChainID, in.Sender, n, b.FromBlock)
		if errors.Is(err, ErrNonceBeforeBlock) {
			continue // a concurrent transaction took this nonce first
		}
		if err != nil {
			return "", err
		}
		if tx != nil && BatchMatches(in, tx) {
			return tx.Hash, nil
		}
	}
	return "", errBatchNotMined
}

// kernelExecute is ERC-7579 execute(bytes32 mode, bytes executionCalldata),
// which Privy's EIP-7702 Kernel account uses for wallet_sendCalls.
const kernelExecute = "e9ae5c53"

// kernelBatchMode is ERC-7579 mode: callType batch (0x01), execType default
// (0x00, reverts the whole batch if any call fails), no selector or payload.
const kernelBatchMode = "01" + "00000000000000000000000000000000000000000000000000000000000000"

// BatchMatches reports whether an on-chain transaction is exactly the agent's
// batch: a zero-value self-call to execute(batch mode, abi.encode(Execution[]))
// whose decoded calls equal the journaled templates in order (target, value,
// calldata) with nothing extra.
func BatchMatches(in journal.OnchainIntent, tx *ChainTx) bool {
	if tx == nil || !strings.EqualFold(tx.From, in.Sender) || !strings.EqualFold(tx.To, in.Sender) {
		return false
	}
	if tx.Value != "" {
		if v, err := Quantity(tx.Value); err != nil || v.Sign() != 0 {
			return false
		}
	}
	calls, ok := decodeKernelBatch(tx.Input)
	if !ok || len(calls) != len(in.Templates) {
		return false
	}
	for i, t := range in.Templates {
		want, err := Quantity(t.Value)
		if err != nil || !strings.EqualFold(calls[i].To, t.To) || calls[i].Value.Cmp(want) != 0 || !strings.EqualFold(calls[i].Data, strings.TrimPrefix(t.Data, "0x")) {
			return false
		}
	}
	return true
}

type kernelCall struct {
	To    string
	Value *big.Int
	Data  string // hex without 0x
}

// decodeKernelBatch strictly decodes execute(bytes32,bytes) in batch mode with
// executionCalldata = abi.encode((address,uint256,bytes)[]). It rejects any
// non-canonical layout or trailing bytes.
func decodeKernelBatch(input string) ([]kernelCall, bool) {
	raw, err := hex.DecodeString(strings.TrimPrefix(input, "0x"))
	if err != nil || len(raw) < 4+64 || hex.EncodeToString(raw[:4]) != kernelExecute {
		return nil, false
	}
	args := raw[4:]
	word := func(b []byte, off uint64) (*big.Int, bool) {
		if off+32 > uint64(len(b)) {
			return nil, false
		}
		return new(big.Int).SetBytes(b[off : off+32]), true
	}
	small := func(b []byte, off uint64) (uint64, bool) {
		v, ok := word(b, off)
		if !ok || !v.IsUint64() || v.Uint64() > uint64(len(b)) {
			return 0, false
		}
		return v.Uint64(), true
	}
	if hex.EncodeToString(args[:32]) != kernelBatchMode {
		return nil, false
	}
	// bytes executionCalldata: offset must be 0x40, then length, then data.
	if off, ok := small(args, 32); !ok || off != 64 {
		return nil, false
	}
	n, ok := small(args, 64)
	if !ok || 96+n > uint64(len(args)) || uint64(len(args)) != 96+(n+31)/32*32 {
		return nil, false
	}
	exec := args[96 : 96+n]
	// abi.encode(Execution[]): head offset 0x20, then array length.
	if off, ok := small(exec, 0); !ok || off != 32 {
		return nil, false
	}
	count, ok := small(exec, 32)
	if !ok || count == 0 || count > 16 {
		return nil, false
	}
	arr := exec[64:]
	calls := make([]kernelCall, 0, count)
	end := uint64(32 * count)
	for i := uint64(0); i < count; i++ {
		off, ok := small(arr, 32*i)
		if !ok || off != end {
			return nil, false // elements must be contiguous and in order
		}
		el := arr[off:]
		if len(el) < 96 {
			return nil, false
		}
		if !allZero(el[:12]) {
			return nil, false
		}
		value, _ := word(el, 32)
		dataOff, ok := small(el, 64)
		if !ok || dataOff != 96 {
			return nil, false
		}
		dlen, ok := small(el, 96)
		if !ok || 128+dlen > uint64(len(el)) {
			return nil, false
		}
		padded := (dlen + 31) / 32 * 32
		if 128+padded > uint64(len(el)) || !allZero(el[128+dlen:128+padded]) {
			return nil, false
		}
		calls = append(calls, kernelCall{To: "0x" + hex.EncodeToString(el[12:32]), Value: value, Data: hex.EncodeToString(el[128 : 128+dlen])})
		end = off + 128 + padded
	}
	if end != uint64(len(arr)) || !allZero(args[96+n:]) {
		return nil, false
	}
	return calls, true
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// checkBatch verifies gas and token balances for the whole batch. Approvals
// inside the batch take effect atomically, so allowance is not pre-checked.
func (x Executor) checkBatch(ctx context.Context, in journal.OnchainIntent) error {
	native, err := x.RPC.Balance(ctx, in.ChainID, in.Sender)
	if err != nil {
		return err
	}
	price, err := x.RPC.GasPrice(ctx, in.ChainID)
	if err != nil {
		return err
	}
	reserve := new(big.Int).Mul(price, big.NewInt(int64(dependentStepGas)*int64(len(in.Templates))))
	if estimator, ok := x.RPC.(interface {
		Estimate(context.Context, uint64, string, []journal.OnchainTemplate) (*big.Int, error)
	}); ok {
		if reserve, err = estimator.Estimate(ctx, in.ChainID, in.Sender, in.Templates); err != nil {
			return err
		}
	}
	value := new(big.Int)
	tokens := map[string]*big.Int{}
	for _, t := range in.Templates {
		v, err := Quantity(t.Value)
		if err != nil {
			return err
		}
		value.Add(value, v)
		if t.Token != "" && t.Spender != "" {
			amount, err := Quantity(t.Amount)
			if err != nil || !ValidAddress(t.Token) {
				return errors.New("invalid token amount")
			}
			tokens[strings.ToLower(t.Token)] = amount
		}
	}
	if native.Cmp(new(big.Int).Add(reserve, value)) < 0 {
		return ErrGas
	}
	for token, required := range tokens {
		balance, err := x.RPC.Call(ctx, in.ChainID, token, "0x70a08231000000000000000000000000"+strings.ToLower(in.Sender[2:]))
		if err != nil {
			return err
		}
		if balance.Cmp(required) < 0 {
			return ErrBalance
		}
	}
	return nil
}

// checkStep runs the pre-broadcast gas, balance and allowance checks for one
// step. remaining is the step plus everything after it.
func (x Executor) checkStep(ctx context.Context, in journal.OnchainIntent, t journal.OnchainTemplate, remaining []journal.OnchainTemplate) error {
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
		reserve, err = estimator.Estimate(ctx, t.ChainID, in.Sender, remaining)
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

// Nonces returns the agent's confirmed and pending transaction counts.
func (r HTTPRPC) Nonces(ctx context.Context, c uint64, sender string) (uint64, uint64, error) {
	var out [2]uint64
	for i, tag := range []string{"latest", "pending"} {
		n, err := r.quantity(ctx, c, "eth_getTransactionCount", []any{sender, tag})
		if err != nil {
			return 0, 0, err
		}
		if !n.IsUint64() {
			return 0, 0, errors.New("invalid nonce")
		}
		out[i] = n.Uint64()
	}
	return out[0], out[1], nil
}

// BlockNumber returns the latest block number.
func (r HTTPRPC) BlockNumber(ctx context.Context, c uint64) (uint64, error) {
	n, err := r.quantity(ctx, c, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	if !n.IsUint64() {
		return 0, errors.New("invalid block number")
	}
	return n.Uint64(), nil
}

// FindNonceTx returns sender's mined transaction with the given nonce, found by
// binary-searching the block where the account nonce passed it (from fromBlock).
// It returns nil when that nonce is not mined yet. Requires an RPC that serves
// historical account state for the searched range.
func (r HTTPRPC) FindNonceTx(ctx context.Context, c uint64, sender string, nonce, fromBlock uint64) (*ChainTx, error) {
	countAt := func(block uint64) (uint64, error) {
		n, err := r.quantity(ctx, c, "eth_getTransactionCount", []any{sender, Hex(new(big.Int).SetUint64(block))})
		if err != nil {
			return 0, err
		}
		return n.Uint64(), nil
	}
	hi, err := r.BlockNumber(ctx, c)
	if err != nil {
		return nil, err
	}
	if n, err := countAt(hi); err != nil || n <= nonce {
		return nil, err
	}
	lo := fromBlock
	if lo > 0 {
		lo--
	}
	if n, err := countAt(lo); err != nil {
		return nil, err
	} else if n > nonce {
		return nil, ErrNonceBeforeBlock
	}
	// Invariant: count(lo) <= nonce < count(hi); the transaction is in the first
	// block whose count exceeds nonce.
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		n, err := countAt(mid)
		if err != nil {
			return nil, err
		}
		if n > nonce {
			hi = mid
		} else {
			lo = mid
		}
	}
	var blk struct {
		Transactions []struct {
			Hash  string `json:"hash"`
			From  string `json:"from"`
			To    string `json:"to"`
			Input string `json:"input"`
			Value string `json:"value"`
			Nonce string `json:"nonce"`
		} `json:"transactions"`
	}
	if err := r.call(ctx, c, "eth_getBlockByNumber", []any{Hex(new(big.Int).SetUint64(hi)), true}, &blk); err != nil {
		return nil, err
	}
	for _, tx := range blk.Transactions {
		n, err := Quantity(tx.Nonce)
		if err != nil || !strings.EqualFold(tx.From, sender) || !n.IsUint64() || n.Uint64() != nonce {
			continue
		}
		return &ChainTx{Hash: tx.Hash, From: tx.From, To: tx.To, Input: tx.Input, Value: tx.Value, Nonce: nonce}, nil
	}
	return nil, errors.New("nonce transaction missing from its block")
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
