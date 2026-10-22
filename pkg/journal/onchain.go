package journal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/trading"
)

// OnchainIntent binds every economic field and every ordered transaction template.
type OnchainIntent struct {
	ID             string            `json:"intent_id"`
	Kind           string            `json:"kind"`
	Intent         bool              `json:"intent_based,omitempty"`
	ChainID        uint64            `json:"chain_id"`
	Sender         string            `json:"sender"`
	Recipient      string            `json:"recipient"`
	Token          string            `json:"token"`
	Amount         string            `json:"amount"`
	ResolvedAmount string            `json:"resolved_amount,omitempty"`
	AmountSource   string            `json:"amount_source,omitempty"`
	Templates      []OnchainTemplate `json:"templates"`
	OrderID        string            `json:"order_id,omitempty"`
	ParentIntent   string            `json:"parent_intent,omitempty"`
	QuoteID        string            `json:"quote_id,omitempty"`
	Generation     int32             `json:"generation,omitempty"`
	Route          string            `json:"route,omitempty"`
	Target         string            `json:"target,omitempty"`
	Spender        string            `json:"spender,omitempty"`
	OutToken       string            `json:"out_token,omitempty"`
	OutChain       uint64            `json:"out_chain,omitempty"`
	MinOut         string            `json:"min_out,omitempty"`
	ExpiresAt      string            `json:"expires_at,omitempty"`
	CreatedAt      string            `json:"created_at"`
}
type OnchainTemplate struct {
	ChainID  uint64 `json:"chain_id"`
	To       string `json:"to"`
	Data     string `json:"data"`
	Value    string `json:"value"`
	Token    string `json:"token,omitempty"`
	Amount   string `json:"amount,omitempty"`
	Decimals int    `json:"decimals,omitempty"`
	Spender  string `json:"spender,omitempty"`
}

