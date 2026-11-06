package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/decimal"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/uniswaplp"
	urfave "github.com/urfave/cli/v3"
)

// lpKind is the journal kind of every LP intent.
const lpKind = "lp"

func lpSlippageFlag() urfave.Flag {
	return &urfave.IntFlag{Name: "slippage-bps", Value: 50, Usage: fmt.Sprintf("maximum price slippage in basis points, 0-%d (web app default 50)", uniswaplp.MaxSlippageBps)}
}

func lpDepositFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "amount", Usage: "decimal amount of --amount-token to deposit, or max/all; the other side is derived at the current price"},
		&urfave.StringFlag{Name: "amount-token", Usage: "which pool token --amount is in: token0, token1, or that token's address/symbol/native"},
		&urfave.StringFlag{Name: "gas-reserve", Usage: "native gas reserve in native units for max/all of a native deposit"},
		lpSlippageFlag(),
		&urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"},
	}
}

func lpOpenFlags() []urfave.Flag {
	flags := append(lpPoolFlags(),
		&urfave.StringFlag{Name: "range-percent", Usage: "range of +/- this percent around the current price, e.g. 5"},
		&urfave.StringFlag{Name: "price-lower", Usage: "lower range price in token1 per token0"},
		&urfave.StringFlag{Name: "price-upper", Usage: "upper range price in token1 per token0"},
		&urfave.StringFlag{Name: "tick-lower", Usage: "lower range tick (multiple of the tick spacing)"},
		&urfave.StringFlag{Name: "tick-upper", Usage: "upper range tick (multiple of the tick spacing)"},
		&urfave.BoolFlag{Name: "full-range", Usage: "use the full price range"},
	)
	return append(flags, lpDepositFlags()...)
}

func lpIncreaseFlags() []urfave.Flag { return append(lpPositionFlags(), lpDepositFlags()...) }

