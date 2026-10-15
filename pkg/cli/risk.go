package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/trading"
	urfave "github.com/urfave/cli/v3"
)

// requiredScope is the device scope a server action kind needs. Risk
// actions (leverage, isolated margin) need the separately granted "risk"
// scope; trading scope alone is not sufficient.
func requiredScope(kind string) string {
	switch kind {
	case trading.KindLeverage, trading.KindMargin:
		return "risk"
	}
	return "trade"
}

func leverageFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "market", Usage: "market, e.g. BTC (required)"},
		&urfave.IntFlag{Name: "leverage", Usage: "integer leverage, 1..market max (required)"},
		&urfave.StringFlag{Name: "mode", Usage: "margin mode: cross or isolated (required)"},
		&urfave.StringFlag{Name: "intent-id", Usage: "caller-chosen idempotency id (default: random)"},
		&urfave.DurationFlag{Name: "expires", Value: defaultIntentTTL, Usage: "intent lifetime (max 1h)"},
	}
}

func marginFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "market", Usage: "market of the isolated position (required)"},
		&urfave.StringFlag{Name: "amount", Usage: "USDC to add (positive) or remove (negative), at most 6 decimals (required)"},
		&urfave.StringFlag{Name: "intent-id", Usage: "caller-chosen idempotency id (default: random)"},
		&urfave.DurationFlag{Name: "expires", Value: defaultIntentTTL, Usage: "intent lifetime (max 1h)"},
	}
}

// cmdPositionsLeverage prepares (never submits) a leverage/mode change.
func (a *App) cmdPositionsLeverage(e *env) (*result, error) {
	coin, err := normCoin(str(e, "market"))
	if err != nil {
		return nil, err
	}
	lev := e.cmd.Int("leverage")
	var isCross bool
	switch str(e, "mode") {
	case "cross":
		isCross = true
	case "isolated":
	default:
		return nil, output.Errorf(output.CodeInvalidArgument, "--mode must be cross or isolated")
	}
	return e.prepareRisk(coin, func(in *trading.Intent, maxLev int, onlyIsolated bool) error {
		if err := trading.ValidateLeverage(lev, maxLev, isCross, onlyIsolated); err != nil {
			return output.Wrap(output.CodeInvalidArgument, err, "--leverage")
		}
		in.Kind, in.Leverage, in.IsCross = trading.KindLeverage, lev, isCross
		return nil
	}, func(in *trading.Intent) string {
		mode := "isolated"
		if in.IsCross {
			mode = "cross"
		}
		return fmt.Sprintf("set %s leverage to %dx %s", in.Market, in.Leverage, mode)
	})
}

// cmdPositionsMargin prepares (never submits) an isolated margin change.
func (a *App) cmdPositionsMargin(e *env) (*result, error) {
	coin, err := normCoin(str(e, "market"))
	if err != nil {
		return nil, err
	}
	amount := str(e, "amount")
	if amount == "" {
		return nil, output.Errorf(output.CodeInvalidArgument, "--amount is required")
	}
	if _, err := trading.MarginUnits(amount); err != nil {
		return nil, output.Wrap(output.CodeInvalidDecimal, err, "--amount")
	}
	return e.prepareRisk(coin, func(in *trading.Intent, _ int, _ bool) error {
		in.Kind, in.MarginAmount = trading.KindMargin, amount
		return nil
	}, func(in *trading.Intent) string {
		verb := "add"
		abs := in.MarginAmount
		if len(abs) > 0 && abs[0] == '-' {
			verb, abs = "remove", abs[1:]
		}
		return fmt.Sprintf("%s %s USDC isolated margin on %s", verb, abs, in.Market)
	})
}

func (e *env) prepareRisk(coin string, fill func(*trading.Intent, int, bool) error, describe func(*trading.Intent) string) (*result, error) {
	if len(e.args) != 0 {
		return nil, output.Errorf(output.CodeUsage, "unexpected arguments %v", e.args)
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	acct, err := e.tradeAccount(p)
	if err != nil {
		return nil, err
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	if !trading.ValidIntentID(id) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--intent-id must be 3-64 characters of [A-Za-z0-9_-]")
	}
	ttl := e.cmd.Duration("expires")
	if ttl <= 0 || ttl > maxIntentTTL {
		return nil, output.Errorf(output.CodeInvalidArgument, "--expires must be between 1s and 1h")
	}
	m, err := e.market(p, coin)
	if err != nil {
		return nil, err
	}
	now := e.app.Now().UTC()
	in := trading.Intent{
		Version: 1, ID: id, Network: p.Network, Account: acct, APIURL: p.APIURL,
		Market: m.Coin, AssetID: m.AssetID,
		CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(ttl).Format(time.RFC3339),
	}
	if err := fill(&in, m.MaxLeverage, m.OnlyIsolated); err != nil {
		return nil, err
	}
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	unlock, err := j.Lock()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "locking journal")
	}
	defer unlock()
	rec, _, err := j.Create(in)
	if errors.Is(err, journal.ErrConflict) {
		return nil, output.Errorf(output.CodeIntentConflict, "intent id %q already exists with a different payload", id).
			WithDetail("existing_digest", rec.Digest)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	summary := describe(&rec.Intent)
	notes := []string{
		"preparing sends nothing to the exchange",
		"this is a separate exchange action from any order and is not atomic with one",
		"submitting requires the device 'risk' scope",
	}
	next := fmt.Sprintf("oku orders submit --intent %s --digest %s", rec.Intent.ID, rec.Digest)
	return &result{data: map[string]any{
		"intent_id": rec.Intent.ID, "digest": rec.Digest, "state": rec.State, "intent": rec.Intent,
		"summary": summary, "notes": notes, "next": next,
	}, human: func(w io.Writer) {
		fmt.Fprintf(w, "prepared: %s on %s for %s (%s), expires %s\n", summary, p.Network, acct, rec.Intent.ID, rec.Intent.ExpiresAt)
		for _, n := range notes {
			fmt.Fprintf(w, "  note: %s\n", n)
		}
		fmt.Fprintf(w, "submit with: %s\n", next)
	}}, nil
}

// openJournal opens the profile journal bound to its network, account and
// API origin so records from a mismatched binding are refused, and so
// account-wide gates see every profile and origin.
func openJournal(p *resolved) (*journal.Journal, error) {
	b := journal.Binding{Profile: p.Name}
	if p.Account != "" {
		b.Network, b.Account, b.Origin = p.Network, strings.ToLower(p.Account), p.APIURL
	}
	j, err := journal.OpenBinding(b)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "opening journal")
	}
	return j, nil
}
