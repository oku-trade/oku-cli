package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

const (
	defaultSwapOrderInterval = 15 * time.Second
	minSwapOrderInterval     = time.Second
	// maxSwapOrderAttempts bounds re-triggering after a sell could not be
	// prepared (no route, balance) or was prepared but never broadcast.
	// Nothing is signed then. High-impact refusals and transient prepare
	// errors (upstream, rate limit, timeout) are not counted: the order
	// stays armed and retries every check.
	maxSwapOrderAttempts = 3
	// tokenPriceBatch is the most tokens one GetTokenPrice request carries.
	tokenPriceBatch = 25
)

func swapOrdersCreateFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "chain", Usage: "EVM chain ID of the position, e.g. 42161 (required)"},
		&urfave.StringFlag{Name: "token", Usage: "ERC-20 address of the token to sell for native (required)"},
		&urfave.StringFlag{Name: "amount", Usage: "token amount to sell when triggered (or use --all)"},
		&urfave.BoolFlag{Name: "all", Usage: "sell the full token balance when triggered"},
		&urfave.StringFlag{Name: "take-profit-pct", Usage: "sell when value rises this many percent above the cost basis"},
		&urfave.StringFlag{Name: "stop-loss-pct", Usage: "sell when value falls this many percent below the cost basis"},
		&urfave.StringFlag{Name: "trailing-stop-pct", Usage: "sell when value falls this many percent below the highest value seen since creation"},
		&urfave.IntFlag{Name: "slippage-bps", Value: 50, Usage: "maximum slippage of the sell in basis points, 0-300"},
		&urfave.StringFlag{Name: "basis", Usage: "native cost basis of the amount (overrides the journal; required for take-profit/stop-loss without a confirmed journal buy)"},
		&urfave.StringFlag{Name: "id", Usage: "optional unique swap order ID"},
	}
}

func swapOrdersListFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "state", Usage: "only orders in this state (" + strings.Join(journal.SwapOrderStates, ", ") + ")"}}
}

func swapOrdersCancelFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "order", Usage: "swap order ID (required)"}}
}

func swapOrdersWatchFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "interval", Value: defaultSwapOrderInterval.String(), Usage: "time between price checks, e.g. 15s or 1m (min 1s)"},
		&urfave.BoolFlag{Name: "once", Usage: "run one check and exit"},
		&urfave.BoolFlag{Name: "dry-run", Usage: "report what would trigger; never prepares or sells and leaves the journal unchanged"},
	}
}

// percentFlag parses an optional positive percentage. below100 rejects 100 or
// more (a stop at -100% can never fire).
func percentFlag(e *env, name string, below100 bool) (string, error) {
	raw := strings.TrimSpace(str(e, name))
	if raw == "" {
		return "", nil
	}
	c, err := decimal.Canonical(raw)
	if err != nil || c == "0" {
		return "", output.Errorf(output.CodeInvalidArgument, "--%s must be a positive decimal percentage", name)
	}
	if r, _ := decimal.Rat(c); below100 && r.Cmp(big.NewRat(100, 1)) >= 0 {
		return "", output.Errorf(output.CodeInvalidArgument, "--%s must be below 100", name)
	}
	return c, nil
}

func ratOf(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil
	}
	return r
}