func lpDecreaseFlags() []urfave.Flag {
	return append(lpPositionFlags(),
		&urfave.StringFlag{Name: "percent", Usage: "percent of the position's liquidity to remove, above 0 up to 100"},
		lpSlippageFlag(),
		&urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"})
}

func lpCloseFlags() []urfave.Flag {
	return append(lpPositionFlags(), lpSlippageFlag(), &urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"})
}

func lpCollectFlags() []urfave.Flag {
	return append(lpPositionFlags(), &urfave.StringFlag{Name: "intent-id", Usage: "optional idempotency ID"})
}

// lpRange resolves exactly one range form against the pool.
func lpRange(e *env, pool uniswaplp.Pool) (uniswaplp.Range, error) {
	forms := 0
	pct, pl, pu, tl, tu := str(e, "range-percent"), str(e, "price-lower"), str(e, "price-upper"), str(e, "tick-lower"), str(e, "tick-upper")
	full := e.cmd.Bool("full-range")
	for _, set := range []bool{pct != "", pl != "" || pu != "", tl != "" || tu != "", full} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		return uniswaplp.Range{}, output.Errorf(output.CodeInvalidArgument, "give exactly one range: --range-percent, --price-lower/--price-upper, --tick-lower/--tick-upper, or --full-range")
	}
	invalid := func(err error) error { return output.Wrap(output.CodeInvalidArgument, err, "range") }
	switch {
	case full:
		return uniswaplp.FullRange(pool.TickSpacing), nil
	case pct != "":
		r, ok := decimal.Rat(pct)
		if !ok || r.Sign() <= 0 {
			return uniswaplp.Range{}, output.Errorf(output.CodeInvalidArgument, "--range-percent must be a positive number")
		}
		bps := new(big.Rat).Mul(r, big.NewRat(100, 1))
		if !bps.IsInt() || bps.Cmp(big.NewRat(10000, 1)) >= 0 {
			return uniswaplp.Range{}, output.Errorf(output.CodeInvalidArgument, "--range-percent must be below 100 with at most 2 decimals")
		}
		rg, err := uniswaplp.PercentRange(pool.SqrtPriceX96, int(bps.Num().Int64()), pool.TickSpacing)
		if err != nil {
			return uniswaplp.Range{}, invalid(err)
		}
		return rg, nil
	case pl != "" || pu != "":
		lo, ok1 := decimal.Rat(pl)
		hi, ok2 := decimal.Rat(pu)
		if !ok1 || !ok2 || lo.Sign() <= 0 || hi.Cmp(lo) <= 0 {
			return uniswaplp.Range{}, output.Errorf(output.CodeInvalidArgument, "--price-lower and --price-upper must both be positive with lower below upper")
		}
		a, err := uniswaplp.TickForHumanPrice(lo, pool.Token0.Decimals, pool.Token1.Decimals)
		if err != nil {
			return uniswaplp.Range{}, invalid(err)
		}
		b, err := uniswaplp.TickForHumanPrice(hi, pool.Token0.Decimals, pool.Token1.Decimals)
		if err != nil {
			return uniswaplp.Range{}, invalid(err)
		}
		rg, err := uniswaplp.UsableRange(a, b, pool.TickSpacing)
		if err != nil {
			return uniswaplp.Range{}, invalid(err)
		}
		return rg, nil
	default:
		a, err1 := strconv.Atoi(tl)
		b, err2 := strconv.Atoi(tu)
		if err1 != nil || err2 != nil {
			return uniswaplp.Range{}, output.Errorf(output.CodeInvalidArgument, "--tick-lower and --tick-upper must both be integers")
		}
		rg, err := uniswaplp.NewRange(a, b, pool.TickSpacing)
		if err != nil {
			return uniswaplp.Range{}, invalid(err)
		}
		return rg, nil
	}
}

// lpDepositSide resolves --amount-token to a pool side.
func (a *App) lpDepositSide(ctx context.Context, e *env, t lpTarget, pool uniswaplp.Pool) (uniswaplp.Side, bool, error) {
	name := strings.TrimSpace(str(e, "amount-token"))
	var side uniswaplp.Side
	switch strings.ToLower(name) {
	case "token0", "token1":
		side = uniswaplp.Side0
		if strings.EqualFold(name, "token1") {
			side = uniswaplp.Side1
		}
		// The pool arguments may name native for this side: honor that
		// payment choice like naming the side's token directly.
		addr := common.HexToAddress(pool.Token0.Address)
		if side == uniswaplp.Side1 {
			addr = common.HexToAddress(pool.Token1.Address)
		}
		for _, arg := range []string{str(e, "token0"), str(e, "token1")} {
			if arg == "" {
				continue
			}
			tok, err := a.resolveLPToken(ctx, e, t, arg)
			if err != nil {
				return 0, false, err
			}
			if tok.native && tok.addr == addr {
				return side, t.d.Version == uniswaplp.V3, nil
			}
		}
		return side, false, nil
	case "":
		return 0, false, output.Errorf(output.CodeInvalidArgument, "--amount-token is required: token0, token1, or a pool token")
	}
	tok, err := a.resolveLPToken(ctx, e, t, name)
	if err != nil {
		return 0, false, err
	}
	switch tok.addr {
	case common.HexToAddress(pool.Token0.Address):
		return uniswaplp.Side0, tok.native && t.d.Version == uniswaplp.V3, nil
	case common.HexToAddress(pool.Token1.Address):
		return uniswaplp.Side1, tok.native && t.d.Version == uniswaplp.V3, nil
	}
	return 0, false, output.Errorf(output.CodeInvalidArgument, "--amount-token %s is not a token of this pool", name)
}

// lpDeposit parses --amount on the chosen side, resolving max/all through
// the shared resolveMaxAmount so native gas reserves match send and swap.
func (a *App) lpDeposit(ctx context.Context, e *env, t lpTarget, pool uniswaplp.Pool, agent string) (uniswaplp.DepositInput, string, error) {
	side, useNative, err := a.lpDepositSide(ctx, e, t, pool)
	if err != nil {
		return uniswaplp.DepositInput{}, "", err
	}
	tok := pool.Token0
	if side == uniswaplp.Side1 {
		tok = pool.Token1
	}
	text := str(e, "amount")
	source := "exact"
	if isMaxAmount(text) {
		balanceToken := tok.Address
		if useNative || tok.IsNative() {
			balanceToken = "native"
		}
		// The reserve is a route-sized gas floor: an LP action costs far more
		// than a transfer, and the managers reject plain native transfers.
		if text, _, err = a.resolveMaxAmount(e, t.chain, balanceToken, agent, tok.Decimals, true); err != nil {
			return uniswaplp.DepositInput{}, "", err
		}
		source = "max"
	}
	amount, err := parseUnits(text, tok.Decimals)
	if err != nil {
		return uniswaplp.DepositInput{}, "", output.Wrap(output.CodeInvalidDecimal, err, "amount")
	}
	return uniswaplp.DepositInput{Side: side, Amount: amount, UseNative: useNative}, source, nil
}

func (a *App) lpDeadline() int64 { return a.Now().Unix() + uniswaplp.DeadlineSeconds }

// lpOwnedPosition loads one position and requires the agent to own it.
func (a *App) lpOwnedPosition(ctx context.Context, e *env, t lpTarget, agent string) (uniswaplp.Position, error) {
	id, err := parseTokenID(str(e, "token-id"))
	if err != nil {
		return uniswaplp.Position{}, err
	}
	pos, err := lpLoadPosition(ctx, t, id)
	if err != nil {
		return uniswaplp.Position{}, err
	}
	if !strings.EqualFold(pos.Owner, agent) {
		return uniswaplp.Position{}, output.Errorf(output.CodeInvalidArgument, "position %s is not owned by the agent", pos.TokenID)
	}
	return pos, nil
}

func (a *App) cmdLPOpenPrepare(e *env) (*result, error) {
	return a.lpPrepare(e, func(ctx context.Context, t lpTarget, w *resolvedWallet) (uniswaplp.Plan, string, error) {
		pool, err := a.lpLoadPool(ctx, e, t)
		if err != nil {
			return uniswaplp.Plan{}, "", err
		}
		rg, err := lpRange(e, pool)
		if err != nil {
			return uniswaplp.Plan{}, "", err
		}
		in, source, err := a.lpDeposit(ctx, e, t, pool, w.agent)
		if err != nil {
			return uniswaplp.Plan{}, "", err
		}
		p, err := uniswaplp.PlanOpen(t.d, common.HexToAddress(w.agent), pool, rg, in, e.cmd.Int("slippage-bps"), a.lpDeadline())
		return p, source, err
	})
}

func (a *App) cmdLPIncreasePrepare(e *env) (*result, error) {
	return a.lpPrepare(e, func(ctx context.Context, t lpTarget, w *resolvedWallet) (uniswaplp.Plan, string, error) {
		pos, err := a.lpOwnedPosition(ctx, e, t, w.agent)
		if err != nil {
			return uniswaplp.Plan{}, "", err
		}
		in, source, err := a.lpDeposit(ctx, e, t, pos.Pool, w.agent)
		if err != nil {
			return uniswaplp.Plan{}, "", err
		}
		p, err := uniswaplp.PlanIncrease(t.d, common.HexToAddress(w.agent), pos, in, e.cmd.Int("slippage-bps"), a.lpDeadline())
		return p, source, err
	})
}

func (a *App) cmdLPDecreasePrepare(e *env) (*result, error) {
	return a.lpPrepare(e, func(ctx context.Context, t lpTarget, w *resolvedWallet) (uniswaplp.Plan, string, error) {
		pct, ok := decimal.Rat(str(e, "percent"))
		bps := new(big.Rat)
		if ok {
			bps.Mul(pct, big.NewRat(100, 1))
		}
		if !ok || !bps.IsInt() || bps.Sign() <= 0 || bps.Cmp(big.NewRat(10000, 1)) > 0 {
			return uniswaplp.Plan{}, "", output.Errorf(output.CodeInvalidArgument, "--percent must be above 0 and at most 100, with at most 2 decimals")
		}
		pos, err := a.lpOwnedPosition(ctx, e, t, w.agent)
		if err != nil {
			return uniswaplp.Plan{}, "", err
		}
		p, err := uniswaplp.PlanDecrease(t.d, common.HexToAddress(w.agent), pos, int(bps.Num().Int64()), false, e.cmd.Int("slippage-bps"), a.lpDeadline())
		return p, "", err
	})
}

func (a *App) cmdLPClosePrepare(e *env) (*result, error) {
	return a.lpPrepare(e, func(ctx context.Context, t lpTarget, w *resolvedWallet) (uniswaplp.Plan, string, error) {
		pos, err := a.lpOwnedPosition(ctx, e, t, w.agent)
		if err != nil {
			return uniswaplp.Plan{}, "", err
		}
		p, err := uniswaplp.PlanDecrease(t.d, common.HexToAddress(w.agent), pos, 10000, true, e.cmd.Int("slippage-bps"), a.lpDeadline())
		return p, "", err
	})
}

func (a *App) cmdLPCollectPrepare(e *env) (*result, error) {
	return a.lpPrepare(e, func(ctx context.Context, t lpTarget, w *resolvedWallet) (uniswaplp.Plan, string, error) {
		pos, err := a.lpOwnedPosition(ctx, e, t, w.agent)
		if err != nil {
			return uniswaplp.Plan{}, "", err
		}
		if pos.Fees.Amount0.Sign() == 0 && pos.Fees.Amount1.Sign() == 0 {
			return uniswaplp.Plan{}, "", output.Errorf(output.CodeInvalidArgument, "position %s has no uncollected fees", pos.TokenID)
		}
		p, err := uniswaplp.PlanCollect(t.d, common.HexToAddress(w.agent), pos, a.lpDeadline())
		return p, "", err
	})
}

// lpPrepare validates the agent and chain, builds the plan, reads the
// approvals it needs, simulates the action when no approval precedes it, and
// journals the intent with the plan and every transaction bound into the
// digest. It never signs or broadcasts.
func (a *App) lpPrepare(e *env, build func(context.Context, lpTarget, *resolvedWallet) (uniswaplp.Plan, string, error)) (*result, error) {
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	t, err := a.lpTarget(e, str(e, "version"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	plan, source, err := build(ctx, t, w)
	if err != nil {
		var oe *output.Error
		if errors.As(err, &oe) {
			return nil, err
		}
		return nil, output.Wrap(output.CodeInvalidArgument, err, "liquidity plan")
	}
	action, err := plan.Calldata()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "building calldata")
	}
	spend, err := plan.Spend()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "deposit amounts")
	}
	var legs []uniswaplp.Allowances
	for i, tok := range []uniswaplp.Token{plan.Token0, plan.Token1} {
		amount := spend.Amount0
		if i == 1 {
			amount = spend.Amount1
		}
		if amount.Sign() > 0 && !tok.IsNative() && plan.NativeSide != i {
			legs = append(legs, uniswaplp.Allowances{Token: tok.Address, Required: amount})
		}
	}
	if err = uniswaplp.ReadAllowances(ctx, t.rpc, t.d, common.HexToAddress(w.agent), legs); err != nil {
		return nil, lpReadError(err, "allowances")
	}
	if err = lpCheckFunds(ctx, t, w.agent, legs, action.Value); err != nil {
		return nil, err
	}
	approvals := uniswaplp.Approvals(t.d, a.Now().Unix(), legs)
	templates := make([]journal.OnchainTemplate, 0, len(approvals)+1)
	for _, c := range approvals {
		templates = append(templates, journal.OnchainTemplate{ChainID: t.chain, To: c.To, Data: c.Data, Value: onchain.Hex(c.Value)})
	}
	templates = append(templates, journal.OnchainTemplate{ChainID: t.chain, To: action.To, Data: action.Data, Value: onchain.Hex(action.Value)})
	// Simulate the exact action when nothing precedes it. With approvals
	// pending the action cannot be simulated yet; submit estimates every
	// step against the chain again before broadcasting.
	simulated := false
	if len(approvals) == 0 {
		if _, err := t.rpc.Estimate(ctx, t.chain, w.agent, templates); err != nil {
			return nil, output.Wrap(output.CodeRejected, err, "simulating the liquidity action; nothing was journaled")
		}
		simulated = true
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "encoding plan")
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	in := journal.OnchainIntent{
		ID: id, Kind: lpKind, ChainID: t.chain, Sender: w.agent, Recipient: w.agent, Token: plan.Token0.Address,
		Amount: string(plan.Action), Target: plan.Manager, LP: raw, Templates: templates,
		ExpiresAt: time.Unix(plan.Deadline, 0).UTC().Format(time.RFC3339Nano), CreatedAt: a.Now().UTC().Format(time.RFC3339Nano),
	}
	if source == "max" {
		in.AmountSource = "max"
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	r, err := j.CreateOnchain(in)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentConflict, err, "creating onchain intent")
	}
	return lpResult(r, plan, simulated), nil
}

