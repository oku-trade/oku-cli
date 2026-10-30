package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/onchain"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

// Swap quotes stream in per router. Collect for up to swapQuoteWindow (bounded by
// the request deadline) with long polls of at most swapFlowstreamPoll each.
const (
	swapQuoteWindow    = 8 * time.Second
	swapFlowstreamPoll = 3 * time.Second
)

func routeFlags(bridge bool) []urfave.Flag {
	flags := []urfave.Flag{&urfave.StringFlag{Name: "chain", Usage: "source EVM chain ID supported by Oku swap, e.g. 42161, 8453, 10, 137"}, &urfave.StringFlag{Name: "from", Usage: "input token address, unambiguous symbol, or native (also 0x0 or 0xEeee address)"}, &urfave.StringFlag{Name: "to", Usage: "output token address, unambiguous symbol, or native (also 0x0 or 0xEeee address)"}, &urfave.StringFlag{Name: "amount", Usage: "exact input decimal amount or max/all (full onchain balance)"}, &urfave.StringFlag{Name: "gas-reserve", Usage: "native gas reserve in native units for max/all (default 0.001, Ethereum 0.01)"}, &urfave.IntFlag{Name: "slippage-bps", Value: 50, Usage: "maximum slippage in basis points, 0-300"}, &urfave.StringFlag{Name: "recipient", Usage: "EVM recipient (default: agent wallet)"}, &urfave.StringFlag{Name: "route", Usage: "select only this swap router or bridge"}, &urfave.BoolFlag{Name: "no-intent", Usage: "exclude typed-data intent routes"}, &urfave.IntFlag{Name: "max-loss-bps", Usage: "swap guardrail: refuse prepare when market loss (excluding the Oku fee) exceeds this many bps (default 500)"}, &urfave.BoolFlag{Name: "allow-high-impact", Usage: "swap guardrail override: prepare even when the priced loss exceeds --max-loss-bps (small chains, mispriced oracles)"}}
	if bridge {
		flags = append(flags, &urfave.StringFlag{Name: "destination-chain", Usage: "destination EVM chain ID"})
	}
	return flags
}
func routePrepareFlags(bridge bool) []urfave.Flag {
	return append(routeFlags(bridge), &urfave.StringFlag{Name: "intent-id", Usage: "optional unique intent ID"})
}

type routeQuote struct {
	OrderID        string `json:"order_id"`
	Generation     int32  `json:"generation,omitempty"`
	QuoteID        string `json:"quote_id"`
	Route          string `json:"route"`
	Intent         bool   `json:"intent_based"`
	Target         string `json:"target"`
	Spender        string `json:"spender,omitempty"`
	MinOut         string `json:"min_out"`
	OutAmount      string `json:"out_amount"`
	InAmount       string `json:"in_amount"`
	ResolvedAmount string `json:"resolved_amount,omitempty"`
	AmountSource   string `json:"amount_source,omitempty"`
	Recipient      string `json:"recipient"`
	Chain          uint64 `json:"chain"`
	OutChain       uint64 `json:"out_chain,omitempty"`
	InToken        string `json:"in_token"`
	OutToken       string `json:"out_token"`
	ExpiresAt      string `json:"expires_at"`
	// Informational backend valuations for guardrails; never used for execution.
	InUSD       float64 `json:"in_usd"`
	OutUSD      float64 `json:"out_usd"`
	GasUSD      float64 `json:"gas_usd"`
	FeesUSD     float64 `json:"fees_usd,omitempty"`
	PriceImpact float64 `json:"price_impact,omitempty"`
	// OkuFee is the Oku fee taken by the route, in raw input or output token
	// units, with its estimated USD value. Small trades pay a flat fee.
	OkuFeeRaw   string  `json:"oku_fee_raw,omitempty"`
	OkuFeeToken string  `json:"oku_fee_token,omitempty"`
	OkuFeeUSD   float64 `json:"oku_fee_usd,omitempty"`
	// MarketImpact is out_usd versus in_usd after removing the Oku fee, i.e.
	// the market's price impact alone. Negative is worse.
	MarketImpact float64 `json:"market_impact,omitempty"`
	// LossBps is the priced market loss excluding the Oku fee (positive is
	// worse). HighImpact flags it above the --max-loss-bps guardrail.
	LossBps      int64 `json:"loss_bps"`
	HighImpact   bool  `json:"high_impact"`
	NoSimulation bool  `json:"no_simulation,omitempty"`
	// amountText is the decimal input sent to the backend, resolved from max/all.
	amountText string
}