// ratString renders a non-negative rational with up to 18 decimals.
func ratString(r *big.Rat) string {
	s := r.FloatString(18)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func pctFactor(pct string, sign int64) *big.Rat {
	p := ratOf(pct)
	f := new(big.Rat).Quo(p, big.NewRat(100, 1))
	if sign < 0 {
		return f.Sub(big.NewRat(1, 1), f)
	}
	return f.Add(big.NewRat(1, 1), f)
}

// swapOrderTrigger reports which exit fires at value, given the peak value
// already including value. Stop-loss wins over trailing over take-profit.
func swapOrderTrigger(o *journal.SwapOrder, value, peak *big.Rat) string {
	basis := ratOf(o.Basis)
	if o.StopLossPct != "" && basis != nil && value.Cmp(new(big.Rat).Mul(basis, pctFactor(o.StopLossPct, -1))) <= 0 {
		return "stop_loss"
	}
	if o.TrailingStopPct != "" && peak != nil && value.Cmp(new(big.Rat).Mul(peak, pctFactor(o.TrailingStopPct, -1))) <= 0 {
		return "trailing_stop"
	}
	if o.TakeProfitPct != "" && basis != nil && value.Cmp(new(big.Rat).Mul(basis, pctFactor(o.TakeProfitPct, 1))) >= 0 {
		return "take_profit"
	}
	return ""
}

// journalBasis returns the native cost of size raw token units at the
// average cost of the agent's journaled swaps (swapBook), or nil when the
// journal tracks none of token. Swaps without a recorded fill use their
// minimum output, so that part of the basis can overstate the entry price.
func journalBasis(j *journal.Journal, agent string, chain uint64, token string, size *big.Int) (*big.Rat, error) {
	book, _, err := swapBook(j, agent, chain)
	if err != nil {
		return nil, err
	}
	p := book.Position(token)
	if p == nil {
		return nil, nil
	}
	wei := p.BasisOf(size)
	if wei == nil {
		return nil, nil
	}
	return wei.Quo(wei, weiPerNative()), nil
}

// tokenPrices reads oracle prices in native per token for tokens on chain
// through TokenService/GetTokenPrice, which creates no backend state.
func (a *App) tokenPrices(e *env, chain uint64, tokens []string) (map[string]*big.Rat, error) {
	_, out, err := a.oraclePrices(e, chain, tokens)
	return out, err
}

// oraclePrices returns the native USD price and each token's price in native
// per token. Tokens without a price are absent from the map.
func (a *App) oraclePrices(e *env, chain uint64, tokens []string) (*big.Rat, map[string]*big.Rat, error) {
	p, err := e.profile()
	if err != nil {
		return nil, nil, err
	}
	c := e.client(p)
	usd := map[string]float64{}
	nativeKey := "native"
	queue := append([]string{nativeKey}, tokens...)
	for len(queue) > 0 {
		n := len(queue)
		if n > tokenPriceBatch {
			n = tokenPriceBatch
		}
		batch := queue[:n]
		queue = queue[n:]
		req := &v2.GetTokenPriceRequest{}
		for _, t := range batch {
			addr := t
			if t == nativeKey {
				addr = eip7528Address
			}
			req.Tokens = append(req.Tokens, &v2.ChainAddress{Chain: strconv.FormatUint(chain, 10), Address: addr})
		}
		var res v2.GetTokenPriceResponse
		ctx, cancel := e.requestCtx()
		err := c.TokenRPC(ctx, "GetTokenPrice", req, &res, false)
		cancel()
		if err != nil {
			return nil, nil, apiError(err, "GetTokenPrice")
		}
		for _, r := range res.Results {
			if r == nil || r.Error != "" || r.PriceUsd <= 0 {
				continue
			}
			key := strings.ToLower(r.Address)
			if r.Address == "" || isNativeToken(r.Address) {
				key = nativeKey
			}
			usd[key] = r.PriceUsd
		}
	}
	native, ok := usd[nativeKey]
	nativeRat := new(big.Rat).SetFloat64(native)
	if !ok || nativeRat == nil {
		return nil, nil, output.Errorf(output.CodeUpstream, "no native price on chain %d", chain).WithRetry()
	}
	out := map[string]*big.Rat{}
	for _, t := range tokens {
		if v, ok := usd[strings.ToLower(t)]; ok {
			if r := new(big.Rat).SetFloat64(v); r != nil {
				out[strings.ToLower(t)] = r.Quo(r, nativeRat)
			}
		}
	}
	return nativeRat, out, nil
}

func (a *App) cmdSwapOrdersCreate(e *env) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	chain, err := strconv.ParseUint(str(e, "chain"), 10, 64)
	if err != nil || !onchain.Allowed(chain) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--chain must be an EVM chain ID")
	}
	token := strings.TrimSpace(str(e, "token"))
	if isNativeToken(token) || !onchain.ValidAddress(token) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--token must be an ERC-20 address (exits sell token for native)")
	}
	amount, all := strings.TrimSpace(str(e, "amount")), e.cmd.Bool("all")
	if (amount == "") == !all || isMaxAmount(amount) {
		return nil, output.Errorf(output.CodeUsage, "use exactly one of --amount X or --all")
	}
	tp, err := percentFlag(e, "take-profit-pct", false)
	if err != nil {
		return nil, err
	}
	sl, err := percentFlag(e, "stop-loss-pct", true)
	if err != nil {
		return nil, err
	}
	trail, err := percentFlag(e, "trailing-stop-pct", true)
	if err != nil {
		return nil, err
	}
	if tp == "" && sl == "" && trail == "" {
		return nil, output.Errorf(output.CodeUsage, "set at least one of --take-profit-pct, --stop-loss-pct or --trailing-stop-pct")
	}
	bps := e.cmd.Int("slippage-bps")
	if bps < 0 || bps > 300 {
		return nil, output.Errorf(output.CodeInvalidArgument, "slippage must be 0-300 bps")
	}
	id := strings.TrimSpace(str(e, "id"))
	if id == "" {
		id = "so-" + strings.TrimPrefix(randomID(), "oku-")
	}
	if !validOrderID(id) {
		return nil, output.Errorf(output.CodeInvalidArgument, "--id must be 3-48 letters, digits, _ or -")
	}
	if _, rerr := onchain.RPCURL(a.Getenv, chain); rerr != nil {
		return nil, output.Wrap(output.CodeConfigError, rerr, "chain RPC")
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	rpc := onchain.HTTPRPC{Getenv: a.Getenv}
	dec, err := rpc.Call(ctx, chain, token, "0x313ce567")
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "token decimals")
	}
	if !dec.IsInt64() || dec.Int64() < 0 || dec.Int64() > 36 {
		return nil, output.Errorf(output.CodeInvalidArgument, "unsupported token decimals")
	}
	decimals := int(dec.Int64())
	balance, err := rpc.Call(ctx, chain, token, "0x70a08231"+strings.Repeat("0", 24)+strings.ToLower(w.agent[2:]))
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "reading token balance")
	}
	size := balance
	if all {
		if size.Sign() <= 0 {
			return nil, output.Errorf(output.CodeInsufficientBalance, "agent holds none of %s on chain %d", token, chain)
		}
		amount = "all"
	} else if size, err = parseUnits(amount, decimals); err != nil {
		return nil, output.Wrap(output.CodeInvalidDecimal, err, "amount")
	} else if size.Cmp(balance) > 0 {
		return nil, output.Errorf(output.CodeInsufficientBalance, "--amount %s exceeds the agent balance of %s on chain %d", amount, formatUnits(balance, decimals), chain)
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	o := journal.SwapOrder{ID: id, Agent: w.agent, ChainID: chain, Token: token, Decimals: decimals, Amount: amount, Size: formatUnits(size, decimals), TakeProfitPct: tp, StopLossPct: sl, TrailingStopPct: trail, SlippageBps: bps, BasisSource: "none"}
	if raw := strings.TrimSpace(str(e, "basis")); raw != "" {
		c, cerr := decimal.Canonical(raw)
		if cerr != nil || c == "0" {
			return nil, output.Errorf(output.CodeInvalidDecimal, "--basis must be a positive native amount")
		}
		o.Basis, o.BasisSource = c, "override"
	} else {
		basis, berr := journalBasis(j, w.agent, chain, token, size)
		if berr != nil {
			return nil, output.Wrap(output.CodeInternal, berr, "journal")
		}
		if basis != nil {
			o.Basis, o.BasisSource = ratString(basis), "journal"
		}
	}
	if o.Basis == "" && (tp != "" || sl != "") {
		return nil, output.Errorf(output.CodeUsage, "no confirmed native buy of %s on chain %d in the journal; pass --basis NATIVE (what the amount cost in native)", token, chain)
	}
	if prices, perr := a.tokenPrices(e, chain, []string{token}); perr != nil {
		e.diag("warning: could not read the current price (%s); watch will retry", output.As(perr).Message)
	} else if price := prices[strings.ToLower(token)]; price != nil {
		value := new(big.Rat).Mul(price, ratOf(o.Size))
		o.LastPrice, o.LastValue, o.PeakValue, o.LastQuoteAt = ratString(price), ratString(value), ratString(value), a.Now().UTC().Format(time.RFC3339Nano)
	} else {
		e.diag("warning: Oku has no price for %s on chain %d; watch cannot evaluate this order until it does", token, chain)
	}
	created, err := j.CreateSwapOrder(o)
	if errors.Is(err, journal.ErrConflict) {
		return nil, output.Errorf(output.CodeIntentConflict, "swap order %s already exists", id)
	}
	if errors.Is(err, journal.ErrSwapOrderAllConflict) {
		return nil, output.Errorf(output.CodeIntentConflict, "an open swap order already sells all of %s on chain %d (%s); cancel it or combine the exits into one order", token, chain, strings.TrimPrefix(err.Error(), journal.ErrSwapOrderAllConflict.Error()+": "))
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "creating swap order")
	}
	view := swapOrderOf(created)
	return &result{data: view, human: func(out io.Writer) {
		fmt.Fprintf(out, "swap order %s: sell %s of %s on chain %d for native\n", view.ID, view.Amount, view.Token, view.ChainID)
		writeSwapOrderTriggers(out, view)
		fmt.Fprintln(out, "Exits are client-side: they only fire while 'oku swap orders watch' runs.")
	}}, nil
}