// lpPreview is the human and JSON summary of an LP intent: what is spent or
// received, the bound limits and every call.
type lpPreview struct {
	Action      string `json:"action"`
	Version     string `json:"version"`
	TokenID     string `json:"token_id,omitempty"`
	Pair        string `json:"pair"`
	FeePercent  string `json:"fee_percent"`
	PriceLower  string `json:"price_lower"`
	PriceUpper  string `json:"price_upper"`
	PriceNow    string `json:"price_current"`
	PriceUnit   string `json:"price_unit"`
	InRange     bool   `json:"in_range"`
	Amount0     string `json:"amount0"`
	Amount1     string `json:"amount1"`
	Limit0      string `json:"limit0"`
	Limit1      string `json:"limit1"`
	LimitKind   string `json:"limit_kind"`
	Native      string `json:"native_value"`
	SlippageBps int    `json:"slippage_bps"`
	Calls       int    `json:"calls"`
	Approvals   int    `json:"approvals"`
	Simulated   bool   `json:"simulated"`
}

func previewOf(p uniswaplp.Plan, calls int, simulated bool) lpPreview {
	price := func(tick int) string {
		s, err := uniswaplp.SqrtRatioAtTick(tick)
		if err != nil {
			return "N/A"
		}
		return formatPrice(uniswaplp.HumanPrice(s, p.Token0.Decimals, p.Token1.Decimals))
	}
	units := func(s string, d int) string {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return "N/A"
		}
		return formatUnits(v, d)
	}
	now := "N/A"
	if sp, ok := new(big.Int).SetString(p.SqrtPriceX96, 10); ok {
		now = formatPrice(uniswaplp.HumanPrice(sp, p.Token0.Decimals, p.Token1.Decimals))
	}
	return lpPreview{
		Action: string(p.Action), Version: string(p.Version), TokenID: p.TokenID, Pair: tokenLabel(p.Token0) + "/" + tokenLabel(p.Token1),
		FeePercent: strconv.FormatFloat(float64(p.Fee)/10000, 'f', -1, 64) + "%",
		PriceLower: price(p.TickLower), PriceUpper: price(p.TickUpper), PriceNow: now, PriceUnit: tokenLabel(p.Token1) + " per " + tokenLabel(p.Token0),
		InRange: p.PoolTick >= p.TickLower && p.PoolTick < p.TickUpper,
		Amount0: units(p.Amount0, p.Token0.Decimals), Amount1: units(p.Amount1, p.Token1.Decimals),
		Limit0: units(p.Limit0, p.Token0.Decimals), Limit1: units(p.Limit1, p.Token1.Decimals), LimitKind: p.LimitKind,
		Native: units(p.Value, 18), SlippageBps: p.SlippageBps, Calls: calls, Approvals: calls - 1, Simulated: simulated,
	}
}

