// Package journal persists order intents and their execution state so that
// a crash or timeout never leads to a blind resend. It stores no secrets and
// no signatures.
package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/trading"
)

// States.
const (
	StatePrepared   = "prepared"   // local only, nothing sent
	StateCreating   = "creating"   // CreateAction sent, response not yet recorded
	StateCreated    = "created"    // server pending action exists, not signed
	StateSubmitting = "submitting" // ConfirmAction sent, response not yet recorded
	StateSubmitted  = "submitted"  // server accepted; see ServerState
	StateUnknown    = "unknown"    // outcome ambiguous; reconcile required
	StateRejected   = "rejected"   // definitively not executed
	StateAbandoned  = "abandoned"  // pending server action cancelled before signing
)

// Record is one intent and its lifecycle.
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

// Transition is an audit entry.
type Transition struct {
	At    string `json:"at"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

// Terminal reports whether no further action on the record is possible.
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

// Journal is a per-profile directory of records guarded by an flock.
type Journal struct {
	dir string
	now func() time.Time
}

// Open opens the journal for a profile.
func Open(profile string) (*Journal, error) {
	base, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "journal", profile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Journal{dir: dir, now: time.Now}, nil
}

// ErrNotFound means no record with that id.
var ErrNotFound = errors.New("intent not found")

// ErrConflict means the id exists with a different payload.
var ErrConflict = errors.New("intent id already used with a different payload")

// Lock takes an exclusive process lock on the journal; callers must hold it
// across read-modify-write sequences, including the network send.
func (j *Journal) Lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(j.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func (j *Journal) path(id string) string { return filepath.Join(j.dir, id+".json") }

// Get loads a record.
func (j *Journal) Get(id string) (*Record, error) {
	if !trading.ValidIntentID(id) {
		return nil, fmt.Errorf("invalid intent id %q", id)
	}
	raw, err := os.ReadFile(j.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("corrupt journal record %s: %w", id, err)
	}
	if r.Intent.Digest() != r.Digest {
		return nil, fmt.Errorf("journal record %s digest mismatch (tampered?)", id)
	}
	return &r, nil
}

// Put writes a record, appending a transition when state changed.
func (j *Journal) Put(r *Record, note string) error {
	ts := j.now().UTC().Format(time.RFC3339Nano)
	if n := len(r.History); n == 0 || r.History[n-1].State != r.State || note != "" {
		r.History = append(r.History, Transition{At: ts, State: r.State, Note: note})
	}
	r.UpdatedAt = ts
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(j.path(r.Intent.ID), raw, 0o600)
}

// Create stores a new prepared intent. Re-preparing the identical intent
// returns the existing record; a different payload under the same id fails.
func (j *Journal) Create(in trading.Intent) (*Record, bool, error) {
	existing, err := j.Get(in.ID)
	switch {
	case err == nil:
		if existing.Digest != in.Digest() {
			return existing, false, ErrConflict
		}
		return existing, false, nil
	case !errors.Is(err, ErrNotFound):
		return nil, false, err
	}
	r := &Record{Intent: in, Digest: in.Digest(), State: StatePrepared}
	return r, true, j.Put(r, "")
}

// List returns all records sorted by creation time.
func (j *Journal) List() ([]*Record, error) {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return nil, err
	}
	out := []*Record{}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		r, err := j.Get(name[:len(name)-5])
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Intent.CreatedAt < out[b].Intent.CreatedAt })
	return out, nil
}

// Unresolved returns records whose outcome is not final and that may have
// reached the server.
func (j *Journal) Unresolved() ([]*Record, error) {
	all, err := j.List()
	if err != nil {
		return nil, err
	}
	out := []*Record{}
	for _, r := range all {
		switch r.State {
		case StateCreating, StateSubmitting, StateUnknown:
			out = append(out, r)
		case StateSubmitted:
			if !r.Terminal() {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// NextNonce returns a strictly increasing millisecond nonce for an agent,
// persisted so restarts and concurrent processes (under Lock) never reuse
// one, matching getMonotonicNonce in the trade frontend.
func (j *Journal) NextNonce(agent string) (int64, error) {
	p := filepath.Join(j.dir, ".nonce-"+agent)
	var last int64
	raw, err := os.ReadFile(p)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &last); err != nil || last <= 0 {
			return 0, fmt.Errorf("nonce state %s is corrupt; refusing to sign (inspect and remove it only if no signed action could reuse it)", p)
		}
	case !errors.Is(err, os.ErrNotExist):
		return 0, err
	}
	if last >= math.MaxInt64-1 {
		return 0, errors.New("nonce state exhausted")
	}
	n := j.now().UnixMilli()
	if n <= last {
		n = last + 1
	}
	out, _ := json.Marshal(n)
	if err := config.WriteFileAtomic(p, out, 0o600); err != nil {
		return 0, err
	}
	return n, nil
}