func validOrderID(id string) bool {
	if len(id) < 3 || len(id) > 48 {
		return false
	}
	for i, ch := range id {
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || i > 0 && (ch == '_' || ch == '-')
		if !ok {
			return false
		}
	}
	return true
}

// swapOrderView adds the native trigger levels to a stored order.
type swapOrderView struct {
	*journal.SwapOrder
	TakeProfitValue   string `json:"take_profit_value,omitempty"`
	StopLossValue     string `json:"stop_loss_value,omitempty"`
	TrailingStopValue string `json:"trailing_stop_value,omitempty"`
}

func swapOrderOf(o *journal.SwapOrder) swapOrderView {
	v := swapOrderView{SwapOrder: o}
	if basis := ratOf(o.Basis); basis != nil {
		if o.TakeProfitPct != "" {
			v.TakeProfitValue = ratString(new(big.Rat).Mul(basis, pctFactor(o.TakeProfitPct, 1)))
		}
		if o.StopLossPct != "" {
			v.StopLossValue = ratString(new(big.Rat).Mul(basis, pctFactor(o.StopLossPct, -1)))
		}
	}
	if peak := ratOf(o.PeakValue); peak != nil && o.TrailingStopPct != "" {
		v.TrailingStopValue = ratString(new(big.Rat).Mul(peak, pctFactor(o.TrailingStopPct, -1)))
	}
	return v
}

