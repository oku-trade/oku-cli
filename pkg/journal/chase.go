package journal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

type ChaseState string

const (
	ChaseReady   ChaseState = "ready"
	ChaseResting ChaseState = "resting"
	ChasePending ChaseState = "pending"
	ChaseUnknown ChaseState = "unknown"
	ChaseStopped ChaseState = "stopped"
)

// ChaseRecord keeps the current order and the in-flight child durable before
// signing. Pending children must be reconciled, never replayed on restart.
type ChaseRecord struct {
	IntentID           string     `json:"intent_id"`
	Digest             string     `json:"digest"`
	State              ChaseState `json:"state"`
	OrderID            int64      `json:"order_id,omitempty"`
	OrderIDs           []int64    `json:"order_ids,omitempty"`
	PendingIntent      string     `json:"pending_intent,omitempty"`
	Remaining          string     `json:"remaining"`
	Price              string     `json:"price,omitempty"`
	FilledSize         *string    `json:"filled_size,omitempty"`
	AveragePrice       *string    `json:"average_price,omitempty"`
	CancelledRemainder *string    `json:"cancelled_remainder,omitempty"`
	Reason             string     `json:"reason,omitempty"`
	Revision           int64      `json:"-"`
}

func (c *ChaseRecord) validate() error {
	if !trading.ValidIntentID(c.IntentID) || len(c.Digest) != 64 || !decimal.IsCanonical(c.Remaining) || c.OrderID < 0 {
		return errors.New("invalid chase journal identity or remaining size")
	}
	owned := make(map[int64]bool, len(c.OrderIDs))
	for _, id := range c.OrderIDs {
		if id <= 0 || id > 9007199254740991 || owned[id] {
			return errors.New("invalid chase owned order history")
		}
		owned[id] = true
	}
	if c.OrderID > 0 && !owned[c.OrderID] {
		return errors.New("current chase order is not owned")
	}
	switch c.State {
	case ChaseReady:
		if c.OrderID != 0 || c.PendingIntent != "" {
			return errors.New("ready chase already has an action")
		}
	case ChaseResting:
		if c.OrderID == 0 || c.PendingIntent != "" {
			return errors.New("resting chase needs one owned order")
		}
	case ChasePending:
		if !trading.ValidIntentID(c.PendingIntent) {
			return errors.New("pending chase needs a child intent")
		}
	case ChaseUnknown:
		if c.OrderID == 0 && c.PendingIntent == "" {
			return errors.New("unknown chase has no recoverable action")
		}
	case ChaseStopped:
		if c.PendingIntent != "" {
			return errors.New("stopped chase still has a pending action")
		}
	default:
		return errors.New("invalid chase state")
	}
	return nil
}

func (j *Journal) LockChase(id string) (func(), error) {
	if !trading.ValidIntentID(id) {
		return nil, errors.New("invalid chase id")
	}
	key := sha256.Sum256([]byte(j.binding.Profile + ":" + id))
	return config.TryLockPath(filepath.Join(j.base, fmt.Sprintf("chase-%x.lock", key)))
}

func (j *Journal) CreateChase(parent *Record) (*ChaseRecord, error) {
	stored, err := j.Get(parent.Intent.ID)
	if err != nil {
		return nil, err
	}
	if stored.Digest != parent.Digest || parent.Intent.Digest() != parent.Digest || stored.Intent.Kind != trading.KindChase || stored.Intent.Chase == nil || stored.Intent.Size != stored.Intent.Chase.TotalSize || stored.State == StateAbandoned || stored.State == StateRejected {
		return nil, ErrConflict
	}
	c := &ChaseRecord{IntentID: parent.Intent.ID, Digest: parent.Digest, State: ChaseReady, Remaining: parent.Intent.Size, Revision: 1}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	_, err = j.db.Exec(`INSERT INTO chases(profile,intent_id,revision,payload) VALUES(?,?,?,?) ON CONFLICT(profile,intent_id) DO NOTHING`, j.binding.Profile, c.IntentID, c.Revision, string(raw))
	if err != nil {
		return nil, err
	}
	return j.GetChase(c.IntentID)
}

