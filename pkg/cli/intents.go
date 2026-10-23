package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func ordersIntentsFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.BoolFlag{Name: "unresolved", Usage: "only intents that still need reconciliation (what doctor counts)"},
		&urfave.StringFlag{Name: "state", Usage: "only intents in this local state (prepared, created, submitting, unknown, submitted, rejected, abandoned)"},
		&urfave.IntFlag{Name: "limit", Usage: "show at most N, newest first (0 = all)"},
	}
}

// cmdOrdersIntents lists Hyperliquid intents from the local journal. It reads
// only local state and never contacts Oku or Hyperliquid; run
// 'orders reconcile' to refresh server state.
func (a *App) cmdOrdersIntents(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	if e.cmd.Int("limit") < 0 {
		return nil, output.Errorf(output.CodeInvalidArgument, "--limit must be 0 or more")
	}
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	all, err := j.List()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	onlyUnresolved, state := e.cmd.Bool("unresolved"), strings.ToLower(e.cmd.String("state"))
	type view struct {
		IntentID    string `json:"intent_id"`
		Kind        string `json:"kind"`
		Market      string `json:"market,omitempty"`
		Side        string `json:"side,omitempty"`
		Size        string `json:"size,omitempty"`
		OrderType   string `json:"order_type,omitempty"`
		Price       string `json:"price,omitempty"`
		State       string `json:"state"`
		ServerState string `json:"server_state,omitempty"`
		OrderID     int64  `json:"order_id,omitempty"`
		Unresolved  bool   `json:"unresolved"`
		APIURL      string `json:"api_url"`
		CurrentAPI  bool   `json:"current_api"`
		CreatedAt   string `json:"created_at"`
		UpdatedAt   string `json:"updated_at"`
		LastError   string `json:"last_error,omitempty"`
	}
	out := []view{}
	for i := len(all) - 1; i >= 0; i-- {
		r := all[i]
		un := r.Unresolved()
		if (onlyUnresolved && !un) || (state != "" && r.State != state) {
			continue
		}
		in := r.Intent
		out = append(out, view{IntentID: in.ID, Kind: in.Kind, Market: in.Market, Side: in.Side, Size: in.Size, OrderType: in.OrderType, Price: in.Price, State: r.State, ServerState: r.ServerState, OrderID: r.OrderID, Unresolved: un, APIURL: in.APIURL, CurrentAPI: in.APIURL == p.APIURL, CreatedAt: in.CreatedAt, UpdatedAt: r.UpdatedAt, LastError: r.LastError})
		if n := e.cmd.Int("limit"); n > 0 && len(out) >= n {
			break
		}
	}
	other := 0
	for _, v := range out {
		if v.Unresolved && !v.CurrentAPI {
			other++
		}
	}
	data := map[string]any{"profile": p.Name, "api_url": p.APIURL, "intents": out}
	return &result{data: data, human: func(w io.Writer) {
		if len(out) == 0 {
			fmt.Fprintln(w, "no intents")
			return
		}
		tw := newTable(w)
		fmt.Fprintln(tw, "INTENT\tKIND\tMARKET\tORDER\tSTATE\tSERVER\tOID\tUNRESOLVED\tAPI\tCREATED")
		for _, v := range out {
			order := strings.TrimSpace(strings.Join([]string{v.Side, v.Size, v.OrderType, v.Price}, " "))
			oid := "-"
			if v.OrderID != 0 {
				oid = fmt.Sprint(v.OrderID)
			}
			un := ""
			if v.Unresolved {
				un = "yes"
			}
			api := strings.TrimPrefix(v.APIURL, "https://")
			if !v.CurrentAPI {
				api += " (other)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.IntentID, v.Kind, orDash(v.Market), orDash(order), v.State, orDash(v.ServerState), oid, un, api, v.CreatedAt)
		}
		tw.Flush()
		if other > 0 {
			fmt.Fprintf(w, "\n%d unresolved intent(s) were sent through a different API than this profile's (%s); 'orders reconcile' cannot query them from here.\n", other, p.APIURL)
		}
	}}, nil
}
