// Package journal persists intent lifecycle and signer-global nonces without secrets.
package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/trading"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const (
	StatePrepared   = "prepared"
	StateCreating   = "creating"
	StateCreated    = "created"
	StateSubmitting = "submitting"
	StateSigning    = "signing"
	StateSubmitted  = "submitted"
	StateUnknown    = "unknown"
	StateRejected   = "rejected"
	StateAbandoned  = "abandoned"
)

type Record struct {
	Intent      trading.Intent `json:"intent"`
	Digest      string         `json:"digest"`
	State       string         `json:"state"`
	ActionID    string         `json:"action_id,omitempty"`
	ActionJSON  string         `json:"action_json,omitempty"`
	Cloid       string         `json:"cloid,omitempty"`
	Nonce       int64          `json:"nonce,omitempty"`
	ServerState string         `json:"server_state,omitempty"`
	OrderID     int64          `json:"order_id,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
	UpdatedAt   string         `json:"updated_at"`
	History     []Transition   `json:"history"`
}
type Transition struct {
	At    string `json:"at"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

func (r *Record) Terminal() bool {
	switch r.State {
	case StateRejected, StateAbandoned:
		return true
	case StateSubmitted:
		switch r.ServerState {
		case "completed", "filled", "cancelled", "canceled", "error":
			return true
		}
	}
	return false
}

var ErrNotFound = errors.New("intent not found")
var ErrConflict = errors.New("intent id already used with a different payload")
var ErrUnresolved = errors.New("account has unresolved submission")

// Store is the durable journal interface. Operations are short transactions, never network-spanning.
type Store interface {
	Create(trading.Intent) (*Record, bool, error)
	Get(string) (*Record, error)
	Put(*Record, string) error
	List() ([]*Record, error)
	Unresolved() ([]*Record, error)
	NextNonce(string) (int64, error)
	BeginSubmission(*Record, string) (int64, error)
	Blocking(string, string) ([]*Record, error)
	Close() error
}

type Binding struct{ Profile, Network, Account, Origin, Signer string }
type Journal struct {
	db      *sql.DB
	base    string
	binding Binding
	now     func() time.Time
}

type writeTx struct {
	conn *sql.Conn
	ctx  context.Context
}

func (j *Journal) begin() (*writeTx, error) {
	ctx := context.Background()
	conn, err := j.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		return nil, err
	}
	return &writeTx{conn: conn, ctx: ctx}, nil
}
func (t *writeTx) QueryRow(q string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(t.ctx, q, args...)
}
func (t *writeTx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(t.ctx, q, args...)
}
func (t *writeTx) Exec(q string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(t.ctx, q, args...)
}
func (t *writeTx) Commit() error { _, err := t.conn.ExecContext(t.ctx, "COMMIT"); return err }
func (t *writeTx) Rollback() error {
	_, err := t.conn.ExecContext(t.ctx, "ROLLBACK")
	closeErr := t.conn.Close()
	if err != nil {
		return err
	}
	return closeErr
}

//go:embed migrations/*.sql
var migrations embed.FS