func writeSwapOrderTriggers(w io.Writer, v swapOrderView) {
	if v.Basis != "" {
		fmt.Fprintf(w, "Basis: %s native (%s)\n", v.Basis, v.BasisSource)
	}
	if v.LastValue != "" {
		fmt.Fprintf(w, "Value: %s native (peak %s)\n", v.LastValue, orDash(v.PeakValue))
	}
	if v.TakeProfitPct != "" {
		fmt.Fprintf(w, "Take profit: +%s%% (value >= %s)\n", v.TakeProfitPct, orDash(v.TakeProfitValue))
	}
	if v.StopLossPct != "" {
		fmt.Fprintf(w, "Stop loss: -%s%% (value <= %s)\n", v.StopLossPct, orDash(v.StopLossValue))
	}
	if v.TrailingStopPct != "" {
		fmt.Fprintf(w, "Trailing stop: -%s%% from peak (value <= %s)\n", v.TrailingStopPct, orDash(v.TrailingStopValue))
	}
}

func (a *App) cmdSwapOrdersList(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	var states []string
	if s := str(e, "state"); s != "" {
		states = []string{s}
	}
	orders, err := j.ListSwapOrders(states...)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	views := make([]swapOrderView, 0, len(orders))
	for _, o := range orders {
		views = append(views, swapOrderOf(o))
	}
	return &result{data: map[string]any{"profile": p.Name, "orders": views}, human: func(w io.Writer) {
		if len(views) == 0 {
			fmt.Fprintln(w, "no swap orders")
			return
		}
		tw := newTable(w)
		fmt.Fprintln(tw, "ORDER\tCHAIN\tTOKEN\tAMOUNT\tTP\tSL\tTRAIL\tBASIS\tVALUE\tPEAK\tSTATE\tSELL INTENT")
		for _, v := range views {
			pct := func(s string) string {
				if s == "" {
					return "-"
				}
				return s + "%"
			}
			state := v.State
			if v.NeedsReconcile {
				state += " (reconcile)"
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.ID, v.ChainID, v.Token, v.Amount, pct(v.TakeProfitPct), pct(v.StopLossPct), pct(v.TrailingStopPct), orDash(v.Basis), orDash(v.LastValue), orDash(v.PeakValue), state, orDash(v.SellIntentID))
		}
		tw.Flush()
	}}, nil
}

func (a *App) cmdSwapOrdersCancel(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	j, err := openJournal(p)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	id := str(e, "order")
	if id == "" {
		return nil, output.Errorf(output.CodeUsage, "--order required")
	}
	o, err := j.GetSwapOrder(id)
	if errors.Is(err, journal.ErrNotFound) {
		return nil, output.Errorf(output.CodeNotFound, "swap order %q not found", id)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	if o.State != journal.SwapOrderActive {
		return nil, output.Errorf(output.CodeIntentState, "swap order %s is %s; only an active order can be cancelled", id, o.State)
	}
	if err = j.TransitionSwapOrder(id, journal.SwapOrderActive, journal.SwapOrderCancelled, journal.SwapOrderPatch{}); err != nil {
		if errors.Is(err, journal.ErrConflict) {
			current, _ := j.GetSwapOrder(id)
			state := "changed"
			if current != nil {
				state = current.State
			}
			return nil, output.Errorf(output.CodeIntentState, "swap order %s is %s; only an active order can be cancelled", id, state)
		}
		return nil, output.Wrap(output.CodeInternal, err, "cancelling swap order")
	}
	o, err = j.GetSwapOrder(id)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "journal")
	}
	return &result{data: swapOrderOf(o), human: func(w io.Writer) { fmt.Fprintf(w, "swap order %s cancelled\n", id) }}, nil
}