func positiveRaw(s string) (*big.Int, error) {
	if s == "" {
		return nil, errors.New("missing raw amount")
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > 256 {
		return nil, fmt.Errorf("invalid raw amount %q", s)
	}
	return n, nil
}
func routeTarget(info *v2.ExecutionInformation) string {
	if info == nil || info.Trade == nil {
		return ""
	}
	return info.Trade.To
}
func safeInfo(info *v2.ExecutionInformation) bool {
	return info != nil && info.Trade != nil && info.DepositAddress == "" && info.PsbtHex == "" && info.OpReturnData == "" && len(info.Transactions) <= 1 && len(info.Approvals) == 0
}
func bridgeInfo(info *v2.ExecutionInformation) bool {
	return info != nil && info.Trade != nil && info.DepositAddress == "" && info.PsbtHex == "" && info.OpReturnData == "" && len(info.Transactions) <= 8
}
func (a *App) resolveRouteToken(ctx context.Context, c *api.Client, chain uint64, name string) (string, error) {
	if isNativeToken(name) {
		return "native", nil
	}
	if onchain.ValidAddress(name) {
		return name, nil
	}
	if name == "" {
		return "", output.Errorf(output.CodeInvalidArgument, "token required")
	}
	var res v2.SearchTokensResponse
	if err := c.TokenRPC(ctx, "SearchTokens", &v2.SearchTokensRequest{Query: name, ChainIds: []int32{int32(chain)}, Limit: 250}, &res, false); err != nil {
		return "", apiError(err, "SearchTokens")
	}
	candidates := []string{}
	for _, hit := range res.Results {
		if hit.Token != nil && hit.Token.ChainId == int32(chain) && strings.EqualFold(hit.Token.Symbol, name) && onchain.ValidAddress(hit.Token.Address) {
			candidates = append(candidates, hit.Token.Address)
		}
	}
	sort.Strings(candidates)
	candidates = compactAddresses(candidates)
	if len(candidates) != 1 {
		return "", output.Errorf(output.CodeInvalidArgument, "symbol %q on chain %d has %d matches: %s; use an address", name, chain, len(candidates), strings.Join(candidates, ", "))
	}
	return candidates[0], nil
}
func compactAddresses(in []string) []string {
	out := []string{}
	for _, s := range in {
		if len(out) == 0 || !strings.EqualFold(out[len(out)-1], s) {
			out = append(out, s)
		}
	}
	return out
}
func supportedMarkets(status []*v2.MarketStatus, chain uint64) []string {
	var names []string
	for _, market := range status {
		if market == nil || !market.Active || market.Name == "" {
			continue
		}
		for _, id := range market.SupportedChainIds {
			if uint64(id) == chain {
				names = append(names, market.Name)
				break
			}
		}
	}
	return names
}
func supportedBridges(status []*v2.BridgeStatus, from, to uint64) []string {
	var names []string
	for _, bridge := range status {
		if bridge == nil || !bridge.Active || bridge.Name == "" {
			continue
		}
		fromOK, toOK := false, false
		for _, chain := range bridge.SupportedChains {
			fromOK = fromOK || chain == fmt.Sprintf("eip155:%d", from)
			toOK = toOK || chain == fmt.Sprintf("eip155:%d", to)
		}
		if fromOK && toOK {
			names = append(names, bridge.Name)
		}
	}
	return names
}
func consistentSwapApprovals(legacy []*v2.ApprovalPayload, modern []*v2.ApprovalPayloadV2, chain uint64, token, amount string) bool {
	router := routerFor(chain)
	if len(legacy) == 0 {
		return true
	}
	spender := quoteSpender(modern, token, amount)
	if router == "" || !strings.EqualFold(spender, router) || len(modern) == 0 {
		return false
	}
	for _, approval := range legacy {
		if approval == nil || approval.ChainId != int64(chain) || !strings.EqualFold(approval.Address, token) || !strings.EqualFold(approval.Approvee, spender) || approval.Amount != amount {
			return false
		}
	}
	return true
}

// swapFee extracts the Oku fee from a quote and the market impact without it.
// Input-side fees are priced pro rata from in_usd; output-side fees from out_usd.
func swapFee(info *v2.FeeInfo, inRaw string, inUSD, outUSD float64) (string, string, float64, float64) {
	var raw, token string
	var usd float64
	if f := info.GetInputTokenFeeInfo(); f != nil && f.AmountRaw != "" && f.AmountRaw != "0" {
		raw, token = f.AmountRaw, f.TokenAddress
		fee, ok1 := new(big.Float).SetString(f.AmountRaw)
		in, ok2 := new(big.Float).SetString(inRaw)
		if ok1 && ok2 && in.Sign() > 0 {
			r, _ := new(big.Float).Quo(fee, in).Float64()
			usd = r * inUSD
		}
	} else if f := info.GetOutputTokenFeeInfo(); f != nil && f.AmountRaw != "" && f.AmountRaw != "0" {
		raw, token = f.AmountRaw, f.TokenAddress
		// Output fee is not in out_usd; its USD share is unknown without the
		// gross output, so leave usd at 0 rather than guess.
	}
	impact := 0.0
	if base := inUSD - usd; base > 0 && outUSD > 0 {
		impact = outUSD/base - 1
	}
	return raw, token, usd, impact
}

// swapMinOut returns the quote's minimum output. Some canoe markets (lusor,
// icecreamswap) omit minAmountOutRaw, and some (filecoin usor) never simulate;
// their swap calldata still carries the slippage bound. Then derive
// out*(1-slippage) from the quote's own output and slippage. A simulation
// that ran must meet it; a failed simulation is refused.
func swapMinOut(v *v2.SimpleSwapQuote) (*big.Int, error) {
	if v.MinAmountOutRaw != "" {
		return positiveRaw(v.MinAmountOutRaw)
	}
	out, err := positiveRaw(v.OutAmountRaw)
	if err != nil {
		return nil, err
	}
	if v.IntentBased || v.SlippagePpm <= 0 || v.SlippagePpm > 30_000 || v.Simulation != nil && v.Simulation.Error != "" {
		return nil, errors.New("missing minimum output")
	}
	min := new(big.Int).Mul(out, big.NewInt(int64(1_000_000-v.SlippagePpm)))
	min.Quo(min, big.NewInt(1_000_000))
	if min.Sign() <= 0 {
		return nil, errors.New("derived minimum is zero")
	}
	// When a simulation exists it must meet the derived minimum; without one
	// the minimum is still enforced on chain by the swap calldata.
	if v.Simulation != nil {
		sim, err := positiveRaw(v.Simulation.AmountOutRaw)
		if err != nil {
			return nil, err
		}
		if sim.Cmp(min) < 0 {
			return nil, errors.New("simulation below derived minimum")
		}
	}
	return min, nil
}

func acceptablePriceImpact(impact float64) bool {
	return !math.IsNaN(impact) && !math.IsInf(impact, 0) && impact <= 0.15
}

// defaultMaxLossBps is the default guardrail on value lost to the market
// (excluding the Oku fee), in basis points. Small chains with thin pools or
// stale oracles routinely exceed it; prepare refuses unless --max-loss-bps or
// --allow-high-impact explicitly accepts the loss.
const defaultMaxLossBps = 500

// highImpactReason is error.details.reason of the --max-loss-bps refusal.
const highImpactReason = "high_impact"

// swapLoss returns the market loss fraction (positive is worse) for a quote,
// excluding an input-side Oku fee when its USD share is known. ok is false
// when the quote carries no usable impact at all.
func swapLoss(v *v2.SimpleSwapQuote) (loss float64, ok bool) {
	if v == nil {
		return 0, false
	}
	_, _, feeUSD, marketImpact := swapFee(v.FeeInfo, v.InAmountRaw, v.InUsdValue, v.OutUsdValue)
	if v.InUsdValue > feeUSD && v.OutUsdValue > 0 && !math.IsNaN(marketImpact) && !math.IsInf(marketImpact, 0) {
		return -marketImpact, true
	}
	if math.IsNaN(v.PriceImpact) || math.IsInf(v.PriceImpact, 0) {
		return 0, false
	}
	// Upstream price_impact is positive when output is worth less.
	return v.PriceImpact, true
}

// acceptableSwapImpact keeps the original selection-time sanity bound used
// for picking among routes; it is no longer a hard refusal (see swapLoss).
func acceptableSwapImpact(v *v2.SimpleSwapQuote) bool {
	loss, ok := swapLoss(v)
	return ok && acceptablePriceImpact(loss)
}

// lossBps converts a loss fraction to bps, clamped so absurd oracle values
// (for example a near-zero input price) cannot overflow.
func lossBps(loss float64) int64 {
	if math.IsNaN(loss) {
		return 0
	}
	return int64(math.Round(math.Max(-1_000_000, math.Min(1_000_000, loss*10_000))))
}

// mispriced flags a quote whose priced loss exceeds the guardrail. Only
// losses count: a quote that returns more than the oracle value is fine.
func mispriced(lossBps int64, maxLoss float64) bool {
	if math.IsInf(maxLoss, 1) {
		return false
	}
	return lossBps > int64(math.Round(maxLoss*10_000))
}

// usableImpact requires a finite valuation so the guardrail can be applied.
func usableImpact(v *v2.SimpleSwapQuote) bool {
	_, ok := swapLoss(v)
	return ok
}

// maxLossFraction reads the guardrail: --allow-high-impact disables it,
// --max-loss-bps sets it, otherwise defaultMaxLossBps.
func maxLossFraction(e *env) float64 {
	if e.cmd.Bool("allow-high-impact") {
		return math.Inf(1)
	}
	if n := e.cmd.Int("max-loss-bps"); n > 0 {
		return float64(n) / 10_000
	}
	return float64(defaultMaxLossBps) / 10_000
}
func (a *App) routeInputs(e *env, kind string, prepare bool) (*resolvedWallet, *api.Client, routeQuote, error) {
	var w *resolvedWallet
	var err error
	w, err = agentWalletMetadata(e)
	if err != nil {
		if prepare || str(e, "recipient") == "" {
			return nil, nil, routeQuote{}, err
		}
		p, profileErr := e.profile()
		if profileErr != nil {
			return nil, nil, routeQuote{}, profileErr
		}
		if !onchain.ValidAddress(p.Account) {
			return nil, nil, routeQuote{}, output.Errorf(output.CodeAccountRequired, "quote profile needs an EVM account")
		}
		w = &resolvedWallet{profile: p, agent: p.Account}
	}
	// Chain IDs are bounded to int32 because the API uses int32 chain IDs.
	chain, err := strconv.ParseUint(str(e, "chain"), 10, 31)
	if err != nil || !onchain.Allowed(chain) {
		return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "--chain must be an EVM chain ID")
	}
	// Token decimals, gas and receipts are read from the chain directly.
	if _, rerr := onchain.RPCURL(a.Getenv, chain); rerr != nil {
		return nil, nil, routeQuote{}, output.Wrap(output.CodeConfigError, rerr, "chain RPC")
	}
	dst := chain
	if kind == "bridge" {
		dst, err = strconv.ParseUint(str(e, "destination-chain"), 10, 31)
		if err != nil || !onchain.Allowed(dst) || dst == chain {
			return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "destination must be a different EVM chain ID")
		}
	}
	bps := e.cmd.Int("slippage-bps")
	if bps < 0 || bps > 300 {
		return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "slippage must be 0-300 bps")
	}
	recipient := str(e, "recipient")
	if recipient == "" {
		recipient = w.agent
	}
	if !onchain.ValidAddress(recipient) {
		return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "invalid recipient")
	}
	c := e.client(w.profile)
	ctx, cancel := e.requestCtx()
	defer cancel()
	from, err := a.resolveRouteToken(ctx, c, chain, str(e, "from"))
	if err != nil {
		return nil, nil, routeQuote{}, err
	}
	to, err := a.resolveRouteToken(ctx, c, dst, str(e, "to"))
	if err != nil {
		return nil, nil, routeQuote{}, err
	}
	if from == to && chain == dst {
		return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "input and output token must differ")
	}
	decimals := big.NewInt(18)
	if from != "native" {
		decimals, err = onchain.HTTPRPC{Getenv: a.Getenv}.Call(ctx, chain, from, "0x313ce567")
		if err != nil {
			return nil, nil, routeQuote{}, output.Wrap(output.CodeUpstream, err, "token decimals")
		}
	}
	if !decimals.IsInt64() || decimals.Int64() < 0 || decimals.Int64() > 36 {
		return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "unsupported token decimals")
	}
	amountText := str(e, "amount")
	amountSource := ""
	if isMaxAmount(amountText) {
		amountSource = "max"
		amountText, _, err = a.resolveMaxAmount(e, chain, from, w.agent, int(decimals.Int64()), recipient)
		if err != nil {
			return nil, nil, routeQuote{}, err
		}
	}
	amount, err := parseUnits(amountText, int(decimals.Int64()))
	if err != nil {
		return nil, nil, routeQuote{}, output.Wrap(output.CodeInvalidDecimal, err, "amount")
	}
	// Celo's native CELO is also an ERC-20 at a fixed address, and swap
	// markets only route it as that token. The amount was resolved against
	// the native balance (with its gas reserve) above; spending it through
	// the ERC-20 interface draws from the same balance.
	if kind == "swap" {
		if alias := nativeTokenAlias(chain); alias != "" {
			if from == "native" {
				from = alias
			}
			if to == "native" {
				to = alias
			}
		}
	}
	return w, c, routeQuote{Chain: chain, OutChain: dst, InToken: from, OutToken: to, InAmount: amount.String(), Recipient: recipient, amountText: amountText, ResolvedAmount: amountText, AmountSource: amountSource}, nil
}