func scanChase(row interface{ Scan(...any) error }) (*ChaseRecord, error) {
	c := new(ChaseRecord)
	var raw string
	if err := row.Scan(&c.Revision, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), c); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (j *Journal) GetChase(id string) (*ChaseRecord, error) {
	c, err := scanChase(j.db.QueryRow(`SELECT revision,payload FROM chases WHERE profile=? AND intent_id=?`, j.binding.Profile, id))
	if err != nil {
		return nil, err
	}
	if c.IntentID != id {
		return nil, errors.New("chase journal identity mismatch")
	}
	return c, nil
}

func (j *Journal) ListChases() (out []*ChaseRecord, err error) {
	rows, err := j.db.Query(`SELECT revision,payload FROM chases WHERE profile=? ORDER BY intent_id`, j.binding.Profile)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []*ChaseRecord{}
	for rows.Next() {
		c, err := scanChase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func saveChase(tx *writeTx, profile string, c *ChaseRecord) error {
	if err := c.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE chases SET revision=revision+1,payload=? WHERE profile=? AND intent_id=? AND revision=?`, string(raw), profile, c.IntentID, c.Revision)
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
	return nil
}

func (j *Journal) PutChase(c *ChaseRecord) (err error) {
	tx, err := j.begin()
	if err != nil {
		return err
	}
	committed := false
	defer finishChaseTx(tx, &committed, &err)
	if err = saveChase(tx, j.binding.Profile, c); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	c.Revision++
	return nil
}

// CreateChaseChild atomically links an unsigned exact action to its approved
// parent and marks that action pending. No crash window leaves an untracked child.
func (j *Journal) CreateChaseChild(c *ChaseRecord, in trading.Intent) (_ *Record, err error) {
	if !trading.ValidIntentID(in.ID) || c.PendingIntent != "" || c.State == ChaseStopped {
		return nil, errors.New("chase cannot start another action")
	}
	parent, err := j.Get(c.IntentID)
	if err != nil {
		return nil, err
	}
	if parent.Digest != c.Digest || in.Network != parent.Intent.Network || in.Account != parent.Intent.Account || in.APIURL != parent.Intent.APIURL {
		return nil, ErrConflict
	}
	if err = trading.ValidateChaseChild(&parent.Intent, &in, c.OrderID, c.Remaining); err != nil {
		return nil, err
	}
	r := &Record{Intent: in, Digest: in.Digest(), State: StatePrepared, UpdatedAt: j.now().UTC().Format(time.RFC3339Nano)}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	tx, err := j.begin()
	if err != nil {
		return nil, err
	}
	committed := false
	defer finishChaseTx(tx, &committed, &err)
	_, err = tx.Exec(`INSERT INTO records(profile,intent_id,account,network,origin,signer,digest,intent_json,state,updated_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, j.binding.Profile, in.ID, in.Account, in.Network, in.APIURL, j.binding.Signer, r.Digest, string(raw), r.State, r.UpdatedAt, in.CreatedAt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO transitions(profile,intent_id,sequence,at,state,note) VALUES(?,?,1,?,?,?)`, j.binding.Profile, in.ID, r.UpdatedAt, r.State, "chase child"); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO chase_children(profile,child_id,parent_id) VALUES(?,?,?)`, j.binding.Profile, in.ID, c.IntentID); err != nil {
		return nil, err
	}
	next := *c
	next.PendingIntent = in.ID
	next.State = ChasePending
	if err = saveChase(tx, j.binding.Profile, &next); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	next.Revision++
	*c = next
	return r, nil
}

func finishChaseTx(tx *writeTx, committed *bool, err *error) {
	var cleanup error
	if *committed {
		cleanup = tx.conn.Close()
	} else {
		cleanup = tx.Rollback()
	}
	if cleanup != nil {
		*err = errors.Join(*err, fmt.Errorf("closing chase transaction: %w", cleanup))
	}
}

func (j *Journal) ChaseForChild(id string) (*ChaseRecord, error) {
	var parent string
	err := j.db.QueryRow(`SELECT parent_id FROM chase_children WHERE profile=? AND child_id=?`, j.binding.Profile, id).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return j.GetChase(parent)
}
