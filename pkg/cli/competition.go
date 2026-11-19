package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/mcp"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The trading competition: Voltrade scores it, the account CompetitionService
// serves it (every RPC public).

const (
	competitionPageSize = 100   // ListLeaderboardRequest.page_size limit
	competitionMaxPages = 10000 // ListLeaderboardRequest.page limit
)

func (a *App) competitionCommands() map[string]*handler {
	return map[string]*handler{
		"competition info":            {access: mcp.AccessRead, summary: "Trading competition: phase, schedule, prize pool, rules, eligible stocks and raffle days (read-only)", run: a.cmdCompetitionInfo},
		"competition leaderboard":     {access: mcp.AccessRead, summary: "Trading competition leaderboard: one page, or every row with --all; --search filters by name or wallet (read-only)", flags: competitionLeaderboardFlags, run: a.cmdCompetitionLeaderboard},
		"competition status":          {access: mcp.AccessRead, summary: "A wallet's competition registration, rank, volume, today's cap and raffle, and rewards (default: the agent wallet; read-only)", flags: competitionWalletFlags, run: a.cmdCompetitionStatus},
		"competition register":        {access: mcp.AccessMutation, summary: "Register the agent wallet for the trading competition: preview the consent message and digest, then sign only with --accept and the matching --digest", flags: competitionRegisterFlags, run: a.cmdCompetitionRegister},
		"competition claim prepare":   {access: mcp.AccessRead, summary: "If Voltrade reports a claimable award, preview claiming all the agent's unclaimed Merkl USDC on Base across all campaigns alongside the reported award (never signs)", run: a.cmdCompetitionClaimPrepare},
		"competition claim submit":    {access: mcp.AccessMutation, summary: "Submit an approved claim for all the agent's unclaimed Merkl USDC on Base", flags: func() []urfave.Flag { return submitFlagsFor("competition claim") }, run: a.cmdCompetitionClaimSubmit, required: map[string]bool{"intent": true, "digest": true}},
		"competition claim status":    {access: mcp.AccessRead, summary: "Check claim status and show the Merkl balance and Voltrade award recorded at prepare", flags: statusFlags, run: a.cmdCompetitionClaimStatus, required: map[string]bool{"intent": true}},
		"competition claim reconcile": {access: mcp.AccessMutation, summary: "Verify and attach a recovered Merkl claim transaction hash", flags: reconcileFlags, run: a.cmdCompetitionClaimReconcile, required: map[string]bool{"intent": true, "tx-hash": true}},
	}
}

func competitionWalletFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "wallet", Usage: "EVM wallet to read (default: the enrolled agent wallet)"}}
}

func competitionLeaderboardFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.IntFlag{Name: "page", Value: 1, Usage: "1-based page"},
		&urfave.IntFlag{Name: "page-size", Value: 25, Usage: "rows per page (max 100)"},
		&urfave.BoolFlag{Name: "all", Usage: "read every page"},
		&urfave.StringFlag{Name: "search", Usage: "only rows whose display name or short wallet contains this text (reads every page)"},
	}
}