// nativeTokenAlias returns the ERC-20 that is the chain's native currency,
// where one exists (Celo's GoldToken).
func nativeTokenAlias(chain uint64) string {
	if chain == 42220 {
		return "0x471EcE3750Da237f93B8E339c536989b8978a438"
	}
	return ""
}

func quoteTokenAddress(kind, token string) string {
	if token == "native" && (kind == "swap" || kind == "bridge") {
		return "0x0000000000000000000000000000000000000000"
	}
	return token
}

func (a *App) fetchRoute(e *env, kind string, prepare bool) (routeQuote, *resolvedWallet, error) {
	w, c, q, err := a.routeInputs(e, kind, prepare)
	if err != nil {
		return q, nil, err
	}
	if prepare {
		s, err := e.unlock(w.profile, "")
		if err != nil {
			return q, nil, err
		}
		s.apply(c)
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	base := q
	q, rej, err := a.fetchRouteAttempt(ctx, e, kind, prepare, w, c, base)
	if err == nil && rej != nil && rej.quoted && retryBudget(ctx) {
		// The backend answered but every route was filtered (or markets were
		// slow): quotes are often transient, so take one fresh quote cycle.
		// Nothing is journaled or signed before a route is chosen.
		select {
		case <-time.After(routeRetryBackoff):
		case <-ctx.Done():
			return q, nil, ctx.Err()
		}
		q, rej, err = a.fetchRouteAttempt(ctx, e, kind, prepare, w, c, base)
		if rej != nil {
			rej.attempts = 2
		}
	}
	if err != nil {
		return q, nil, err
	}
	if rej != nil {
		return q, nil, rej.error(str(e, "route"))
	}
	q.ExpiresAt = a.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano)
	return q, w, nil
}