// swapOrderExec is what a watch tick needs from the outside world. Tests
// substitute it; the default reads oracle prices and sells through the same
// prepare and submit handlers as 'swap prepare' and 'swap submit'.
type swapOrderExec struct {
	prices func(chain uint64, tokens []string) (map[string]*big.Rat, error)
	// prepare journals a sell intent for o and returns its ID and digest.
	prepare func(o *journal.SwapOrder) (string, string, error)
	// submit signs and broadcasts a prepared sell. ctx is detached from the
	// watch context, so Ctrl-C never abandons a sell mid-broadcast.
	submit func(ctx context.Context, intent, digest string) error
	// intentState returns the journal state of a sell intent.
	intentState func(intent string) (string, error)
}

func (a *App) defaultSwapOrderExec(e *env, j *journal.Journal) swapOrderExec {
	return swapOrderExec{
		prices: func(chain uint64, tokens []string) (map[string]*big.Rat, error) {
			return a.tokenPrices(e, chain, tokens)
		},
		prepare: func(o *journal.SwapOrder) (string, string, error) {
			amount := o.Amount
			given := map[string]any{"chain": strconv.FormatUint(o.ChainID, 10), "from": o.Token, "to": "native", "amount": amount, "slippage-bps": int64(o.SlippageBps), "no-intent": true, "intent-id": fmt.Sprintf("%s-sell-%d", o.ID, o.Attempts+1)}
			sub, err := subEnv(e, routePrepareFlags(false), given)
			if err != nil {
				return "", "", err
			}
			res, err := a.cmdRoutePrepare(sub, "swap")
			if err != nil {
				return "", "", err
			}
			prepared, ok := res.data.(preparedRoute)
			if !ok || prepared.OnchainRecord == nil {
				return "", "", output.Errorf(output.CodeInternal, "unexpected prepare result")
			}
			return prepared.ID, prepared.Digest, nil
		},
		submit: func(ctx context.Context, intent, digest string) error {
			detached := *e
			detached.ctx = ctx
			sub, err := subEnv(&detached, submitFlagsFor("swap"), map[string]any{"intent": intent, "digest": digest})
			if err != nil {
				return err
			}
			_, err = a.cmdRouteSubmit(sub, "swap")
			return err
		},
		intentState: func(intent string) (string, error) {
			r, err := j.GetOnchain(intent)
			if err != nil {
				return "", err
			}
			return r.State, nil
		},
	}
}

// subEnv runs another handler in-process with typed flags, like Service.
func subEnv(e *env, flags []urfave.Flag, given map[string]any) (*env, error) {
	src, err := newMapFlags(&handler{flags: func() []urfave.Flag { return flags }}, given)
	if err != nil {
		return nil, err
	}
	return &env{app: e.app, g: e.g, cmd: newCheckedFlags(src, flags), ctx: e.ctx}, nil
}

