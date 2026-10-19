package cli

import (
	"encoding/hex"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func reconcileFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "intent", Usage: "unresolved intent ID"}, &urfave.StringFlag{Name: "tx-hash", Usage: "recovered onchain transaction hash"}}
}

func (a *App) cmdFundsReconcile(e *env, kind string) (*result, error) {
	hash := str(e, "tx-hash")
	if len(hash) != 66 || !strings.HasPrefix(hash, "0x") {
		return nil, output.Errorf(output.CodeInvalidArgument, "valid --tx-hash required")
	}
	if _, err := hex.DecodeString(hash[2:]); err != nil {
		return nil, output.Errorf(output.CodeInvalidArgument, "invalid hash")
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	unlock, err := j.Lock()
	if err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	r, err := j.GetOnchain(str(e, "intent"))
	if err != nil {
		return nil, output.Wrap(output.CodeNotFound, err, "intent")
	}
	need := len(r.Templates)
	if r.Batch != nil {
		need = 1
	}
	if r.Kind != kind || !strings.EqualFold(r.Sender, w.agent) || (r.State != "unknown" && r.State != "sending") || len(r.Hashes) >= need {
		return nil, output.Errorf(output.CodeIntentState, "only unresolved unknown intents with an unrecorded transaction can attach a hash")
	}
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	ctx, cancel := e.requestCtx()
	defer cancel()
	if r.Batch != nil {
		err = rpc.VerifyBatchTransaction(ctx, r.OnchainIntent, *r.Batch, hash)
	} else {
		err = rpc.VerifyTransaction(ctx, r.ChainID, hash, w.agent, r.Templates[len(r.Hashes)])
	}
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "recovered transaction differs")
	}
	if err = j.AdvanceOnchain(r.ID, r.State, "sent", hash); err != nil {
		return nil, output.Wrap(output.CodeIntentState, err, "attach recovered hash")
	}
	unlock()
	locked = false
	if kind == "swap" {
		return a.cmdSwapStatus(e)
	}
	return a.cmdSendStatus(e)
}
func (a *App) cmdSendReconcile(e *env) (*result, error) { return a.cmdFundsReconcile(e, "send") }
func (a *App) cmdSwapReconcile(e *env) (*result, error) { return a.cmdFundsReconcile(e, "swap") }