// fetchRouteAttempt runs one quote cycle. It returns a non-nil rejection when
// the cycle completed but no route was eligible.
func (a *App) fetchRouteAttempt(ctx context.Context, e *env, kind string, prepare bool, w *resolvedWallet, c *api.Client, q routeQuote) (routeQuote, *routeRejection, error) {
	var err error
	ppm := int32(e.cmd.Int("slippage-bps") * 100)
	noIntent := e.cmd.Bool("no-intent")
	reasons := map[string]string{}
	answered, quoted := false, false
	if kind == "swap" {
		var supported v2.GetSupportedMarketsResponse
		if err = c.Swap(ctx, "GetSupportedMarkets", &v2.GetSupportedMarketsRequest{}, &supported, prepare); err != nil {
			return q, nil, apiError(err, "GetSupportedMarkets")
		}
		markets := supportedMarkets(supported.Markets, q.Chain)
		if forced := str(e, "route"); forced != "" {
			markets = filterRoute(markets, forced)
		}
		if len(markets) == 0 {
			return q, nil, output.Errorf(output.CodeRejected, "no active swap markets support chain %d", q.Chain)
		}
		var created v2.SwapCreateResponse
		req := &v2.SwapCreateRequest{Integration: "", Params: &v2.SwapQuoteParams{Chain: int64(q.Chain), EnabledMarkets: markets, Account: w.agent, IsExactIn: true, InTokenAddress: quoteTokenAddress("swap", q.InToken), OutTokenAddress: quoteTokenAddress("swap", q.OutToken), TokenAmount: q.amountText, SlippagePpm: ppm, DstAddress: q.Recipient, UseOkuRouter: true, UsePermit2: false, EnableJit: false}}
		if err = c.Swap(ctx, "Create", req, &created, prepare); err != nil {
			return q, nil, apiError(err, "swap Create")
		}
		q.OrderID = created.OrderId
		// Same quote stream the web app uses: long-poll GetFlowstreamQuotes from the
		// Create timeline, accumulating routers until every enabled market has
		// answered or the overall quote deadline passes, then pick the best.
		collected := map[string]*v2.SimpleSwapQuoteInformation{}
		timeline := created.Timeline
		deadline := time.Now().Add(swapQuoteWindow)
		if d, ok := ctx.Deadline(); ok && time.Until(d)-time.Second < time.Until(deadline) {
			// Leave headroom so a final long-poll never outlives the command budget.
			deadline = d.Add(-time.Second)
		}
		requested := map[string]bool{}
		for _, m := range markets {
			requested[m] = true
		}
		// Done once every enabled market answered, or the server substituted its own
		// router set (e.g. DirectWrap for wraps) and every router it reports is fetched.
		done := func() bool {
			fetched, substituted := 0, false
			for router, entry := range collected {
				if !entry.Fetched {
					return false
				}
				fetched++
				if !requested[router] {
					substituted = true
				}
			}
			return fetched >= len(markets) || substituted
		}
		for !done() && time.Now().Before(deadline) {
			wait := time.Until(deadline)
			if wait > swapFlowstreamPoll {
				wait = swapFlowstreamPoll
			}
			// The server returns every router's state; only finished ones count as fetched.
			fetchedRouters := make([]string, 0, len(collected))
			for router, entry := range collected {
				if entry.Fetched {
					fetchedRouters = append(fetchedRouters, router)
				}
			}
			sort.Strings(fetchedRouters)
			var stream v2.SwapGetFlowstreamQuotesResponse
			err = c.Swap(ctx, "GetFlowstreamQuotes", &v2.SwapGetFlowstreamQuotesRequest{OrderId: q.OrderID, Timeline: timeline, FetchedRouters: fetchedRouters, WaitTime: int32(wait.Milliseconds())}, &stream, prepare)
			if err != nil {
				if len(fetchedRouters) > 0 {
					// Keep the quotes already collected; a late poll failure should not discard them.
					break
				}
				return q, nil, apiError(err, "swap GetFlowstreamQuotes")
			}
			if stream.Timeline != timeline {
				// Params changed server-side: quotes from the old timeline are stale.
				collected = map[string]*v2.SimpleSwapQuoteInformation{}
				timeline = stream.Timeline
			}
			for router, entry := range stream.Quotes {
				if entry != nil {
					collected[router] = entry
				}
			}
			if ctx.Err() != nil {
				return q, nil, ctx.Err()
			}
		}
		{
			quotes := v2.SwapGetQuotesResponse{Quotes: collected}
			keys := make([]string, 0, len(quotes.Quotes))
			for key := range quotes.Quotes {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			best := math.Inf(-1)
			bestInBound := false
			for _, key := range keys {
				entry := quotes.Quotes[key]
				if entry != nil && entry.Fetched && entry.Quote != nil && entry.Error == nil {
					answered, quoted = true, true
				}
				if reason := swapRejectReason(entry, q, noIntent); reason != "" {
					reasons[strings.ToLower(key)] = reason
					continue
				}
				v := entry.Quote
				min, _ := swapMinOut(v)
				// Prefer routes inside the selection bound; a high-impact route is
				// only chosen when no in-bound route exists, and is then flagged.
				inBound := v.IntentBased || acceptableSwapImpact(v)
				if inBound && !bestInBound || inBound == bestInBound && v.NetUsdValue > best {
					best = v.NetUsdValue
					bestInBound = inBound
					q.QuoteID = entry.QuoteId
					q.Route = entry.Router
					q.Intent = v.IntentBased
					q.Target = ""
					if !v.IntentBased {
						q.Target = routerFor(q.Chain)
					}
					q.MinOut = min.String()
					q.OutAmount = v.OutAmountRaw
					q.InUSD, q.OutUSD, q.GasUSD, q.PriceImpact = v.InUsdValue, v.OutUsdValue, v.GasUsdValue, v.PriceImpact
					q.OkuFeeRaw, q.OkuFeeToken, q.OkuFeeUSD, q.MarketImpact = swapFee(v.FeeInfo, q.InAmount, v.InUsdValue, v.OutUsdValue)
					q.LossBps = 0
					if loss, ok := swapLoss(v); ok {
						q.LossBps = lossBps(loss)
					}
					q.HighImpact = !inBound || mispriced(q.LossBps, maxLossFraction(e))
					q.NoSimulation = !v.IntentBased && v.Simulation == nil
					q.Spender = quoteSpender(v.ApprovalsV2, q.InToken, q.InAmount)
				}
			}
			for _, m := range markets {
				if _, ok := collected[m]; !ok && !done() {
					reasons[strings.ToLower(m)] = "not_answered"
					answered = true
				}
			}
		}
	} else {
		var supported v2.GetSupportedBridgesResponse
		if err = c.Bridge(ctx, "GetSupportedBridges", &v2.GetSupportedBridgesRequest{}, &supported, prepare); err != nil {
			return q, nil, apiError(err, "GetSupportedBridges")
		}
		bridges := supportedBridges(supported.Bridges, q.Chain, q.OutChain)
		if forced := str(e, "route"); forced != "" {
			bridges = filterRoute(bridges, forced)
		}
		if len(bridges) == 0 {
			return q, nil, output.Errorf(output.CodeRejected, "no active bridge supports both chains")
		}
		var created v2.BridgeCreateResponse
		req := &v2.BridgeCreateRequest{Integration: "", Params: &v2.BridgeQuoteParams{InChain: int64(q.Chain), OutChain: int64(q.OutChain), EnabledBridges: bridges, InChainCaip2: fmt.Sprintf("eip155:%d", q.Chain), OutChainCaip2: fmt.Sprintf("eip155:%d", q.OutChain), Account: w.agent, DstAddress: q.Recipient, InTokenAddress: quoteTokenAddress("bridge", q.InToken), OutTokenAddress: quoteTokenAddress("bridge", q.OutToken), InTokenAmount: q.amountText, SlippagePpm: ppm}}
		if err = c.Bridge(ctx, "Create", req, &created, prepare); err != nil {
			return q, nil, apiError(err, "bridge Create")
		}
		q.OrderID = created.OrderId
		var updated v2.BridgeUpdateQuoteParamsResponse
		if err = c.Bridge(ctx, "UpdateQuoteParams", &v2.BridgeUpdateQuoteParamsRequest{OrderId: q.OrderID, Params: req.Params}, &updated, prepare); err != nil {
			return q, nil, apiError(err, "bridge UpdateQuoteParams")
		}
		if updated.Generation < 0 || updated.Generation > math.MaxInt32 || updated.OrderId != "" && updated.OrderId != q.OrderID {
			return q, nil, output.Errorf(output.CodePayloadMismatch, "bridge quote generation or order differs")
		}
		// Like the web app: after a successful UpdateQuoteParams the quote
		// generation is 1 (the response often reports 0).
		q.Generation = int32(updated.Generation)
		if q.Generation == 0 {
			q.Generation = 1
		}
		collected := map[string]*v2.BridgeQuoteInformation{}
		deadline := time.Now().Add(swapQuoteWindow)
		if d, ok := ctx.Deadline(); ok && d.Add(-time.Second).Before(deadline) {
			deadline = d.Add(-time.Second)
		}
		for time.Now().Before(deadline) {
			fetched := make([]string, 0, len(collected))
			for name, entry := range collected {
				if entry != nil && entry.Fetched {
					fetched = append(fetched, name)
				}
			}
			if len(fetched) >= len(bridges) {
				break
			}
			sort.Strings(fetched)
			wait := time.Until(deadline)
			if wait > swapFlowstreamPoll {
				wait = swapFlowstreamPoll
			}
			var quotes v2.BridgeGetQuotesResponse
			err = c.Bridge(ctx, "GetNewQuotes", &v2.BridgeGetNewQuotesRequest{OrderId: q.OrderID, FetchedBridges: fetched, WaitTime: int64(wait.Milliseconds()), Generation: q.Generation}, &quotes, prepare)
			if err != nil {
				if len(fetched) > 0 {
					break
				}
				return q, nil, apiError(err, "bridge GetNewQuotes")
			}
			for name, entry := range quotes.Quotes {
				if entry != nil {
					collected[name] = entry
				}
			}
			if ctx.Err() != nil && len(fetched) == 0 {
				return q, nil, ctx.Err()
			}
		}
		{
			quotes := v2.BridgeGetQuotesResponse{Quotes: collected}
			keys := make([]string, 0, len(quotes.Quotes))
			for key := range quotes.Quotes {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			best := math.Inf(-1)
			for _, name := range bridges {
				if _, ok := collected[name]; !ok {
					reasons[strings.ToLower(name)] = "not_answered"
					answered = true
				}
			}
			for _, key := range keys {
				entry := quotes.Quotes[key]
				if entry != nil && entry.Fetched && entry.Quote != nil && entry.Error == nil {
					answered, quoted = true, true
				}
				if reason := bridgeRejectReason(entry, q, noIntent); reason != "" {
					reasons[strings.ToLower(key)] = reason
					continue
				}
				v := entry.Quote
				min, _ := parseUnits(v.MinAmountOut, int(v.OutToken.Decimals))
				net := v.OutUsdValue - v.FeesUsdValue - v.GasUsdValue
				if net > best {
					out, _ := parseUnits(v.OutAmount, int(v.OutToken.Decimals))
					best = net
					q.QuoteID = entry.QuoteId
					q.Route = entry.Bridge
					q.Intent = v.IntentBased
					// Bridge targets are only available from authenticated GetExecutionInfo.
					q.Target = ""
					q.MinOut = min.String()
					q.OutAmount = out.String()
					q.InUSD, q.OutUSD, q.GasUSD, q.FeesUSD = v.InUsdValue, v.OutUsdValue, v.GasUsdValue, v.FeesUsdValue
					q.Spender = quoteSpender(v.Approvals, q.InToken, q.InAmount)
				}
			}
		}
	}
	if q.OrderID == "" || q.QuoteID == "" {
		return q, &routeRejection{reasons: reasons, answered: answered, quoted: quoted, attempts: 1}, nil
	}
	return q, nil, nil
}
func bridgeTokenMatches(quoted, requested string) bool {
	if strings.EqualFold(quoted, requested) {
		return true
	}
	return requested == "native" && isNativeToken(quoted)
}
func filterRoute(names []string, forced string) []string {
	for _, name := range names {
		if strings.EqualFold(name, forced) {
			return []string{name}
		}
	}
	return nil
}
func quoteSpender(approvals []*v2.ApprovalPayloadV2, token, amount string) string {
	if len(approvals) == 0 {
		return ""
	}
	spender := ""
	for _, ap := range approvals {
		if ap == nil || !strings.EqualFold(ap.To, token) || ap.FunctionName != "approve" || ap.Values == nil {
			return ""
		}
		v := ap.Values.AsMap()
		s, _ := v["spender"].(string)
		raw := fmt.Sprint(v["amount"])
		if !onchain.ValidAddress(s) || raw != amount || spender != "" && !strings.EqualFold(spender, s) {
			return ""
		}
		spender = s
	}
	return spender
}
func (a *App) cmdRouteQuote(e *env, kind string) (*result, error) {
	q, _, err := a.fetchRoute(e, kind, false)
	if err != nil {
		return nil, err
	}
	e.setTelemetryRoute(q.Route)
	w, err := e.profile()
	if err == nil {
		ctx, cancel := e.requestCtx()
		defer cancel()
		var response v2.CancelResponse
		request := &v2.CancelRequest{OrderId: q.OrderID}
		client := e.client(w)
		if kind == "swap" {
			_ = client.Swap(ctx, "Cancel", request, &response, false)
		} else {
			_ = client.Bridge(ctx, "Cancel", request, &response, false)
		}
	}
	return routeResult(q), nil
}
func routeResult(q routeQuote) *result {
	return &result{data: q, human: func(w io.Writer) {
		fmt.Fprintf(w, "%s quote %s: %s -> %s; minimum %s; source %d destination %d; recipient %s\n", q.Route, q.QuoteID, q.InAmount, q.OutAmount, q.MinOut, q.Chain, q.OutChain, q.Recipient)
	}}
}
func (a *App) cmdSwapQuote(e *env) (*result, error)   { return a.cmdRouteQuote(e, "swap") }
func (a *App) cmdBridgeQuote(e *env) (*result, error) { return a.cmdRouteQuote(e, "bridge") }
func (a *App) cmdRoutePrepare(e *env, kind string) (*result, error) {
	q, w, err := a.fetchRoute(e, kind, true)
	if err != nil {
		return nil, err
	}
	e.setTelemetryRoute(q.Route)
	if kind == "swap" && !q.Intent && q.HighImpact && !e.cmd.Bool("allow-high-impact") {
		return nil, output.Errorf(output.CodeRejected, "high impact: priced loss %.2f%% (in_usd %.4f, out_usd %.4f, oku fee %.4f) exceeds --max-loss-bps %d; rerun with a higher --max-loss-bps or --allow-high-impact only after reviewing the quote", float64(q.LossBps)/100, q.InUSD, q.OutUSD, q.OkuFeeUSD, int(maxLossFraction(e)*10_000)).WithDetail("reason", highImpactReason)
	}
	router := routerFor(q.Chain)
	if kind == "swap" && !q.Intent && (router == "" || !strings.EqualFold(q.Target, router) || q.InToken != "native" && q.Spender != "" && !strings.EqualFold(q.Spender, router)) {
		return nil, output.Errorf(output.CodeRejected, "swap route or approval spender is not OkuRouter")
	}
	if kind == "swap" && !q.Intent && q.InToken != "native" {
		q.Spender = router
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	// Bridge's trade target is unknown until submit. This placeholder cannot be
	// executed: submit replaces it with validated execution before BeginOnchain.
	placeholders := []journal.OnchainTemplate{{ChainID: q.Chain, To: q.Target, Value: "0x0"}}
	if q.InToken != "native" {
		placeholders = append([]journal.OnchainTemplate{{ChainID: q.Chain, To: q.InToken, Value: "0x0", Token: q.InToken, Amount: onchain.Hex(mustRaw(q.InAmount))}}, placeholders...)
	}
	if q.Intent {
		placeholders = nil // No source trade transaction. Approvals are bound at submit.
	}
	in := journal.OnchainIntent{ID: id, Kind: kind, Intent: q.Intent, ChainID: q.Chain, OutChain: q.OutChain, Sender: w.agent, Recipient: q.Recipient, Token: q.InToken, OutToken: q.OutToken, Amount: q.InAmount, OrderID: q.OrderID, QuoteID: q.QuoteID, Generation: q.Generation, Route: q.Route, Target: q.Target, Spender: q.Spender, MinOut: q.MinOut, ExpiresAt: q.ExpiresAt, CreatedAt: a.Now().UTC().Format(time.RFC3339Nano), Templates: placeholders}
	if q.AmountSource != "" {
		in.ResolvedAmount = q.ResolvedAmount
		in.AmountSource = q.AmountSource
	}
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	r, err := j.CreateOnchain(in)
	if err != nil {
		return nil, output.Wrap(output.CodeIntentConflict, err, "creating route intent")
	}
	res := routeRecord(r)
	res.data = preparedRoute{OnchainRecord: r, Valuation: valuationOf(q)}
	return res, nil
}

// routeValuation is the backend's informational pricing of the prepared
// quote, for guardrails. It is not part of the digest and never executed.
type routeValuation struct {
	InUSD        float64 `json:"in_usd"`
	OutUSD       float64 `json:"out_usd"`
	GasUSD       float64 `json:"gas_usd"`
	FeesUSD      float64 `json:"fees_usd,omitempty"`
	PriceImpact  float64 `json:"price_impact,omitempty"`
	OkuFeeRaw    string  `json:"oku_fee_raw,omitempty"`
	OkuFeeToken  string  `json:"oku_fee_token,omitempty"`
	OkuFeeUSD    float64 `json:"oku_fee_usd,omitempty"`
	MarketImpact float64 `json:"market_impact,omitempty"`
	OutAmount    string  `json:"out_amount"`
	LossBps      int64   `json:"loss_bps"`
	HighImpact   bool    `json:"high_impact"`
	NoSimulation bool    `json:"no_simulation,omitempty"`
}

type preparedRoute struct {
	*journal.OnchainRecord
	Valuation routeValuation `json:"valuation"`
}

func valuationOf(q routeQuote) routeValuation {
	return routeValuation{InUSD: q.InUSD, OutUSD: q.OutUSD, GasUSD: q.GasUSD, FeesUSD: q.FeesUSD, PriceImpact: q.PriceImpact, OkuFeeRaw: q.OkuFeeRaw, OkuFeeToken: q.OkuFeeToken, OkuFeeUSD: q.OkuFeeUSD, MarketImpact: q.MarketImpact, OutAmount: q.OutAmount, LossBps: q.LossBps, HighImpact: q.HighImpact, NoSimulation: q.NoSimulation}
}
func mustRaw(s string) *big.Int { n, _ := positiveRaw(s); return n }
func routeRecord(r *journal.OnchainRecord) *result {
	return &result{data: r, human: func(w io.Writer) {
		fmt.Fprintf(w, "%s intent %s: %s on chain %d -> %s; minimum output %s\nDigest: %s\nState: %s\n", r.Kind, r.ID, r.Amount, r.ChainID, r.Recipient, r.MinOut, r.Digest, r.State)
		if r.State == "prepared" {
			fmt.Fprintf(w, "Submit with: oku %s submit --intent %s --digest %s\n", r.Kind, r.ID, r.Digest)
		}
	}}
}
func (a *App) cmdSwapPrepare(e *env) (*result, error)   { return a.cmdRoutePrepare(e, "swap") }
func (a *App) cmdBridgePrepare(e *env) (*result, error) { return a.cmdRoutePrepare(e, "bridge") }

// routeRetryBackoff is the pause before the single automatic re-quote.
const routeRetryBackoff = 2 * time.Second

// retryBudget reports whether enough of the command deadline remains for a
// backoff plus a useful (possibly shortened) quote cycle. The cycle itself is
// bounded by the same deadline.
func retryBudget(ctx context.Context) bool {
	d, ok := ctx.Deadline()
	return !ok || time.Until(d) > routeRetryBackoff+5*time.Second
}

// routeRejection explains why no route was eligible. answered means the
// backend returned quotes (all filtered) or markets did not answer in time,
// which is transient-looking and therefore retryable.
type routeRejection struct {
	reasons  map[string]string
	answered bool
	quoted   bool // at least one route returned a quote
	attempts int
}

func (r *routeRejection) error(forced string) *output.Error {
	if forced != "" {
		if detail := r.reasons[strings.ToLower(forced)]; strings.HasPrefix(detail, "backend_error: ") {
			err := output.Errorf(output.CodeRejected, "no eligible route: %s: %s", forced, strings.TrimPrefix(detail, "backend_error: "))
			return r.decorate(err)
		}
	}
	msg := "no eligible route for requested tokens and amount"
	if r.attempts > 1 {
		msg += fmt.Sprintf(" after %d attempts", r.attempts)
	}
	if len(r.reasons) > 0 {
		keys := make([]string, 0, len(r.reasons))
		for k := range r.reasons {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+r.reasons[k])
		}
		msg += " (" + strings.Join(parts, "; ") + ")"
	}
	return r.decorate(output.Errorf(output.CodeRejected, "%s", msg))
}

func (r *routeRejection) decorate(err *output.Error) *output.Error {
	routes := map[string]string{}
	for k, v := range r.reasons {
		routes[k] = v
	}
	err = err.WithDetail("routes", routes).WithDetail("attempts", r.attempts)
	if r.answered {
		err = err.WithRetry()
	}
	return err
}

func backendReason(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "..."
	}
	return "backend_error: " + msg
}

// swapRejectReason returns why a swap route is ineligible, or "" if usable.
func swapRejectReason(entry *v2.SimpleSwapQuoteInformation, q routeQuote, noIntent bool) string {
	switch {
	case entry == nil:
		return "not_answered"
	case entry.Error != nil:
		return backendReason(entry.Error.Message)
	case !entry.Fetched:
		return "not_fetched"
	case entry.Quote == nil:
		return "no_quote"
	}
	v := entry.Quote
	switch {
	case noIntent && v.IntentBased:
		return "intent_excluded"
	case v.ChainId != int32(q.Chain):
		return fmt.Sprintf("chain_mismatch: got %d", v.ChainId)
	case !v.IsExactIn:
		return "not_exact_in"
	case !v.IntentBased && !usableImpact(v):
		return "unusable_impact"
	case math.IsNaN(v.NetUsdValue):
		return "net_usd_nan"
	}
	input, ie := positiveRaw(v.InAmountRaw)
	if ie != nil || input.String() != q.InAmount {
		return fmt.Sprintf("amount_mismatch: got %s want %s", v.InAmountRaw, q.InAmount)
	}
	if _, me := swapMinOut(v); me != nil {
		return "min_out_invalid"
	}
	if v.IntentBased {
		if len(v.SigningRequest.GetTypedData()) == 0 || v.SigningRequest.TypedData[0].GetPayload() == nil {
			return "intent_without_typed_data"
		}
	} else {
		// A missing simulation is accepted (some small-chain markets never
		// simulate); a simulation that ran and failed is not.
		if routerFor(q.Chain) == "" {
			return "no_router_for_chain"
		}
		if v.Simulation != nil && v.Simulation.Error != "" {
			return "simulation_failed: " + strings.TrimPrefix(backendReason(v.Simulation.Error), "backend_error: ")
		}
		if v.Simulation != nil {
			if _, se := positiveRaw(v.Simulation.AmountOutRaw); se != nil {
				return "simulation_no_output"
			}
		}
	}
	if v.InToken == nil || v.OutToken == nil || !bridgeTokenMatches(v.InToken.Address, q.InToken) || !bridgeTokenMatches(v.OutToken.Address, q.OutToken) {
		return "token_mismatch"
	}
	if !v.IntentBased {
		if len(v.SigningRequest.GetTypedData()) > 0 {
			return "typed_data_on_router_route"
		}
		if !consistentSwapApprovals(v.Approvals, v.ApprovalsV2, q.Chain, q.InToken, q.InAmount) {
			return "approvals_inconsistent"
		}
		spender := quoteSpender(v.ApprovalsV2, q.InToken, q.InAmount)
		if q.InToken != "native" && (len(v.ApprovalsV2) > 0 || len(v.Approvals) > 0) && !strings.EqualFold(spender, routerFor(q.Chain)) {
			return "spender_not_router: " + spender
		}
	}
	return ""
}

// bridgeRejectReason returns why a bridge route is ineligible, or "" if usable.
func bridgeRejectReason(entry *v2.BridgeQuoteInformation, q routeQuote, noIntent bool) string {
	switch {
	case entry == nil:
		return "not_answered"
	case entry.Error != nil:
		return backendReason(entry.Error.Message)
	case !entry.Fetched:
		return "not_fetched"
	case entry.Quote == nil:
		return "no_quote"
	}
	v := entry.Quote
	switch {
	case noIntent && v.IntentBased:
		return "intent_excluded"
	case v.InChainId != int32(q.Chain) || v.OutChainId != int32(q.OutChain):
		return fmt.Sprintf("chain_mismatch: got %d->%d", v.InChainId, v.OutChainId)
	case v.IntentBased && (len(v.SigningRequest.GetTypedData()) == 0 || v.SigningRequest.TypedData[0].GetPayload() == nil):
		return "intent_without_typed_data"
	case !v.IntentBased && len(v.SigningRequest.GetTypedData()) > 0:
		return "typed_data_on_router_route"
	case v.InToken == nil || v.OutToken == nil || !bridgeTokenMatches(v.InToken.Address, q.InToken) || !bridgeTokenMatches(v.OutToken.Address, q.OutToken):
		return "token_mismatch"
	}
	input, ie := parseUnits(v.InAmount, int(v.InToken.Decimals))
	if ie != nil || input.String() != q.InAmount {
		got := v.InAmount
		if ie == nil {
			got = input.String()
		}
		return fmt.Sprintf("amount_mismatch: got %s want %s", got, q.InAmount)
	}
	if _, me := parseUnits(v.MinAmountOut, int(v.OutToken.Decimals)); me != nil {
		return "min_out_invalid"
	}
	if net := v.OutUsdValue - v.FeesUsdValue - v.GasUsdValue; math.IsNaN(net) || math.IsInf(net, 0) {
		return "net_usd_invalid"
	}
	if _, oe := parseUnits(v.OutAmount, int(v.OutToken.Decimals)); oe != nil {
		return "out_amount_invalid"
	}
	return ""
}