func lpResult(r *journal.OnchainRecord, p uniswaplp.Plan, simulated bool) *result {
	pv := previewOf(p, len(r.Templates), simulated)
	data := struct {
		*journal.OnchainRecord
		Preview lpPreview `json:"preview"`
	}{r, pv}
	return &result{data: data, human: func(w io.Writer) {
		verb := map[string]string{"open": "Open", "increase": "Add to", "decrease": "Remove from", "collect": "Collect fees of", "close": "Close"}[pv.Action]
		target := "new position"
		if pv.TokenID != "" {
			target = "position " + pv.TokenID
		}
		fmt.Fprintf(w, "%s %s: Uniswap %s %s %s on chain %d\n", verb, target, pv.Version, pv.Pair, pv.FeePercent, r.ChainID)
		fmt.Fprintf(w, "Range: %s - %s %s (current %s, %s)\n", pv.PriceLower, pv.PriceUpper, pv.PriceUnit, pv.PriceNow, map[bool]string{true: "in range", false: "out of range"}[pv.InRange])
		flow := "deposit"
		if pv.Action == "decrease" || pv.Action == "close" || pv.Action == "collect" {
			flow = "receive"
		}
		t0, t1, _ := strings.Cut(pv.Pair, "/")
		fmt.Fprintf(w, "Expected %s: %s %s + %s %s\n", flow, pv.Amount0, t0, pv.Amount1, t1)
		if pv.Action != "collect" {
			label := "Minimum"
			if pv.LimitKind == "max" {
				label = "Maximum"
			}
			fmt.Fprintf(w, "%s (slippage %d bps): %s %s + %s %s\n", label, pv.SlippageBps, pv.Limit0, t0, pv.Limit1, t1)
		}
		if pv.Native != "0" {
			fmt.Fprintf(w, "Native value sent: %s\n", pv.Native)
		}
		fmt.Fprintf(w, "Calls: %d (%d approvals)  Simulated: %t\nIntent: %s\nDigest: %s\nState: %s\n", pv.Calls, pv.Approvals, pv.Simulated, r.ID, r.Digest, r.State)
		if r.State == "prepared" {
			fmt.Fprintf(w, "Submit with: oku lp submit --intent %s --digest %s\n", r.ID, r.Digest)
		}
	}}
}