func (e *env) competitionCall(method string, req, resp any) error {
	p, err := e.profile()
	if err != nil {
		return err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	if err := e.client(p).Competition(ctx, method, req, resp, false); err != nil {
		return apiError(err, method)
	}
	return nil
}

// Views. Money stays a decimal USD string as the API returns it.

type competitionRules struct {
	PotMinimumUSD       string `json:"potMinimumUsd"`
	DailyVolumeCapUSD   string `json:"dailyVolumeCapUsd"`
	RaffleMinimumUSD    string `json:"raffleMinimumUsd"`
	RafflePrizeUSD      string `json:"rafflePrizeUsd"`
	RaffleWinnersPerDay int32  `json:"raffleWinnersPerDay"`
	RaffleDayCount      int32  `json:"raffleDayCount"`
	VolumePotUSD        string `json:"volumePotUsd"`
}

type competitionStock struct {
	Address string `json:"address"`
	Symbol  string `json:"symbol"`
	Name    string `json:"name"`
}

type competitionWinner struct {
	Name        string  `json:"name"`
	ShortWallet string  `json:"shortWallet"`
	PrizeUSD    *string `json:"prizeUsd,omitempty"`
}

type competitionRaffleDay struct {
	Day     int32               `json:"day"`
	DrawAt  *time.Time          `json:"drawAt,omitempty"`
	Status  string              `json:"status"`
	Winners []competitionWinner `json:"winners"`
}

type competitionView struct {
	Phase           string                 `json:"phase"`
	StartAt         *time.Time             `json:"startAt,omitempty"`
	EndAt           *time.Time             `json:"endAt,omitempty"`
	NextResetAt     *time.Time             `json:"nextResetAt,omitempty"`
	PrizePoolUSD    string                 `json:"prizePoolUsd"`
	UnlockedPoolUSD *string                `json:"unlockedPoolUsd,omitempty"`
	Participants    int64                  `json:"participants"`
	TotalVolumeUSD  string                 `json:"totalVolumeUsd"`
	DataAsOf        *time.Time             `json:"dataAsOf,omitempty"`
	Rules           competitionRules       `json:"rules"`
	ConsentText     string                 `json:"consentText"`
	EligibleStocks  []competitionStock     `json:"eligibleStocks"`
	RaffleDays      []competitionRaffleDay `json:"raffleDays"`
}

func tsPtr(t *timestamppb.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	v := t.AsTime().UTC()
	return &v
}

func enumName(s, prefix string) string {
	name := strings.ToLower(strings.TrimPrefix(s, prefix))
	if name == "" || name == "unspecified" {
		return "unknown"
	}
	return name
}

func toCompetitionView(r *v2.GetCompetitionResponse) competitionView {
	v := competitionView{
		Phase:           enumName(r.Phase.String(), "COMPETITION_PHASE_"),
		StartAt:         tsPtr(r.StartTime),
		EndAt:           tsPtr(r.EndTime),
		NextResetAt:     tsPtr(r.NextDailyResetTime),
		PrizePoolUSD:    r.PrizePoolUsd,
		UnlockedPoolUSD: r.UnlockedPoolUsd,
		Participants:    r.ParticipantCount,
		TotalVolumeUSD:  r.TotalVolumeUsd,
		DataAsOf:        tsPtr(r.DataAsOf),
		ConsentText:     r.ConsentText,
		EligibleStocks:  []competitionStock{},
		RaffleDays:      []competitionRaffleDay{},
	}
	if ru := r.Rules; ru != nil {
		v.Rules = competitionRules{ru.PotMinimumUsd, ru.DailyVolumeCapUsd, ru.RaffleMinimumUsd, ru.RafflePrizeUsd, ru.RaffleWinnersPerDay, ru.RaffleDayCount, ru.VolumePotUsd}
	}
	for _, s := range r.EligibleStocks {
		v.EligibleStocks = append(v.EligibleStocks, competitionStock{strings.ToLower(s.Address), s.Symbol, s.Name})
	}
	for _, d := range r.RaffleDays {
		day := competitionRaffleDay{Day: d.DayNumber, DrawAt: tsPtr(d.DrawTime), Status: enumName(d.Status.String(), "COMPETITION_RAFFLE_DAY_STATUS_"), Winners: []competitionWinner{}}
		for _, w := range d.Winners {
			day.Winners = append(day.Winners, competitionWinner{w.Name, w.ShortWallet, w.PrizeUsd})
		}
		v.RaffleDays = append(v.RaffleDays, day)
	}
	return v
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}

func usd(s string) string {
	if s == "" {
		return "-"
	}
	return "$" + s
}

func usdPtr(s *string) string {
	if s == nil {
		return "-"
	}
	return usd(*s)
}

func (a *App) cmdCompetitionInfo(e *env) (*result, error) {
	var resp v2.GetCompetitionResponse
	if err := e.competitionCall("GetCompetition", &v2.GetCompetitionRequest{}, &resp); err != nil {
		return nil, err
	}
	v := toCompetitionView(&resp)
	return &result{data: v, human: func(w io.Writer) {
		fmt.Fprintf(w, "Phase: %s\nStart: %s\nEnd: %s\n", v.Phase, fmtTime(v.StartAt), fmtTime(v.EndAt))
		if v.NextResetAt != nil {
			fmt.Fprintf(w, "Next daily reset: %s\n", fmtTime(v.NextResetAt))
		}
		fmt.Fprintf(w, "Prize pool: %s", usd(v.PrizePoolUSD))
		if v.UnlockedPoolUSD != nil {
			fmt.Fprintf(w, " (unlocked %s)", usd(*v.UnlockedPoolUSD))
		}
		fmt.Fprintf(w, "\nParticipants: %d\nTotal volume: %s\nData as of: %s\n", v.Participants, usd(v.TotalVolumeUSD), fmtTime(v.DataAsOf))
		r := v.Rules
		fmt.Fprintf(w, "\nRules:\n  Volume pot: %s, shared pro rata by counted volume among wallets with at least %s\n", usd(r.VolumePotUSD), usd(r.PotMinimumUSD))
		fmt.Fprintf(w, "  Daily counted volume cap: %s per wallet\n", usd(r.DailyVolumeCapUSD))
		fmt.Fprintf(w, "  Daily raffle: %d winners x %s over %d days; enter with %s volume that day\n", r.RaffleWinnersPerDay, usd(r.RafflePrizeUSD), r.RaffleDayCount, usd(r.RaffleMinimumUSD))
		if len(v.EligibleStocks) > 0 {
			fmt.Fprintln(w, "\nEligible stocks (Base):")
			tw := newTable(w)
			fmt.Fprintln(tw, "SYMBOL\tNAME\tADDRESS")
			for _, s := range v.EligibleStocks {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Symbol, s.Name, s.Address)
			}
			tw.Flush()
		}
		if len(v.RaffleDays) > 0 {
			fmt.Fprintln(w, "\nRaffle days:")
			tw := newTable(w)
			fmt.Fprintln(tw, "DAY\tDRAW\tSTATUS\tWINNERS")
			for _, d := range v.RaffleDays {
				names := make([]string, 0, len(d.Winners))
				for _, x := range d.Winners {
					names = append(names, x.Name)
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", d.Day, fmtTime(d.DrawAt), d.Status, orDash(strings.Join(names, ", ")))
			}
			tw.Flush()
		}
		if v.ConsentText != "" {
			fmt.Fprintf(w, "\nConsent: %s\n", v.ConsentText)
		}
	}}, nil
}

type competitionRow struct {
	Rank               int32   `json:"rank"`
	DisplayName        string  `json:"displayName"`
	ShortWallet        string  `json:"shortWallet"`
	CountedVolumeUSD   string  `json:"countedVolumeUsd"`
	ProjectedRewardUSD *string `json:"projectedRewardUsd,omitempty"`
	RaffleWinningsUSD  string  `json:"raffleWinningsUsd"`
}

func (a *App) cmdCompetitionLeaderboard(e *env) (*result, error) {
	page, size := e.cmd.Int("page"), e.cmd.Int("page-size")
	search := strings.ToLower(strings.TrimSpace(e.cmd.String("search")))
	all := e.cmd.Bool("all") || search != ""
	if (all || e.cmd.IsSet("search")) && (e.cmd.IsSet("page") || e.cmd.IsSet("page-size")) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--all and --search cannot be combined with --page or --page-size")
	}
	if page < 1 || page > competitionMaxPages {
		return nil, output.Errorf(output.CodeInvalidArgument, "--page must be 1..%d", competitionMaxPages)
	}
	if size < 1 || size > competitionPageSize {
		return nil, output.Errorf(output.CodeInvalidArgument, "--page-size must be 1..%d", competitionPageSize)
	}
	if all {
		page, size = 1, competitionPageSize
	}
	read := func(p int) (*v2.ListLeaderboardResponse, error) {
		var resp v2.ListLeaderboardResponse
		err := e.competitionCall("ListLeaderboard", &v2.ListLeaderboardRequest{Page: int32(p), PageSize: int32(size)}, &resp)
		return &resp, err
	}
	first, err := read(page)
	if err != nil {
		return nil, err
	}
	rows := first.Rows
	if all {
		for p, last := 2, first; p <= competitionMaxPages && len(last.Rows) >= size; p++ {
			next, err := read(p)
			if err != nil {
				return nil, err
			}
			rows = append(rows, next.Rows...)
			last = next
		}
	}
	out := []competitionRow{}
	type rowKey struct {
		rank   int32
		wallet string
	}
	seen := make(map[rowKey]struct{})
	for _, r := range rows {
		if search != "" && !strings.Contains(strings.ToLower(r.DisplayName), search) && !strings.Contains(strings.ToLower(r.ShortWallet), search) {
			continue
		}
		if all {
			key := rowKey{r.Rank, strings.ToLower(r.ShortWallet)}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, competitionRow{r.Rank, r.DisplayName, r.ShortWallet, r.CountedVolumeUsd, r.ProjectedRewardUsd, r.RaffleWinningsUsd})
	}
	data := map[string]any{"rows": out, "total": first.TotalCount, "dataAsOf": tsPtr(first.DataAsOf)}
	if !all {
		data["page"], data["pageSize"] = page, size
	}
	return &result{data: data, human: func(w io.Writer) {
		tw := newTable(w)
		fmt.Fprintln(tw, "RANK\tTRADER\tWALLET\tVOLUME\tPROJECTED\tRAFFLE")
		for _, r := range out {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", r.Rank, r.DisplayName, r.ShortWallet, usd(r.CountedVolumeUSD), usdPtr(r.ProjectedRewardUSD), usd(r.RaffleWinningsUSD))
		}
		tw.Flush()
		if all {
			fmt.Fprintf(w, "%d of %d ranked traders; data as of %s\n", len(out), first.TotalCount, fmtTime(tsPtr(first.DataAsOf)))
		} else {
			fmt.Fprintf(w, "page %d (%d per page) of %d ranked traders; data as of %s\n", page, size, first.TotalCount, fmtTime(tsPtr(first.DataAsOf)))
		}
	}}, nil
}

