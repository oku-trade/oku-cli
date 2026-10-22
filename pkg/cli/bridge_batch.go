package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	v1 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func bridgeStatusFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "intent", Usage: "bridge intent ID"}, &urfave.BoolFlag{Name: "all", Usage: "list unsettled bridge intents"}, &urfave.BoolFlag{Name: "include-expired", Usage: "with --all, also list prepared bridges that were never submitted and whose quote expired (labeled expired)"}}
}
func bridgeClaimFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "intent", Usage: "bridge intent ID"}, &urfave.BoolFlag{Name: "ready", Usage: "claim all ready bridge intents"}}
}

type bridgePending struct {
	IntentID         string          `json:"intent_id"`
	SourceChain      uint64          `json:"source_chain"`
	DestinationChain uint64          `json:"destination_chain"`
	Route            string          `json:"route"`
	Amount           string          `json:"amount"`
	State            string          `json:"state"`
	DestinationFill  destinationFill `json:"destination_fill"`
	ClaimRequired    bool            `json:"claim_required"`
	ClaimIntentID    string          `json:"claim_intent_id,omitempty"`
	Age              string          `json:"age"`
	// Expired marks a prepared intent that was never submitted and whose
	// quote has expired; it moved no funds and is shown only on request.
	Expired bool `json:"expired,omitempty"`
}

// neverSubmittedExpired reports a bridge intent that was only prepared (no
// transaction or relay was ever attempted) and whose quote has expired.
func neverSubmittedExpired(r *journal.OnchainRecord, now time.Time) bool {
	if r.State != "prepared" {
		return false
	}
	expires, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	return err != nil || !now.Before(expires)
}

// One bounded history request covers all bridge orders, avoiding unbounded
// per-intent network calls. Missing orders remain visible as unknown.
func ageOf(a *App, createdAt string) string {
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return "unknown"
	}
	return a.Now().Sub(created).Round(time.Second).String()
}

func (a *App) pendingBridges(e *env, includeExpired bool) ([]bridgePending, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	records, err := j.ListOnchainBridges()
	if err != nil {
		return nil, err
	}
	var history v1.OrderHistoryResponse
	if len(records) > 0 {
		c := e.client(w.profile)
		s, unlockErr := e.unlock(w.profile, "")
		if unlockErr != nil {
			return nil, unlockErr
		}
		s.apply(c)
		ctx, cancel := e.requestCtx()
		defer cancel()
		if err = c.History(ctx, &v1.OrderHistoryRequest{WalletAddr: w.agent, Types: []string{"bridge"}, MaxEntries: 1000}, &history); err != nil {
			return nil, apiError(err, "History")
		}
	}
	out := make([]bridgePending, 0, len(records))
	for _, r := range records {
		if !strings.EqualFold(r.Sender, w.agent) {
			continue
		}
		if r.State == "prepared" {
			// Never submitted: no source transaction and nothing to claim.
			if neverSubmittedExpired(r, a.Now()) {
				if includeExpired {
					out = append(out, bridgePending{IntentID: r.ID, SourceChain: r.ChainID, DestinationChain: r.OutChain, Route: r.Route, Amount: r.Amount, State: r.State, DestinationFill: destinationFill{Status: "expired"}, Age: ageOf(a, r.CreatedAt), Expired: true})
				}
				continue
			}
			out = append(out, bridgePending{IntentID: r.ID, SourceChain: r.ChainID, DestinationChain: r.OutChain, Route: r.Route, Amount: r.Amount, State: r.State, DestinationFill: destinationFill{Status: "not_submitted"}, Age: ageOf(a, r.CreatedAt)})
			continue
		}
		fill := destinationFill{Status: "unknown"}
		claimRequired := false
		orderID := r.OrderID
		if orderID == "" {
			orderID = r.BackendOrderID
		}
		if orderID != "" {
			if trade := bridgeTrade(&history, orderID); trade != nil {
				fill = historyFill(trade)
				claimRequired = trade.Status == v1.OrderStatus_ORDER_STATUS_AWAITING_CLAIM
			}
		}
		switch fill.Status {
		case "filled", "refunded", "failed", "expired":
			continue
		}
		claimID := ""
		if claim, lookupErr := j.ClaimForBridge(r.ID); lookupErr == nil {
			claimID = claim.ID
		} else if !errors.Is(lookupErr, journal.ErrNotFound) {
			return nil, lookupErr
		}
		created, parseErr := time.Parse(time.RFC3339Nano, r.CreatedAt)
		age := "unknown"
		if parseErr == nil {
			age = a.Now().Sub(created).Round(time.Second).String()
		}
		out = append(out, bridgePending{IntentID: r.ID, SourceChain: r.ChainID, DestinationChain: r.OutChain, Route: r.Route, Amount: r.Amount, State: r.State, DestinationFill: fill, ClaimRequired: claimRequired, ClaimIntentID: claimID, Age: age})
	}
	return out, nil
}
func (a *App) cmdBridgeStatusAll(e *env) (*result, error) {
	if str(e, "intent") != "" {
		return nil, output.Errorf(output.CodeUsage, "use --all or --intent, not both")
	}
	entries, err := a.pendingBridges(e, e.cmd.Bool("include-expired"))
	if err != nil {
		return nil, err
	}
	return &result{data: entries, human: func(out io.Writer) {
		fmt.Fprintln(out, "INTENT\tSOURCE\tDESTINATION\tROUTE\tAMOUNT\tSTATE\tDESTINATION\tCLAIM\tAGE")
		for _, r := range entries {
			state := r.State
			if r.Expired {
				state = "prepared (EXPIRED, never submitted)"
			}
			fmt.Fprintf(out, "%s\t%d\t%d\t%s\t%s\t%s\t%s\t%t (%s)\t%s\n", r.IntentID, r.SourceChain, r.DestinationChain, r.Route, r.Amount, state, r.DestinationFill.Status, r.ClaimRequired, r.ClaimIntentID, r.Age)
		}
	}}, nil
}

type bridgeClaimOutcome struct {
	IntentID string `json:"intent_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// Ready claims run sequentially. Claim locks still serialize each destination
// with other processes, while a failure on one chain never stops another.
func (a *App) cmdBridgeClaimReady(e *env) (*result, error) {
	if str(e, "intent") != "" {
		return nil, output.Errorf(output.CodeUsage, "use --ready or --intent, not both")
	}
	pending, err := a.pendingBridges(e, false)
	if err != nil {
		return nil, err
	}
	outcomes := make([]bridgeClaimOutcome, 0, len(pending))
	for _, r := range pending {
		if !r.ClaimRequired || r.State == "prepared" {
			continue
		}
		outcome := bridgeClaimOutcome{IntentID: r.IntentID}
		result, claimErr := a.claimBridgeIntent(e, r.IntentID)
		if claimErr != nil {
			outcome.Status = "error"
			outcome.Error = claimErr.Error()
		} else if data, ok := result.data.(map[string]any); ok {
			outcome.Status = fmt.Sprint(data["status"])
			if r.ClaimIntentID != "" && outcome.Status == "claim confirmed" {
				outcome.Status = "already claimed"
			}
		} else {
			outcome.Status = "claimed"
		}
		outcomes = append(outcomes, outcome)
	}
	return &result{data: outcomes, human: func(out io.Writer) {
		for _, r := range outcomes {
			fmt.Fprintf(out, "%s: %s", r.IntentID, r.Status)
			if r.Error != "" {
				fmt.Fprintf(out, " (%s)", r.Error)
			}
			fmt.Fprintln(out)
		}
	}}, nil
}