// lpPlanOf decodes and validates the bound plan of an LP record against its
// chain deployment and every stored transaction.
func lpPlanOf(r *journal.OnchainRecord) (uniswaplp.Plan, error) {
	var p uniswaplp.Plan
	if err := json.Unmarshal(r.LP, &p); err != nil {
		return p, err
	}
	d, err := uniswaplp.Lookup(r.ChainID, p.Version)
	if err != nil {
		return p, err
	}
	if p.ChainID != r.ChainID || !strings.EqualFold(p.Owner, r.Sender) || !strings.EqualFold(p.Manager, r.Target) {
		return p, errors.New("plan chain, owner or position manager differs from the intent")
	}
	ts := make([]uniswaplp.Template, len(r.Templates))
	for i, t := range r.Templates {
		v, err := onchain.Quantity(t.Value)
		if err != nil || t.ChainID != r.ChainID || t.Token != "" || t.Spender != "" {
			return p, errors.New("invalid stored transaction")
		}
		ts[i] = uniswaplp.Template{To: t.To, Data: t.Data, Value: v}
	}
	return p, uniswaplp.VerifyTemplates(p, d, ts)
}

func (a *App) cmdLPSubmit(e *env) (*result, error) {
	if str(e, "intent") == "" || str(e, "digest") == "" {
		return nil, output.Errorf(output.CodeUsage, "--intent and --digest required")
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
	r, err := j.GetOnchain(str(e, "intent"))
	if errors.Is(err, journal.ErrNotFound) {
		return nil, output.Errorf(output.CodeNotFound, "intent not found")
	}
	if err != nil {
		return nil, err
	}
	if r.Kind != lpKind || !strings.EqualFold(r.Sender, w.agent) {
		return nil, output.Errorf(output.CodeIntentConflict, "intent does not belong to this agent or command")
	}
	if r.Digest != str(e, "digest") {
		return nil, output.Errorf(output.CodePayloadMismatch, "digest mismatch")
	}
	resume := r.State == "sending" && r.ExecutionMode == "sequential" && r.StepIndex > 0 && !r.StepInFlight
	if r.State != "prepared" && !resume {
		return nil, output.Errorf(output.CodeIntentState, "intent is %s; never resubmit; use lp status", r.State)
	}
	plan, err := lpPlanOf(r)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "refusing unverified liquidity transactions")
	}
	unlock, err := j.LockChain(w.agent, r.ChainID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if r, err = j.GetOnchain(r.ID); err != nil {
		return nil, err
	}
	resume = r.State == "sending" && r.ExecutionMode == "sequential" && r.StepIndex > 0 && !r.StepInFlight
	if r.Digest != str(e, "digest") || r.State != "prepared" && !resume {
		return nil, output.Errorf(output.CodeIntentState, "intent changed while waiting for chain lock")
	}
	if !a.Now().Before(time.Unix(plan.Deadline, 0)) {
		// A resume sits at a recorded boundary with nothing in flight, so
		// closing it cannot orphan a transaction. Earlier confirmed approvals
		// stay on chain and are reused by the next prepare.
		if resume {
			if err = j.AdvanceOnchain(r.ID, "sending", "failed", ""); err != nil {
				return nil, err
			}
		}
		return nil, output.Errorf(output.CodeIntentState, "deadline passed; prepare a new intent")
	}
	ps, walletID, err := a.agentSigner(e, w)
	if err != nil {
		return nil, err
	}
	execCtx, execCancel := context.WithTimeout(e.ctx, onchainExecBudget)
	defer execCancel()
	rpc := a.httpRPC()
	executor := onchain.Executor{Getenv: a.Getenv, RPC: rpc, Sender: ps, Journal: j, WalletID: walletID}
	if resume {
		err = executor.ExecuteSteps(execCtx, r.OnchainIntent, r.StepIndex)
	} else {
		if err = j.BeginOnchain(r.ID, r.Digest, w.agent); err != nil {
			if errors.Is(err, journal.ErrUnresolved) {
				return nil, output.Wrap(output.CodeOutcomeUnknown, err, "reconcile the blocking onchain intent")
			}
			return nil, output.Wrap(output.CodeIntentState, err, "starting onchain intent")
		}
		if err = a.lpExecute(execCtx, j, executor, rpc, r); errors.Is(err, errLPCapability) {
			return nil, output.Wrap(output.CodeUpstream, err, "nothing broadcast; prepare a new lp intent")
		}
	}
	if err != nil {
		return nil, executionError(err, "lp intent", r.ChainID, "")
	}
	if r, err = j.GetOnchain(r.ID); err != nil {
		return nil, err
	}
	return lpResult(r, plan, false), nil
}