// competitionWallet is --wallet, else the enrolled agent wallet.
func competitionWallet(e *env) (string, error) {
	if w := strings.TrimSpace(e.cmd.String("wallet")); w != "" {
		if !config.ValidAddress(w) {
			return "", output.Errorf(output.CodeInvalidArgument, "--wallet must be a 0x EVM address")
		}
		return strings.ToLower(w), nil
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return "", err
	}
	return strings.ToLower(w.agent), nil
}

type competitionToday struct {
	CountedVolumeUSD   string     `json:"countedVolumeUsd"`
	DailyCapUSD        string     `json:"dailyCapUsd"`
	RemainingVolumeUSD string     `json:"remainingVolumeUsd"`
	RaffleEligible     bool       `json:"raffleEligible"`
	RaffleMinimumUSD   string     `json:"raffleMinimumUsd"`
	ResetsAt           *time.Time `json:"resetsAt,omitempty"`
}

type competitionReward struct {
	State   string  `json:"state"`
	Amount  string  `json:"amount"`
	Token   string  `json:"token,omitempty"`
	Chain   string  `json:"chain,omitempty"`
	Symbol  *string `json:"symbol,omitempty"`
	Decimal int32   `json:"decimals,omitempty"`
	TxHash  *string `json:"txHash,omitempty"`
}

