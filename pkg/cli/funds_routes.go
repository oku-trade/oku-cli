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
	flags := []urfave.Flag{&urfave.StringFlag{Name: "chain", Usage: "source EVM chain ID supported by Oku swap, e.g. 42161, 8453, 10, 137"}, &urfave.StringFlag{Name: "from", Usage: "input token address or unambiguous symbol"}, &urfave.StringFlag{Name: "to", Usage: "output token address or unambiguous symbol"}, &urfave.StringFlag{Name: "amount", Usage: "exact input decimal amount or max/all (full onchain balance)"}, &urfave.StringFlag{Name: "gas-reserve", Usage: "native gas reserve in native units for max/all (default 0.001, Ethereum 0.01)"}, &urfave.IntFlag{Name: "slippage-bps", Value: 50, Usage: "maximum slippage in basis points, 0-300"}, &urfave.StringFlag{Name: "recipient", Usage: "EVM recipient (default: agent wallet)"}, &urfave.StringFlag{Name: "route", Usage: "select only this swap router or bridge"}, &urfave.BoolFlag{Name: "no-intent", Usage: "exclude typed-data intent routes"}}
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
	if strings.EqualFold(name, "native") {
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
func acceptablePriceImpact(impact float64) bool {
	return !math.IsNaN(impact) && impact <= 0.15
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
	chain, err := strconv.ParseUint(str(e, "chain"), 10, 64)
	if err != nil || !onchain.Allowed(chain) {
		return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "--chain must be an EVM chain ID")
	}
	// Token decimals, gas and receipts are read from the chain directly.
	if _, rerr := onchain.RPCURL(a.Getenv, chain); rerr != nil {
		return nil, nil, routeQuote{}, output.Wrap(output.CodeConfigError, rerr, "chain RPC")
	}
	dst := chain
	if kind == "bridge" {
		dst, err = strconv.ParseUint(str(e, "destination-chain"), 10, 64)
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
	return w, c, routeQuote{Chain: chain, OutChain: dst, InToken: from, OutToken: to, InAmount: amount.String(), Recipient: recipient, amountText: amountText, ResolvedAmount: amountText, AmountSource: amountSource}, nil
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
	ppm := int32(e.cmd.Int("slippage-bps") * 100)
	routeErrors := map[string]string{}
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
			for _, key := range keys {
				entry := quotes.Quotes[key]
				if entry != nil && entry.Error != nil && entry.Error.Message != "" {
					routeErrors[strings.ToLower(key)] = entry.Error.Message
				}
				if entry == nil || !entry.Fetched || entry.Error != nil || entry.Quote == nil {
					continue
				}
				v := entry.Quote
				if e.cmd.Bool("no-intent") && v.IntentBased || v.ChainId != int32(q.Chain) || !v.IsExactIn || !v.IntentBased && !acceptablePriceImpact(v.PriceImpact) || math.IsNaN(v.NetUsdValue) {
					continue
				}
				input, ie := positiveRaw(v.InAmountRaw)
				min, me := positiveRaw(v.MinAmountOutRaw)
				if ie != nil || me != nil || input.String() != q.InAmount {
					continue
				}
				if v.IntentBased {
					if len(v.SigningRequest.GetTypedData()) == 0 || v.SigningRequest.TypedData[0].GetPayload() == nil {
						continue
					}
				} else {
					if routerFor(q.Chain) == "" || v.Simulation == nil || v.Simulation.Error != "" {
						continue
					}
					if _, se := positiveRaw(v.Simulation.AmountOutRaw); se != nil {
						continue
					}
				}
				if v.InToken == nil || v.OutToken == nil || !bridgeTokenMatches(v.InToken.Address, q.InToken) || !bridgeTokenMatches(v.OutToken.Address, q.OutToken) {
					continue
				}
				if !v.IntentBased && (len(v.SigningRequest.GetTypedData()) > 0 || !consistentSwapApprovals(v.Approvals, v.ApprovalsV2, q.Chain, q.InToken, q.InAmount)) {
					continue
				}
				spender := quoteSpender(v.ApprovalsV2, q.InToken, q.InAmount)
				if !v.IntentBased && q.InToken != "native" && (len(v.ApprovalsV2) > 0 || len(v.Approvals) > 0) && !strings.EqualFold(spender, routerFor(q.Chain)) {
					continue
				}
				if v.NetUsdValue > best {
					best = v.NetUsdValue
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
					q.Spender = quoteSpender(v.ApprovalsV2, q.InToken, q.InAmount)
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
			for _, key := range keys {
				entry := quotes.Quotes[key]
				if entry != nil && entry.Error != nil && entry.Error.Message != "" {
					routeErrors[strings.ToLower(key)] = entry.Error.Message
				}
				if entry == nil || !entry.Fetched || entry.Error != nil || entry.Quote == nil {
					continue
				}
				v := entry.Quote
				if e.cmd.Bool("no-intent") && v.IntentBased || v.InChainId != int32(q.Chain) || v.OutChainId != int32(q.OutChain) {
					continue
				}
				if v.IntentBased && (len(v.SigningRequest.GetTypedData()) == 0 || v.SigningRequest.TypedData[0].GetPayload() == nil) || !v.IntentBased && len(v.SigningRequest.GetTypedData()) > 0 {
					continue
				}
				if v.InToken == nil || v.OutToken == nil || !bridgeTokenMatches(v.InToken.Address, q.InToken) || !bridgeTokenMatches(v.OutToken.Address, q.OutToken) {
					continue
				}
				input, ie := parseUnits(v.InAmount, int(v.InToken.Decimals))
				min, me := parseUnits(v.MinAmountOut, int(v.OutToken.Decimals))
				if ie != nil || me != nil || input.String() != q.InAmount {
					continue
				}
				net := v.OutUsdValue - v.FeesUsdValue - v.GasUsdValue
				if math.IsNaN(net) || math.IsInf(net, 0) {
					continue
				}
				if net > best {
					out, oe := parseUnits(v.OutAmount, int(v.OutToken.Decimals))
					if oe != nil {
						continue
					}
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
		if forced := str(e, "route"); forced != "" {
			if detail := routeErrors[strings.ToLower(forced)]; detail != "" {
				return q, nil, output.Errorf(output.CodeRejected, "no eligible route: %s: %s", forced, detail)
			}
		}
		return q, nil, output.Errorf(output.CodeRejected, "no eligible route for requested tokens and amount")
	}
	q.ExpiresAt = a.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano)
	return q, w, nil
}
func bridgeTokenMatches(quoted, requested string) bool {
	if strings.EqualFold(quoted, requested) {
		return true
	}
	return requested == "native" && (strings.EqualFold(quoted, "0x0000000000000000000000000000000000000000") || strings.EqualFold(quoted, "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE"))
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
	return routeRecord(r), nil
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