// errLPCapability reports a failed EIP-7702 capability read. The intent was
// closed as failed before anything was handed to the signer.
var errLPCapability = errors.New("execution capability check failed")

// lpExecute picks the execution mode like swap submit: one atomic EIP-7702
// batch when the agent supports it, otherwise each call in order with a
// durable resume point after every confirmed step.
func (a *App) lpExecute(ctx context.Context, j *journal.Journal, x onchain.Executor, rpc onchain.HTTPRPC, r *journal.OnchainRecord) error {
	if len(r.Templates) == 1 {
		if err := j.SetExecutionMode(r.ID, "sequential"); err != nil {
			return err
		}
		return x.Execute(ctx, r.OnchainIntent)
	}
	allowed, err := onchain.BatchAllowed(ctx, rpc, r.ChainID, r.Sender)
	if err != nil {
		if closeErr := j.AdvanceOnchain(r.ID, "sending", "failed", ""); closeErr != nil {
			return fmt.Errorf("%w: capability check failed (%v) and closing the intent failed: %v", onchain.ErrUnknown, err, closeErr)
		}
		return fmt.Errorf("%w: %v", errLPCapability, err)
	}
	mode := "sequential"
	if allowed {
		mode = "batch"
	}
	if err = j.SetExecutionMode(r.ID, mode); err != nil {
		return err
	}
	if allowed {
		batcher, ok := x.Sender.(onchain.BatchSender)
		if !ok {
			return fmt.Errorf("%w: signer cannot batch", onchain.ErrUnknown)
		}
		return x.ExecuteBatch(ctx, r.OnchainIntent, batcher, rpc)
	}
	return x.ExecuteSteps(ctx, r.OnchainIntent, 0)
}