type competitionClaim struct {
	Chain       string `json:"chain"`
	Distributor string `json:"distributor"`
}

type competitionStatus struct {
	Wallet             string              `json:"wallet"`
	Registered         bool                `json:"registered"`
	Rank               *int32              `json:"rank,omitempty"`
	CountedVolumeUSD   string              `json:"countedVolumeUsd"`
	PotMinimumReached  bool                `json:"potMinimumReached"`
	ProjectedRewardUSD *string             `json:"projectedRewardUsd,omitempty"`
	Provisional        bool                `json:"provisional"`
	RafflesWon         int32               `json:"rafflesWon"`
	RaffleWinningsUSD  string              `json:"raffleWinningsUsd"`
	Today              *competitionToday   `json:"today,omitempty"`
	Rewards            []competitionReward `json:"rewards"`
	Claim              *competitionClaim   `json:"claim,omitempty"`
}

func toCompetitionStatus(wallet string, r *v2.GetParticipantResponse) competitionStatus {
	s := competitionStatus{Wallet: wallet, Registered: r.Registered, Rank: r.Rank, CountedVolumeUSD: r.CountedVolumeUsd, PotMinimumReached: r.PotMinimumReached, ProjectedRewardUSD: r.ProjectedRewardUsd, Provisional: r.Provisional, RafflesWon: r.RafflesWon, RaffleWinningsUSD: r.RaffleWinningsUsd, Rewards: []competitionReward{}}
	if s.Rank != nil && *s.Rank == 0 {
		s.Rank = nil
	}
	if t := r.Today; t != nil {
		s.Today = &competitionToday{t.CountedVolumeUsd, t.DailyCapUsd, t.RemainingVolumeUsd, t.RaffleEligible, t.RaffleMinimumUsd, tsPtr(t.ResetsAt)}
	}
	for _, rw := range r.Rewards {
		out := competitionReward{State: enumName(rw.State.String(), "COMPETITION_REWARD_STATE_"), Amount: rw.Amount, TxHash: rw.TxHash}
		if tk := rw.Token; tk != nil {
			out.Token, out.Chain, out.Symbol, out.Decimal = strings.ToLower(tk.Address), tk.Chain, tk.Symbol, tk.Decimals
		}
		s.Rewards = append(s.Rewards, out)
	}
	if c := r.Claim; c != nil && c.Distributor != "" {
		s.Claim = &competitionClaim{c.Chain, c.Distributor}
	}
	return s
}

