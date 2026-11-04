package cli

import (
	"strings"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// advanceRecordStep resolves the receipt of a sequential intent's latest
// step. A confirmed intermediate step moves the intent back to sending with
// the next cursor, so submit can resume; a confirmed last step confirms it and
// a reverted step fails it. It returns nil when there is nothing to resolve
// yet (not sequential, no new hash, or the receipt is not mined).
func (a *App) advanceRecordStep(e *env, j *journal.Journal, r *journal.OnchainRecord) (*journal.OnchainRecord, error) {
	hashesBeforeStep := r.StepIndex
	if r.ApprovalSkipped {
		hashesBeforeStep--
	}
	if r.ExecutionMode != "sequential" || (r.State != "sent" && r.State != "unknown") || len(r.Hashes) <= hashesBeforeStep {
		return nil, nil
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	status, err := onchain.HTTPRPC{Getenv: a.Getenv}.Receipt(ctx, r.ChainID, r.Hashes[len(r.Hashes)-1])
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "sequential receipt")
	}
	if status == nil {
		return nil, nil
	}
	next := "failed"
	if *status {
		next = "confirmed"
		if r.StepIndex+1 < len(r.Templates) {
			next = "sending"
		}
	}
	if err = j.AdvanceOnchain(r.ID, r.State, next, ""); err != nil {
		return nil, err
	}
	return j.GetOnchain(r.ID)
}

// advanceSequentialStep runs advanceRecordStep for --intent of kind under the
// chain lock, for status commands that otherwise read through cmdFundsStatus.
func (a *App) advanceSequentialStep(e *env, kind string) error {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return err
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return err
	}
	defer j.Close()
	r, err := j.GetOnchain(str(e, "intent"))
	if err != nil {
		return output.Wrap(output.CodeNotFound, err, "intent")
	}
	if r.Kind != kind || !strings.EqualFold(r.Sender, w.agent) {
		return output.Errorf(output.CodeNotFound, "intent not found")
	}
	unlock, err := j.LockChain(w.agent, r.ChainID)
	if err != nil {
		return err
	}
	defer unlock()
	if r, err = j.GetOnchain(r.ID); err != nil {
		return err
	}
	_, err = a.advanceRecordStep(e, j, r)
	return err
}
