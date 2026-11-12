package cli

import (
	"errors"
	"io"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
)

func chaseStatusResult(j *journal.Journal, rec *journal.Record) (*result, error) {
	c, err := j.GetChase(rec.Intent.ID)
	if errors.Is(err, journal.ErrNotFound) {
		return recordResult(rec), nil
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "loading chase state")
	}
	r := recordResult(rec)
	r.data.(map[string]any)["chase"] = c
	parentHuman := r.human
	r.human = func(w io.Writer) { parentHuman(w); chaseResult(c).human(w) }
	return r, nil
}

// Keep the owner lock until the parent is durably abandoned. A live chase must
// be reconciled or resumed for cancellation instead of revoking its policy
// while a resting order or pending replacement still exists.
func lockChaseAbandon(j *journal.Journal, id string) (func(), error) {
	unlock, err := j.LockChase(id)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "chase is owned by another process")
	}
	c, err := j.GetChase(id)
	if errors.Is(err, journal.ErrNotFound) {
		return unlock, nil
	}
	if err != nil {
		unlock()
		return nil, output.Wrap(output.CodeInternal, err, "loading chase state")
	}
	if c.State != journal.ChaseStopped {
		unlock()
		return nil, output.Errorf(output.CodeIntentState, "chase %s is %s; use orders reconcile or orders chase --resume to resolve its resting order", id, c.State)
	}
	return unlock, nil
}