func (a *App) cmdCompetitionStatus(e *env) (*result, error) {
	wallet, err := competitionWallet(e)
	if err != nil {
		return nil, err
	}
	var resp v2.GetParticipantResponse
	if err := e.competitionCall("GetParticipant", &v2.GetParticipantRequest{Wallet: wallet}, &resp); err != nil {
		return nil, err
	}
	s := toCompetitionStatus(wallet, &resp)
	return &result{data: s, human: func(w io.Writer) {
		fmt.Fprintf(w, "Wallet: %s\nRegistered: %t\n", s.Wallet, s.Registered)
		if !s.Registered {
			fmt.Fprintln(w, "Not registered: run 'oku competition register' to join.")
		}
		rank := "-"
		if s.Rank != nil {
			rank = fmt.Sprint(*s.Rank)
		}
		fmt.Fprintf(w, "Rank: %s\nCounted volume: %s (pot minimum reached: %t)\n", rank, usd(s.CountedVolumeUSD), s.PotMinimumReached)
		fmt.Fprintf(w, "Projected reward: %s", usdPtr(s.ProjectedRewardUSD))
		if s.Provisional {
			fmt.Fprint(w, " (provisional)")
		}
		fmt.Fprintf(w, "\nRaffles won: %d (%s)\n", s.RafflesWon, usd(s.RaffleWinningsUSD))
		if t := s.Today; t != nil {
			fmt.Fprintf(w, "\nToday: %s counted of %s cap, %s remaining\n", usd(t.CountedVolumeUSD), usd(t.DailyCapUSD), usd(t.RemainingVolumeUSD))
			fmt.Fprintf(w, "Today's raffle: eligible %t (minimum %s); resets %s\n", t.RaffleEligible, usd(t.RaffleMinimumUSD), fmtTime(t.ResetsAt))
		}
		if len(s.Rewards) > 0 {
			fmt.Fprintln(w, "\nRewards:")
			tw := newTable(w)
			fmt.Fprintln(tw, "STATE\tAMOUNT\tTOKEN\tCHAIN\tTX")
			for _, r := range s.Rewards {
				tok := r.Token
				if r.Symbol != nil {
					tok = *r.Symbol
				}
				tx := "-"
				if r.TxHash != nil {
					tx = *r.TxHash
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.State, r.Amount, orDash(tok), orDash(r.Chain), tx)
			}
			tw.Flush()
		}
		if s.Claim != nil {
			fmt.Fprintf(w, "Claim through Merkl distributor %s on %s\n", s.Claim.Distributor, s.Claim.Chain)
		}
	}}, nil
}
