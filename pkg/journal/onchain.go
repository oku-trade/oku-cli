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
	ID           string            `json:"intent_id"`
	Kind         string            `json:"kind"`
	Intent       bool              `json:"intent_based,omitempty"`
	ChainID      uint64            `json:"chain_id"`
	Sender       string            `json:"sender"`
	Recipient    string            `json:"recipient"`
	Token        string            `json:"token"`
	Amount       string            `json:"amount"`
	Templates    []OnchainTemplate `json:"templates"`
	OrderID      string            `json:"order_id,omitempty"`
	ParentIntent string            `json:"parent_intent,omitempty"`
	QuoteID      string            `json:"quote_id,omitempty"`
	Generation   int32             `json:"generation,omitempty"`
	Route        string            `json:"route,omitempty"`
	Target       string            `json:"target,omitempty"`
	Spender      string            `json:"spender,omitempty"`
	OutToken     string            `json:"out_token,omitempty"`
	OutChain     uint64            `json:"out_chain,omitempty"`
	MinOut       string            `json:"min_out,omitempty"`
	ExpiresAt    string            `json:"expires_at,omitempty"`
	CreatedAt    string            `json:"created_at"`
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
	Digest         string   `json:"digest"`
	State          string   `json:"state"`
	Hashes         []string `json:"tx_hashes"`
	Batch          *Batch   `json:"batch,omitempty"`
	NextStep       string   `json:"next_step,omitempty"`
	BackendOrderID string   `json:"backend_order_id,omitempty"`
	UpdatedAt      string   `json:"updated_at"`
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
	var raw, hashes, digest, state, order, updated, batch string
	err := j.db.QueryRow(`SELECT intent_json,tx_hashes,digest,state,COALESCE(backend_order_id,''),updated_at,COALESCE(batch_json,'') FROM onchain_intents WHERE profile=? AND intent_id=?`, j.binding.Profile, id).Scan(&raw, &hashes, &digest, &state, &order, &updated, &batch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &OnchainRecord{Digest: digest, State: state, BackendOrderID: order, UpdatedAt: updated}
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
	return r, nil
}

// ClaimForBridge finds a previously created claim, including terminal claims.
// The journal lock must be held while checking and creating a claim.
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
		if r.ParentIntent == parent && r.State != "failed" {
			return r, nil
		}
	}
	return nil, ErrNotFound
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

// BeginOnchain atomically checks the agent-wide unresolved gate and marks the intent
// sending before any network call. A sending intent must never be replayed.
func (j *Journal) BeginOnchain(id, digest, agent string) error {
	tx, err := j.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current, stored, sender string
	err = tx.QueryRow(`SELECT state,digest,agent FROM onchain_intents WHERE profile=? AND intent_id=?`, j.binding.Profile, id).Scan(&current, &stored, &sender)
	if err != nil {
		return err
	}
	if stored != digest || !strings.EqualFold(sender, agent) || current != "prepared" {
		return ErrConflict
	}
	var blocked string
	err = tx.QueryRow(`SELECT intent_id FROM onchain_intents WHERE agent=? AND state IN ('sending','sent','unknown') AND NOT(profile=? AND intent_id=?) LIMIT 1`, strings.ToLower(agent), j.binding.Profile, id).Scan(&blocked)
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
	res, err := tx.Exec(`UPDATE onchain_intents SET state=?,tx_hashes=?,updated_at=? WHERE profile=? AND intent_id=? AND state=?`, to, string(b), j.now().UTC().Format(time.RFC3339Nano), j.binding.Profile, id, from)
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