func Open(profile string) (*Journal, error) { return OpenBinding(Binding{Profile: profile}) }
func OpenBinding(b Binding) (*Journal, error) {
	if !config.ValidName(b.Profile) {
		return nil, fmt.Errorf("invalid profile %q", b.Profile)
	}
	base, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(base, 0700); err != nil {
		return nil, err
	}
	db, jerr := openDB(base)
	if jerr != nil {
		return nil, jerr
	}
	return &Journal{db: db, base: base, binding: b, now: time.Now}, nil
}
func openDB(base string) (*sql.DB, error) {
	path := filepath.Join(base, "journal.sqlite")
	if st, e := os.Lstat(path); e == nil && !st.Mode().IsRegular() {
		return nil, fmt.Errorf("journal database path is not a regular file")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	pragmas := []string{"PRAGMA busy_timeout=15000", "PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL"}
	if runtime.GOOS == "darwin" {
		pragmas = append(pragmas, "PRAGMA fullfsync=ON", "PRAGMA checkpoint_fullfsync=ON")
	}
	for _, pragma := range pragmas {
		if _, err = db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	providerFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		db.Close()
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, providerFS)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err = provider.Up(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		if _, e := os.Stat(p); e == nil {
			if e = os.Chmod(p, 0600); e != nil {
				db.Close()
				return nil, e
			}
		}
	}
	if err = syncDir(base); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func syncDir(path string) error {
	d, e := os.Open(path)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (j *Journal) Close() error { return j.db.Close() }

// Lock remains compatible with existing callers, but should not span network requests.
func (j *Journal) Lock() (func(), error) {
	return j.lockFile("journal.lock")
}

// LockChain serializes transactions for one agent on one EVM chain across processes.
func (j *Journal) LockChain(agent string, chain uint64) (func(), error) {
	if agent == "" || chain == 0 {
		return nil, errors.New("agent and chain required for chain lock")
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", strings.ToLower(agent), chain)))
	return j.lockFile(fmt.Sprintf("chain-%x.lock", key))
}

func (j *Journal) lockFile(name string) (func(), error) {
	return config.LockPath(filepath.Join(j.base, name))
}
func (j *Journal) Get(id string) (*Record, error) {
	if !trading.ValidIntentID(id) {
		return nil, fmt.Errorf("invalid intent id %q", id)
	}
	return load(j.db, j.binding.Profile, id)
}

type querier interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

func load(q querier, profile, id string) (*Record, error) {
	var raw string
	r := new(Record)
	err := q.QueryRow(`SELECT intent_json,digest,state,action_id,action_json,cloid,nonce,server_state,order_id,last_error,updated_at FROM records WHERE profile=? AND intent_id=?`, profile, id).Scan(&raw, &r.Digest, &r.State, &r.ActionID, &r.ActionJSON, &r.Cloid, &r.Nonce, &r.ServerState, &r.OrderID, &r.LastError, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &r.Intent); err != nil {
		return nil, fmt.Errorf("corrupt intent %s: %w", id, err)
	}
	if r.Intent.ID != id || r.Intent.Digest() != r.Digest {
		return nil, fmt.Errorf("journal record %s digest mismatch", id)
	}
	rows, err := q.Query(`SELECT at,state,note FROM transitions WHERE profile=? AND intent_id=? ORDER BY sequence`, profile, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Transition
		if err = rows.Scan(&t.At, &t.State, &t.Note); err != nil {
			return nil, err
		}
		r.History = append(r.History, t)
	}
	return r, rows.Err()
}
func (j *Journal) Create(in trading.Intent) (*Record, bool, error) {
	if !trading.ValidIntentID(in.ID) {
		return nil, false, fmt.Errorf("invalid intent id %q", in.ID)
	}
	if j.binding.Network != "" && (in.Network != j.binding.Network || !strings.EqualFold(in.Account, j.binding.Account) || in.APIURL != j.binding.Origin) {
		return nil, false, errors.New("intent does not match journal binding")
	}
	r := &Record{Intent: in, Digest: in.Digest(), State: StatePrepared}
	raw, _ := json.Marshal(in)
	ts := j.now().UTC().Format(time.RFC3339Nano)
	tx, err := j.begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO records(profile,intent_id,account,network,origin,signer,digest,intent_json,state,updated_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(profile,intent_id) DO NOTHING`, j.binding.Profile, in.ID, strings.ToLower(in.Account), in.Network, in.APIURL, strings.ToLower(j.binding.Signer), r.Digest, string(raw), r.State, ts, in.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n == 0 {
		existing, e := load(tx, j.binding.Profile, in.ID)
		if e != nil {
			return nil, false, e
		}
		if existing.Digest != r.Digest {
			return existing, false, ErrConflict
		}
		return existing, false, nil
	}
	r.UpdatedAt = ts
	r.History = []Transition{{At: ts, State: r.State}}
	if _, err = tx.Exec(`INSERT INTO transitions(profile,intent_id,sequence,at,state,note) VALUES(?,?,1,?,?,?)`, j.binding.Profile, in.ID, ts, r.State, ""); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return r, true, nil
}
func (j *Journal) Put(r *Record, note string) error { return j.put(r, note, false) }

// RejectDefinite persists a submitting record as rejected on the strength of
// a definite, non-ambiguous RPC rejection (a Connect error code the API
// client classifies as returned before the handler acted, e.g.
// invalid_argument or permission_denied) rather than an observed server
// state. It only applies to a record currently submitting with the same
// nonce, and the reason is recorded in last_error and the transition note.
// Ambiguous outcomes must instead be persisted as unknown.
func (j *Journal) RejectDefinite(r *Record, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("definite rejection requires a reason")
	}
	r.State, r.LastError = StateRejected, reason
	return j.put(r, "confirm_rejected: "+reason, true)
}

func (j *Journal) put(r *Record, note string, definiteReject bool) error {
	if !trading.ValidIntentID(r.Intent.ID) || r.Intent.Digest() != r.Digest {
		return errors.New("invalid or modified intent digest")
	}
	tx, err := j.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prev, err := load(tx, j.binding.Profile, r.Intent.ID)
	if err != nil {
		return err
	}
	if prev.Digest != r.Digest {
		return ErrConflict
	}
	if prev.Nonce > 0 && (r.Nonce != prev.Nonce || r.State == StatePrepared || r.State == StateCreated || r.State == StateCreating || r.State == StateAbandoned) {
		return errors.New("signed or possibly sent action cannot be reset or abandoned")
	}
	if r.State == StateAbandoned && (prev.State == StateSubmitting || prev.State == StateUnknown || prev.State == StateSubmitted) {
		return errors.New("possibly sent action cannot be abandoned")
	}
	if definiteReject && (prev.State != StateSubmitting || r.State != StateRejected) {
		return errors.New("definite rejection applies only to a submitting record")
	}
	if prev.State == StateSubmitting && r.State == StateRejected && r.ServerState == "" && !definiteReject {
		return errors.New("rejection requires authoritative server observation")
	}
	ts := j.now().UTC().Format(time.RFC3339Nano)
	res, err := tx.Exec(`UPDATE records SET state=?,action_id=?,action_json=?,cloid=?,nonce=?,server_state=?,order_id=?,last_error=?,updated_at=? WHERE profile=? AND intent_id=? AND updated_at=?`, r.State, r.ActionID, r.ActionJSON, r.Cloid, r.Nonce, r.ServerState, r.OrderID, r.LastError, ts, j.binding.Profile, r.Intent.ID, prev.UpdatedAt)
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
	history := prev.History
	if len(history) == 0 || history[len(history)-1].State != r.State || note != "" {
		t := Transition{At: ts, State: r.State, Note: note}
		history = append(history, t)
		_, err = tx.Exec(`INSERT INTO transitions(profile,intent_id,sequence,at,state,note) VALUES(?,?,?,?,?,?)`, j.binding.Profile, r.Intent.ID, len(history), ts, r.State, note)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.UpdatedAt = ts
	r.History = history
	return nil
}
func (j *Journal) List() ([]*Record, error) {
	rows, err := j.db.Query(`SELECT intent_id FROM records WHERE profile=? ORDER BY created_at,intent_id`, j.binding.Profile)
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
	out := make([]*Record, 0, len(ids))
	for _, id := range ids {
		r, e := j.Get(id)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
// Unresolved reports whether a record still needs reconciliation.
func (r *Record) Unresolved() bool { return unresolved(r) }

func unresolved(r *Record) bool {
	switch r.State {
	case StateCreating, StateSigning, StateSubmitting, StateUnknown:
		return true
	case StateSubmitted:
		return !r.Terminal()
	}
	return false
}
func (j *Journal) Unresolved() ([]*Record, error) {
	all, err := j.List()
	if err != nil {
		return nil, err
	}
	out := []*Record{}
	for _, r := range all {
		if unresolved(r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// Blocking finds possible exposure for the same account across every profile and origin.
func (j *Journal) Blocking(network, account string) ([]*Record, error) {
	rows, err := j.db.Query(`SELECT profile,intent_id FROM records WHERE network=? AND account=? AND state IN ('signing','submitting','unknown')`, network, strings.ToLower(account))
	if err != nil {
		return nil, err
	}
	type key struct{ profile, id string }
	var keys []key
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.profile, &k.id); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []*Record{}
	for _, k := range keys {
		r, e := load(j.db, k.profile, k.id)
		if e != nil {
			return nil, e
		}
		if r.State == StateSigning || r.State == StateSubmitting || r.State == StateUnknown {
			out = append(out, r)
		}
	}
	return out, nil
}
func (j *Journal) NextNonce(signer string) (int64, error) {
	if signer == "" {
		return 0, errors.New("signer required")
	}
	tx, err := j.begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := j.nextNonce(tx, signer)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
func (j *Journal) nextNonce(tx *writeTx, signer string) (int64, error) {
	signer = strings.ToLower(signer)
	var last int64
	err := tx.QueryRow(`SELECT last_nonce FROM nonces WHERE signer=?`, signer).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var global int64
	if e := tx.QueryRow(`SELECT last_nonce FROM nonces WHERE signer='*'`).Scan(&global); e != nil && !errors.Is(e, sql.ErrNoRows) {
		return 0, e
	}
	if global > last {
		last = global
	}
	if last < 0 || last >= math.MaxInt64-1 {
		return 0, errors.New("nonce exhausted or corrupt")
	}
	n := j.now().UnixMilli()
	if n <= last {
		n = last + 1
	}
	if n <= 0 {
		return 0, errors.New("invalid nonce clock")
	}
	_, err = tx.Exec(`INSERT INTO nonces(signer,last_nonce) VALUES(?,?) ON CONFLICT(signer) DO UPDATE SET last_nonce=excluded.last_nonce`, signer, n)
	return n, err
}

// BeginSubmission atomically commits signer-global nonce and submitting state before signing/sending.
// The caller must sign only after this returns successfully, and must never retry the action.
func (j *Journal) BeginSubmission(r *Record, signer string) (int64, error) {
	if signer == "" {
		return 0, errors.New("signer required")
	}
	tx, err := j.begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	prev, err := load(tx, j.binding.Profile, r.Intent.ID)
	if err != nil {
		return 0, err
	}
	if prev.Digest != r.Digest || prev.State != StateCreated || prev.Nonce != 0 || r.ActionID != prev.ActionID || r.ActionJSON != prev.ActionJSON {
		return 0, ErrConflict
	}
	// Cancelling an existing order cannot add exposure, so a cancel whose
	// stored action validates as exactly that cancel (one target, the
	// intent's asset and oid, nothing else) is exempt from the unresolved
	// gate. Every other kind is gated. Nonce allocation and the submitting
	// transition stay in this one write transaction either way.
	riskReducing := false
	if prev.Intent.Kind == trading.KindCancel {
		if _, verr := trading.ValidateAction(&prev.Intent, prev.ActionJSON); verr != nil {
			return 0, ErrConflict
		}
		riskReducing = true
	}
	// Gate the account in the same write transaction as nonce allocation.
	rows, err := tx.Query(`SELECT profile,intent_id FROM records WHERE network=? AND account=? AND NOT(profile=? AND intent_id=?) AND state IN ('signing','submitting','unknown')`, r.Intent.Network, strings.ToLower(r.Intent.Account), j.binding.Profile, r.Intent.ID)
	if err != nil {
		return 0, err
	}
	type key struct{ profile, id string }
	var keys []key
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.profile, &k.id); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, k := range keys {
		other, e := load(tx, k.profile, k.id)
		if e != nil {
			return 0, e
		}
		if (other.State == StateSigning || other.State == StateSubmitting || other.State == StateUnknown) && !riskReducing {
			return 0, ErrUnresolved
		}
	}
	n, err := j.nextNonce(tx, signer)
	if err != nil {
		return 0, err
	}
	ts := j.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.Exec(`UPDATE records SET state=?,nonce=?,signer=?,updated_at=? WHERE profile=? AND intent_id=?`, StateSigning, n, strings.ToLower(signer), ts, j.binding.Profile, r.Intent.ID)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(`INSERT INTO transitions(profile,intent_id,sequence,at,state,note) VALUES(?,?,?,?,?,?)`, j.binding.Profile, r.Intent.ID, len(prev.History)+1, ts, StateSigning, "")
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	r.State = StateSigning
	r.Nonce = n
	r.UpdatedAt = ts
	r.History = append(prev.History, Transition{At: ts, State: StateSigning})
	return n, nil
}

// FinishSigning atomically advances a verified signature to submitting, or
// returns an unsent action to created. Nonces remain burned in the nonce table.
func (j *Journal) FinishSigning(r *Record, signed bool) error {
	tx, err := j.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prev, err := load(tx, j.binding.Profile, r.Intent.ID)
	if err != nil {
		return err
	}
	if prev.State != StateSigning || prev.Nonce != r.Nonce || prev.Digest != r.Digest {
		return ErrConflict
	}
	state, nonce := StateCreated, int64(0)
	if signed {
		state, nonce = StateSubmitting, prev.Nonce
	}
	ts := j.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec(`UPDATE records SET state=?,nonce=?,updated_at=? WHERE profile=? AND intent_id=? AND state='signing'`, state, nonce, ts, j.binding.Profile, r.Intent.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO transitions(profile,intent_id,sequence,at,state,note) VALUES(?,?,?,?,?,?)`, j.binding.Profile, r.Intent.ID, len(prev.History)+1, ts, state, "signing finished"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.State, r.Nonce, r.UpdatedAt = state, nonce, ts
	r.History = append(prev.History, Transition{At: ts, State: state, Note: "signing finished"})
	return nil
}

var _ Store = (*Journal)(nil)