func (in OnchainIntent) Digest() string {
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type OnchainRecord struct {
	OnchainIntent
	Digest          string   `json:"digest"`
	State           string   `json:"state"`
	Hashes          []string `json:"tx_hashes"`
	Batch           *Batch   `json:"batch,omitempty"`
	NextStep        string   `json:"next_step,omitempty"`
	ExecutionMode   string   `json:"execution_mode,omitempty"`
	StepIndex       int      `json:"step_index,omitempty"`
	StepInFlight    bool     `json:"step_in_flight,omitempty"`
	ApprovalSkipped bool     `json:"approval_skipped,omitempty"`
	BackendOrderID  string   `json:"backend_order_id,omitempty"`
	UpdatedAt       string   `json:"updated_at"`
}

// Batch records an atomic wallet_sendCalls execution. Nonce is the agent's
// confirmed and pending nonce observed before submission: the batch is the
// agent's transaction at exactly that nonce, which lets status and reconcile
// find it on chain without a Privy transaction lookup.
type Batch struct {
	Nonce         uint64 `json:"nonce"`
	FromBlock     uint64 `json:"from_block"`
	TransactionID string `json:"transaction_id,omitempty"`
}

func (j *Journal) CreateOnchain(in OnchainIntent) (*OnchainRecord, error) {
	if !trading.ValidIntentID(in.ID) || len(in.Templates) == 0 && !in.Intent {
		return nil, errors.New("invalid onchain intent")
	}
	b, _ := json.Marshal(in)
	now := j.now().UTC().Format(time.RFC3339Nano)
	tx, err := j.begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO onchain_intents(profile,intent_id,agent,kind,chain_id,sender,recipient,token,amount,digest,intent_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'prepared',?,?)`, j.binding.Profile, in.ID, strings.ToLower(in.Sender), in.Kind, in.ChainID, in.Sender, in.Recipient, in.Token, in.Amount, in.Digest(), string(b), now, now)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &OnchainRecord{OnchainIntent: in, Digest: in.Digest(), State: "prepared", UpdatedAt: now, Hashes: []string{}}, nil
}
func (j *Journal) GetOnchain(id string) (*OnchainRecord, error) {
	if !trading.ValidIntentID(id) {
		return nil, ErrNotFound
	}
	var raw, hashes, digest, state, order, updated, batch, mode string
	var step, inflight, skipped int
	err := j.db.QueryRow(`SELECT intent_json,tx_hashes,digest,state,COALESCE(backend_order_id,''),updated_at,COALESCE(batch_json,''),next_step,execution_mode,step_inflight,approval_skipped FROM onchain_intents WHERE profile=? AND intent_id=?`, j.binding.Profile, id).Scan(&raw, &hashes, &digest, &state, &order, &updated, &batch, &step, &mode, &inflight, &skipped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &OnchainRecord{Digest: digest, State: state, BackendOrderID: order, UpdatedAt: updated, ExecutionMode: mode, StepIndex: step, StepInFlight: inflight != 0, ApprovalSkipped: skipped != 0}
	if err = json.Unmarshal([]byte(raw), &r.OnchainIntent); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(hashes), &r.Hashes); err != nil {
		return nil, err
	}
	if batch != "" {
		r.Batch = &Batch{}
		if err = json.Unmarshal([]byte(batch), r.Batch); err != nil {
			return nil, err
		}
	}
	if r.ID != id || r.OnchainIntent.Digest() != digest {
		return nil, errors.New("corrupt onchain intent digest")
	}
	if mode == "sequential" && step < len(r.Templates) && step > 0 && state == "sending" && inflight == 0 {
		r.NextStep = fmt.Sprintf("step %d confirmed; step %d not sent", step, step+1)
	}
	if mode == "sequential" && inflight != 0 && (state == "sending" || state == "unknown") {
		r.NextStep = "transaction handoff ambiguous; do not resubmit until reconciled"
	}
	return r, nil
}

// MarkStepHandoff commits before calling Privy. A crash after this point is
// ambiguous even when no returned hash was persisted.
func (j *Journal) MarkStepHandoff(id string, step int) error {
	res, err := j.db.Exec(`UPDATE onchain_intents SET step_inflight=1,updated_at=? WHERE profile=? AND intent_id=? AND state='sending' AND execution_mode='sequential' AND next_step=? AND step_inflight=0`, j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id, step)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return ErrConflict
	}
	return nil
}

// SetExecutionMode commits the selected mode before any transaction handoff.
func (j *Journal) SetExecutionMode(id, mode string) error {
	if mode != "batch" && mode != "sequential" {
		return ErrConflict
	}
	res, err := j.db.Exec(`UPDATE onchain_intents SET execution_mode=?,updated_at=? WHERE profile=? AND intent_id=? AND state='sending'`, mode, j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return ErrConflict
	}
	return nil
}

// SkipApproval advances only when a fresh allowance read proves the exact
// approval is unnecessary. No transaction hash is appended.
func (j *Journal) SkipApproval(id string) error {
	res, err := j.db.Exec(`UPDATE onchain_intents SET next_step=1,approval_skipped=1,updated_at=? WHERE profile=? AND intent_id=? AND state='sending' AND execution_mode='sequential' AND next_step=0 AND tx_hashes='[]'`, j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return ErrConflict
	}
	return nil
}

// ClaimForBridge finds a previously created claim, including terminal claims.
// The destination chain lock must be held while checking and creating a claim.
func (j *Journal) ClaimForBridge(parent string) (*OnchainRecord, error) {
	rows, err := j.db.Query(`SELECT intent_id FROM onchain_intents WHERE profile=? AND kind='claim' ORDER BY created_at`, j.binding.Profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		r, err := j.GetOnchain(id)
		if err != nil {
			return nil, err
		}
		if r.ParentIntent == parent {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

// ListOnchainBridges returns this profile's bridge records without holding a
// database cursor while loading individual records.
func (j *Journal) ListOnchainBridges() ([]*OnchainRecord, error) {
	rows, err := j.db.Query(`SELECT intent_id FROM onchain_intents WHERE profile=? AND kind='bridge' ORDER BY created_at,intent_id`, j.binding.Profile)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]*OnchainRecord, 0, len(ids))
	for _, id := range ids {
		r, e := j.GetOnchain(id)
		if e != nil {
			return nil, e
		}
		result = append(result, r)
	}
	return result, nil
}

// SetOnchainBatch durably records batch metadata while the intent is in state.
// The pre-submission record (nonce, block) must commit before wallet_sendCalls.
func (j *Journal) SetOnchainBatch(id, state string, b Batch) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	res, err := j.db.Exec(`UPDATE onchain_intents SET batch_json=?,updated_at=? WHERE profile=? AND intent_id=? AND state=?`, string(raw), j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id, state)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	return nil
}

// BeginOnchain atomically checks the agent-and-chain unresolved gate and marks the intent
// sending before any network call. A sending intent must never be replayed.
func (j *Journal) BeginOnchain(id, digest, agent string) error {
	tx, err := j.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current, stored, sender string
	var chain uint64
	err = tx.QueryRow(`SELECT state,digest,agent,chain_id FROM onchain_intents WHERE profile=? AND intent_id=?`, j.binding.Profile, id).Scan(&current, &stored, &sender, &chain)
	if err != nil {
		return err
	}
	if stored != digest || !strings.EqualFold(sender, agent) || current != "prepared" {
		return ErrConflict
	}
	var blocked string
	err = tx.QueryRow(`SELECT intent_id FROM onchain_intents WHERE agent=? AND chain_id=? AND state IN ('sending','sent','unknown') AND NOT(profile=? AND intent_id=?) LIMIT 1`, strings.ToLower(agent), chain, j.binding.Profile, id).Scan(&blocked)
	if err == nil {
		return fmt.Errorf("%w: onchain intent %s", ErrUnresolved, blocked)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := j.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.Exec(`UPDATE onchain_intents SET state='sending',updated_at=? WHERE profile=? AND intent_id=? AND state='prepared'`, now, j.binding.Profile, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// BindOnchainExecution replaces placeholder templates only while prepared and
// atomically seals the final, verified transactions under a fresh digest.
func (j *Journal) BindOnchainExecution(id, digest string, templates []OnchainTemplate) (string, error) {
	r, err := j.GetOnchain(id)
	if err != nil {
		return "", err
	}
	if r.State != "prepared" || r.Digest != digest {
		return "", ErrConflict
	}
	if len(templates) == 0 && !r.Intent {
		return "", ErrConflict
	}
	r.Templates = templates
	b, err := json.Marshal(r.OnchainIntent)
	if err != nil {
		return "", err
	}
	newDigest := r.OnchainIntent.Digest()
	tx, err := j.begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE onchain_intents SET intent_json=?,digest=?,updated_at=? WHERE profile=? AND intent_id=? AND digest=? AND state='prepared'`, string(b), newDigest, j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id, digest)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return "", ErrConflict
	}
	return newDigest, tx.Commit()
}