type swapOrderTickEntry struct {
	OrderID      string `json:"order_id"`
	State        string `json:"state"`
	Price        string `json:"price,omitempty"`
	Value        string `json:"value,omitempty"`
	Peak         string `json:"peak_value,omitempty"`
	Trigger      string `json:"trigger,omitempty"`
	WouldTrigger bool   `json:"would_trigger,omitempty"`
	SellIntentID string `json:"sell_intent_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

type swapOrderTick struct {
	At     string               `json:"at"`
	DryRun bool                 `json:"dry_run"`
	Orders []swapOrderTickEntry `json:"orders"`
	Active int                  `json:"active"`
}

// swapOrdersTick evaluates every open order of agent once and sells at most
// one triggered order at a time. In dry-run mode it reads prices only.
func (a *App) swapOrdersTick(ctx context.Context, j *journal.Journal, x swapOrderExec, agent string, dryRun bool) (*swapOrderTick, error) {
	tick := &swapOrderTick{At: a.Now().UTC().Format(time.RFC3339Nano), DryRun: dryRun, Orders: []swapOrderTickEntry{}}
	listMine := func() ([]*journal.SwapOrder, error) {
		open, err := j.ListSwapOrders(journal.SwapOrderActive, journal.SwapOrderTriggered, journal.SwapOrderExecuting)
		if err != nil {
			return nil, output.Wrap(output.CodeInternal, err, "journal")
		}
		mine := []*journal.SwapOrder{}
		for _, o := range open {
			if strings.EqualFold(o.Agent, agent) {
				mine = append(mine, o)
			}
		}
		return mine, nil
	}
	mine, err := listMine()
	if err != nil {
		return nil, err
	}
	// Orders a previous watcher left mid-flight are resolved from the journal
	// first, never resubmitted. Orders resumed to active are then evaluated
	// from a fresh read.
	if !dryRun {
		resumed := map[string]bool{}
		for _, o := range mine {
			if o.State != journal.SwapOrderActive {
				entry := a.resumeSwapOrder(j, x, o)
				if entry.State == journal.SwapOrderActive {
					resumed[o.ID] = true
				} else {
					tick.Orders = append(tick.Orders, entry)
				}
			}
		}
		if len(resumed) > 0 {
			if mine, err = listMine(); err != nil {
				return nil, err
			}
		}
	}
	byChain := map[uint64][]*journal.SwapOrder{}
	for _, o := range mine {
		if o.State == journal.SwapOrderActive {
			byChain[o.ChainID] = append(byChain[o.ChainID], o)
		}
	}
	chains := make([]uint64, 0, len(byChain))
	for c := range byChain {
		chains = append(chains, c)
	}
	sort.Slice(chains, func(i, k int) bool { return chains[i] < chains[k] })
	var firstErr error
	sold := false
	for _, chain := range chains {
		orders := byChain[chain]
		seen := map[string]bool{}
		tokens := []string{}
		for _, o := range orders {
			if t := strings.ToLower(o.Token); !seen[t] {
				seen[t] = true
				tokens = append(tokens, o.Token)
			}
		}
		prices, err := x.prices(chain, tokens)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			for _, o := range orders {
				tick.Orders = append(tick.Orders, swapOrderTickEntry{OrderID: o.ID, State: o.State, Error: output.As(err).Message})
				tick.Active++
			}
			continue
		}
		for _, o := range orders {
			entry := a.evaluateSwapOrder(ctx, j, x, o, prices[strings.ToLower(o.Token)], dryRun, sold)
			if entry.SellIntentID != "" {
				sold = true
			}
			if entry.State == journal.SwapOrderActive {
				tick.Active++
			}
			tick.Orders = append(tick.Orders, entry)
		}
	}
	return tick, firstErr
}

// evaluateSwapOrder records the price of o and sells it when an exit fires.
// Once a sell ran this tick (deferSell), further triggers wait for the next
// tick, so one transaction is in flight at a time and an order sharing the
// token sees the post-sell balance.
func (a *App) evaluateSwapOrder(ctx context.Context, j *journal.Journal, x swapOrderExec, o *journal.SwapOrder, price *big.Rat, dryRun, deferSell bool) swapOrderTickEntry {
	entry := swapOrderTickEntry{OrderID: o.ID, State: o.State}
	if price == nil {
		entry.Error = "no price available"
		return entry
	}
	value := new(big.Rat).Mul(price, ratOf(o.Size))
	entry.Price, entry.Value = ratString(price), ratString(value)
	peak := value
	if old := ratOf(o.PeakValue); old != nil && old.Cmp(value) > 0 {
		peak = old
	}
	if !dryRun {
		stored, err := j.RecordSwapOrderQuote(o.ID, entry.Price, entry.Value, a.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			entry.Error = "recording quote: " + err.Error()
			return entry
		}
		peak = ratOf(stored)
	}
	entry.Peak = ratString(peak)
	trigger := swapOrderTrigger(o, value, peak)
	if trigger == "" {
		return entry
	}
	entry.Trigger = trigger
	if dryRun {
		entry.WouldTrigger = true
		return entry
	}
	if deferSell {
		entry.Error = "deferred: another sell ran this check"
		return entry
	}
	return a.sellSwapOrder(ctx, j, x, o, trigger, entry)
}

// sellSwapOrder claims the order with a compare-and-set before anything is
// prepared, so a cancel or a second watcher can never cause a second sell.
func (a *App) sellSwapOrder(ctx context.Context, j *journal.Journal, x swapOrderExec, o *journal.SwapOrder, trigger string, entry swapOrderTickEntry) swapOrderTickEntry {
	if err := ctx.Err(); err != nil {
		entry.Error = "not sold: watcher stopping"
		return entry
	}
	if err := j.TransitionSwapOrder(o.ID, journal.SwapOrderActive, journal.SwapOrderTriggered, journal.SwapOrderPatch{Trigger: &trigger}); err != nil {
		entry.Error = "order changed before selling: " + err.Error()
		if current, _ := j.GetSwapOrder(o.ID); current != nil {
			entry.State = current.State
		}
		return entry
	}
	entry.State = journal.SwapOrderTriggered
	intent, digest, err := x.prepare(o)
	if err != nil {
		oe := output.As(err)
		msg := "prepare: " + oe.Message
		entry.Error = msg
		to, count := journal.SwapOrderActive, countsAsSwapOrderAttempt(err)
		if count && o.Attempts+1 >= maxSwapOrderAttempts {
			to = journal.SwapOrderFailed
			msg = fmt.Sprintf("%s (gave up after %d attempts; nothing was signed)", msg, maxSwapOrderAttempts)
		}
		if terr := j.TransitionSwapOrder(o.ID, journal.SwapOrderTriggered, to, journal.SwapOrderPatch{LastError: &msg, CountAttempt: count}); terr == nil {
			entry.State = to
		}
		return entry
	}
	if err = j.TransitionSwapOrder(o.ID, journal.SwapOrderTriggered, journal.SwapOrderExecuting, journal.SwapOrderPatch{SellIntentID: &intent}); err != nil {
		entry.Error = "binding sell intent: " + err.Error()
		return entry
	}
	entry.State, entry.SellIntentID = journal.SwapOrderExecuting, intent
	sellCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), onchainExecBudget)
	submitErr := x.submit(sellCtx, intent, digest)
	cancel()
	return a.settleSwapOrder(j, x, o.ID, o.Attempts, intent, submitErr, entry)
}

// countsAsSwapOrderAttempt reports whether a prepare error is a permanent
// refusal (no route, insufficient balance) that should use up an attempt.
func countsAsSwapOrderAttempt(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	oe := output.As(err)
	if oe.Retryable || oe.Details["reason"] == highImpactReason {
		return false
	}
	switch oe.Code {
	case output.CodeUpstream, output.CodeUpstreamTimeout, output.CodeRateLimited:
		return false
	}
	return true
}

// settleSwapOrder moves an executing order to filled or failed from the
// sell intent's journal state. A sell that is still prepared (never signed)
// re-arms the order, up to maxSwapOrderAttempts. Anything that may have
// broadcast without a confirmed receipt is failed with needs_reconcile and
// never retried.
func (a *App) settleSwapOrder(j *journal.Journal, x swapOrderExec, id string, attempts int, intent string, submitErr error, entry swapOrderTickEntry) swapOrderTickEntry {
	state, err := x.intentState(intent)
	if err != nil {
		state = "unknown"
	}
	unknown := submitErr != nil && output.As(submitErr).Code == output.CodeOutcomeUnknown
	if state == "prepared" && !unknown && attempts+1 < maxSwapOrderAttempts {
		msg := "sell was not broadcast"
		if submitErr != nil {
			msg += ": " + output.As(submitErr).Message
		}
		if err := j.TransitionSwapOrder(id, journal.SwapOrderExecuting, journal.SwapOrderActive, journal.SwapOrderPatch{LastError: &msg, CountAttempt: true}); err == nil {
			entry.State, entry.Error = journal.SwapOrderActive, msg
			return entry
		}
		state = "unknown"
	}
	to, reconcile, msg := journal.SwapOrderFailed, false, ""
	switch {
	case state == "confirmed" && !unknown:
		to = journal.SwapOrderFilled
	case state == "prepared" && !unknown:
		msg = fmt.Sprintf("sell was not broadcast (gave up after %d attempts; nothing was signed)", maxSwapOrderAttempts)
	case state == "failed" && !unknown:
		msg = "sell failed"
	default:
		reconcile = true
		msg = fmt.Sprintf("sell outcome unknown (intent %s); never retried: run 'oku swap status --intent %s'", state, intent)
	}
	if submitErr != nil {
		msg = strings.TrimPrefix(msg+": "+output.As(submitErr).Message, ": ")
	}
	if err := j.TransitionSwapOrder(id, journal.SwapOrderExecuting, to, journal.SwapOrderPatch{LastError: &msg, NeedsReconcile: &reconcile, CountAttempt: state == "prepared" && !unknown}); err != nil {
		entry.Error = "settling: " + err.Error()
		return entry
	}
	entry.State = to
	if msg != "" {
		entry.Error = msg
	}
	return entry
}

func (a *App) resumeSwapOrder(j *journal.Journal, x swapOrderExec, o *journal.SwapOrder) swapOrderTickEntry {
	entry := swapOrderTickEntry{OrderID: o.ID, State: o.State, Trigger: o.Trigger, SellIntentID: o.SellIntentID}
	if o.State == journal.SwapOrderTriggered {
		// The previous watcher stopped before binding a sell intent, so
		// nothing was submitted for this order. Counting the attempt gives
		// the next sell a fresh intent ID.
		msg := "watcher stopped before the sell was submitted"
		if err := j.TransitionSwapOrder(o.ID, journal.SwapOrderTriggered, journal.SwapOrderActive, journal.SwapOrderPatch{LastError: &msg, CountAttempt: true}); err == nil {
			entry.State = journal.SwapOrderActive
		}
		return entry
	}
	return a.settleSwapOrder(j, x, o.ID, o.Attempts, o.SellIntentID, nil, entry)
}

func (a *App) cmdSwapOrdersWatch(e *env) (*result, error) {
	interval := defaultSwapOrderInterval
	if raw := strings.TrimSpace(str(e, "interval")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, output.Errorf(output.CodeUsage, "--interval must be a duration like 15s or 1m")
		}
		interval = d
	}
	if interval < minSwapOrderInterval {
		return nil, output.Errorf(output.CodeInvalidArgument, "--interval must be at least %s", minSwapOrderInterval)
	}
	once, dryRun := e.cmd.Bool("once"), e.cmd.Bool("dry-run")
	if e.g.inProcess && !once {
		return nil, output.Errorf(output.CodeUsage, "in-process (MCP) watch requires once=true; run 'oku swap orders watch' in a terminal to keep exits armed")
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
	if !dryRun {
		unlock, lerr := j.LockSwapOrderWatch()
		if errors.Is(lerr, config.ErrLocked) {
			return nil, output.Errorf(output.CodeIntentState, "another 'swap orders watch' is running for profile %s", w.profile.Name)
		}
		if lerr != nil {
			return nil, output.Wrap(output.CodeInternal, lerr, "watch lock")
		}
		defer unlock()
	}
	ctx, stop := signal.NotifyContext(e.ctx, os.Interrupt)
	defer stop()
	loop := *e
	loop.ctx = ctx
	x := a.defaultSwapOrderExec(&loop, j)
	ticks, last, stopped, err := a.watchSwapOrders(ctx, j, x, w.agent, dryRun, once, interval, e.diag)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"ticks": ticks, "stopped": stopped, "dry_run": dryRun, "last": last}
	return &result{data: data, human: func(out io.Writer) {
		if last == nil {
			fmt.Fprintln(out, "no swap orders checked")
			return
		}
		if len(last.Orders) == 0 {
			fmt.Fprintln(out, "no open swap orders")
			return
		}
		tw := newTable(out)
		fmt.Fprintln(tw, "ORDER\tSTATE\tPRICE\tVALUE\tPEAK\tTRIGGER\tSELL INTENT\tNOTE")
		for _, o := range last.Orders {
			trigger := o.Trigger
			if o.WouldTrigger {
				trigger += " (dry run)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", o.OrderID, o.State, orDash(o.Price), orDash(o.Value), orDash(o.Peak), orDash(trigger), orDash(o.SellIntentID), orDash(o.Error))
		}
		tw.Flush()
		if stopped != "once" {
			fmt.Fprintf(out, "stopped after %d checks: %s\n", ticks, strings.ReplaceAll(stopped, "_", " "))
		}
	}}, nil
}

// watchSwapOrders runs ticks until once, no active orders remain, or ctx or
// Sleep stops it. It returns the tick count, last tick and stop reason.
func (a *App) watchSwapOrders(ctx context.Context, j *journal.Journal, x swapOrderExec, agent string, dryRun, once bool, interval time.Duration, diag func(string, ...any)) (int, *swapOrderTick, string, error) {
	ticks := 0
	var last *swapOrderTick
	for {
		tick, terr := a.swapOrdersTick(ctx, j, x, agent, dryRun)
		ticks++
		if tick != nil {
			last = tick
			if !once {
				for _, o := range tick.Orders {
					line := fmt.Sprintf("%s %s %s value=%s peak=%s", tick.At, o.OrderID, o.State, orDash(o.Value), orDash(o.Peak))
					if o.Trigger != "" {
						line += " trigger=" + o.Trigger
					}
					if o.Error != "" {
						line += " error=" + o.Error
					}
					diag("%s", line)
				}
			}
		}
		if once {
			return ticks, last, "once", terr
		}
		if terr != nil {
			diag("%s tick error: %s", a.Now().UTC().Format(time.RFC3339), output.As(terr).Message)
		}
		if tick != nil && tick.Active == 0 {
			return ticks, last, "no_active_orders", nil
		}
		if ctx.Err() != nil {
			return ticks, last, "interrupted", nil
		}
		if err := a.Sleep(ctx, interval); err != nil {
			return ticks, last, "interrupted", nil
		}
	}
}