func (a *App) cmdLPStatus(e *env) (*result, error) {
	if err := a.advanceSequentialStep(e, lpKind); err != nil {
		return nil, err
	}
	res, err := a.cmdFundsStatus(e, lpKind)
	if err != nil {
		return nil, err
	}
	r, ok := res.data.(*journal.OnchainRecord)
	if !ok {
		return res, nil
	}
	plan, err := lpPlanOf(r)
	if err != nil {
		return nil, output.Wrap(output.CodePayloadMismatch, err, "stored liquidity plan")
	}
	return lpResult(r, plan, false), nil
}

func (a *App) cmdLPReconcile(e *env) (*result, error) {
	return a.cmdFundsReconcile(e, lpKind, a.cmdLPStatus)
}

// lpCheckFunds refuses a deposit the agent cannot pay before anything is
// journaled, so sequential approvals never spend gas on an unfundable action.
func lpCheckFunds(ctx context.Context, t lpTarget, agent string, legs []uniswaplp.Allowances, value *big.Int) error {
	tokens := make([]string, len(legs))
	for i, l := range legs {
		tokens[i] = l.Token
	}
	if len(tokens) > 0 {
		balances, err := t.rpc.TokenBalances(ctx, t.chain, agent, tokens)
		if err != nil {
			return lpReadError(err, "token balances")
		}
		for i, l := range legs {
			if balances[i].Cmp(l.Required) < 0 {
				return output.Errorf(output.CodeInsufficientBalance, "agent holds %s of %s, the deposit needs %s; nothing was journaled", balances[i], l.Token, l.Required)
			}
		}
	}
	if value.Sign() > 0 {
		bal, err := t.rpc.Balance(ctx, t.chain, agent)
		if err != nil {
			return lpReadError(err, "native balance")
		}
		if bal.Cmp(value) < 0 {
			return output.Errorf(output.CodeInsufficientBalance, "agent native balance %s is below the deposit value %s; nothing was journaled", bal, value)
		}
	}
	return nil
}