// RelayOnchain stores the backend's offchain order identifier and closes the
// source submission state. A missing response must instead remain unknown.
func (j *Journal) RelayOnchain(id, from, orderID string) error {
	if orderID == "" {
		return ErrConflict
	}
	res, err := j.db.Exec(`UPDATE onchain_intents SET state='submitted',backend_order_id=?,updated_at=? WHERE profile=? AND intent_id=? AND state=?`, orderID, j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id, from)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	return nil
}

// AdvanceOnchain is a durable transition. Hash append commits before receipt polling.
func (j *Journal) AdvanceOnchain(id, from, to, hash string) error {
	tx, err := j.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRow(`SELECT tx_hashes FROM onchain_intents WHERE profile=? AND intent_id=? AND state=?`, j.binding.Profile, id, from).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	var hashes []string
	if err = json.Unmarshal([]byte(raw), &hashes); err != nil {
		return err
	}
	if hash != "" {
		hashes = append(hashes, hash)
	}
	b, _ := json.Marshal(hashes)
	stepIncrement := 0
	if (from == "sent" || from == "unknown") && to == "sending" {
		stepIncrement = 1
	}
	clearInflight := 0
	if from == "sent" || to == "sending" || to == "failed" || to == "confirmed" {
		clearInflight = 1
	}
	res, err := tx.Exec(`UPDATE onchain_intents SET state=?,tx_hashes=?,next_step=next_step+?,step_inflight=CASE WHEN ?=1 THEN 0 ELSE step_inflight END,updated_at=? WHERE profile=? AND intent_id=? AND state=?`, to, string(b), stepIncrement, clearInflight, j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id, from)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}
