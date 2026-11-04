package cli

import (
	"context"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// closeLapsedBatch closes a lending batch intent stuck unknown with no hash
// as failed when it provably can no longer execute:
//   - the agent's nonce reserved for the batch is still unused, with nothing
//     pending, so no transaction took it;
//   - the intent expired and every router warrant in it has lapsed, so a
//     broadcast that is somehow still in flight reverts as a whole (the
//     batch is atomic) and could at most burn gas. An intent with no router
//     swap has no such bound and is never closed here.
//
// Anything short of that leaves the intent unknown. Only intents with a
// lending plan qualify, and only from "unknown": a "sending" record may
// belong to a live submit.
func (a *App) closeLapsedBatch(ctx context.Context, rpc onchain.HTTPRPC, j *journal.Journal, r *journal.OnchainRecord) (*journal.OnchainRecord, error) {
	if r.State != "unknown" || r.Batch == nil || len(r.Hashes) != 0 || len(r.Lending) == 0 {
		return r, nil
	}
	expires, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	if err != nil || a.Now().Before(expires) {
		return r, nil
	}
	plan, calls, err := lendingPlanOf(r)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "reading the lending plan")
	}
	warrant, err := plan.WarrantExpiry(calls)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "reading the swap warrant")
	}
	// Without a warrant nothing bounds when a late broadcast could still
	// execute, so such intents stay unknown.
	if warrant == 0 || !a.Now().After(time.Unix(int64(warrant), 0)) {
		return r, nil
	}
	latest, pending, err := rpc.Nonces(ctx, r.ChainID, r.Sender)
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "agent nonce lookup")
	}
	if latest != r.Batch.Nonce || pending != r.Batch.Nonce {
		return r, nil
	}
	if err = j.AdvanceOnchainObserved(r.ID, "unknown", "failed", "", journal.OnchainObservation{Hashes: r.Hashes, Step: r.StepIndex}); err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "closing the lapsed batch")
	}
	return j.GetOnchain(r.ID)
}
