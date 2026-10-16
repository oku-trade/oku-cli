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

func routeFlags(bridge bool) []urfave.Flag {
	flags := []urfave.Flag{&urfave.StringFlag{Name: "chain", Usage: "source EVM chain ID (42161, 8453, 1)"}, &urfave.StringFlag{Name: "from", Usage: "input token address or unambiguous symbol"}, &urfave.StringFlag{Name: "to", Usage: "output token address or unambiguous symbol"}, &urfave.StringFlag{Name: "amount", Usage: "exact input decimal amount"}, &urfave.IntFlag{Name: "slippage-bps", Value: 50, Usage: "maximum slippage in basis points, 0-300"}, &urfave.StringFlag{Name: "recipient", Usage: "EVM recipient (default: agent wallet)"}}
	if bridge {
		flags = append(flags, &urfave.StringFlag{Name: "destination-chain", Usage: "destination EVM chain ID"})
	}
	return flags
}
func routePrepareFlags(bridge bool) []urfave.Flag {
	return append(routeFlags(bridge), &urfave.StringFlag{Name: "intent-id", Usage: "optional unique intent ID"})
}

type routeQuote struct {
	OrderID   string `json:"order_id"`
	QuoteID   string `json:"quote_id"`
	Route     string `json:"route"`
	Target    string `json:"target"`
	Spender   string `json:"spender,omitempty"`
	MinOut    string `json:"min_out"`
	OutAmount string `json:"out_amount"`
	InAmount  string `json:"in_amount"`
	Recipient string `json:"recipient"`
	Chain     uint64 `json:"chain"`
	OutChain  uint64 `json:"out_chain,omitempty"`
	InToken   string `json:"in_token"`
	OutToken  string `json:"out_token"`
	ExpiresAt string `json:"expires_at"`
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
		return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "--chain must be 42161, 8453, or 1")
	}
	dst := chain
	if kind == "bridge" {
		dst, err = strconv.ParseUint(str(e, "destination-chain"), 10, 64)
		if err != nil || !onchain.Allowed(dst) || dst == chain {
			return nil, nil, routeQuote{}, output.Errorf(output.CodeInvalidArgument, "destination must be a different allowlisted EVM chain")
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
	amount, err := parseUnits(str(e, "amount"), int(decimals.Int64()))
	if err != nil {
		return nil, nil, routeQuote{}, output.Wrap(output.CodeInvalidDecimal, err, "amount")
	}
	return w, c, routeQuote{Chain: chain, OutChain: dst, InToken: from, OutToken: to, InAmount: amount.String(), Recipient: recipient}, nil
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
	if kind == "swap" {
		var created v2.SwapCreateResponse
		req := &v2.SwapCreateRequest{Integration: "", Params: &v2.SwapQuoteParams{Chain: int64(q.Chain), Account: w.agent, IsExactIn: true, InTokenAddress: q.InToken, OutTokenAddress: q.OutToken, TokenAmount: q.InAmount, SlippagePpm: ppm, DstAddress: q.Recipient, UsePermit2: false, EnableJit: false}}
		if err = c.Swap(ctx, "Create", req, &created, prepare); err != nil {
			return q, nil, apiError(err, "swap Create")
		}
		q.OrderID = created.OrderId
		for attempt := 0; attempt < 5; attempt++ {
			var quotes v2.SwapGetQuotesResponse
			err = c.Swap(ctx, "GetQuotes", &v2.SwapGetQuotesRequest{OrderId: q.OrderID}, &quotes, prepare)
			if err != nil {
				return q, nil, apiError(err, "swap GetQuotes")
			}
			keys := make([]string, 0, len(quotes.Quotes))
			for key := range quotes.Quotes {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			best := math.Inf(-1)
			for _, key := range keys {
				entry := quotes.Quotes[key]
				if entry == nil || !entry.Fetched || entry.Error != nil || entry.Quote == nil {
					continue
				}
				v := entry.Quote
				if v.IntentBased || v.ChainId != int32(q.Chain) || !v.IsExactIn || v.Simulation == nil || v.Simulation.Error != "" || math.IsNaN(v.PriceImpact) || v.PriceImpact > 0.15 || v.PriceImpact < 0 || math.IsNaN(v.NetUsdValue) {
					continue
				}
				if simulated, se := positiveRaw(v.Simulation.AmountOutRaw); se != nil || simulated.Sign() <= 0 {
					continue
				}
				input, ie := positiveRaw(v.InAmountRaw)
				min, me := positiveRaw(v.MinAmountOutRaw)
				if ie != nil || me != nil || input.String() != q.InAmount || !safeInfo(v.ExecutionInfo) || !strings.EqualFold(routeTarget(v.ExecutionInfo), okuRouter) {
					continue
				}
				if v.InToken == nil || v.OutToken == nil || q.InToken != "native" && !strings.EqualFold(v.InToken.Address, q.InToken) || q.OutToken != "native" && !strings.EqualFold(v.OutToken.Address, q.OutToken) {
					continue
				}
				if len(v.SigningRequest.GetTypedData()) > 0 || len(v.Approvals) > 0 {
					continue
				}
				if v.NetUsdValue > best {
					best = v.NetUsdValue
					q.QuoteID = entry.QuoteId
					q.Route = entry.Router
					q.Target = routeTarget(v.ExecutionInfo)
					q.MinOut = min.String()
					q.OutAmount = v.OutAmountRaw
					q.Spender = quoteSpender(v.ApprovalsV2, q.InToken, q.InAmount)
				}
			}
			if q.QuoteID != "" {
				break
			}
			if attempt < 4 {
				select {
				case <-ctx.Done():
					return q, nil, ctx.Err()
				case <-time.After(350 * time.Millisecond):
				}
			}
		}
	} else {
		var created v2.BridgeCreateResponse
		req := &v2.BridgeCreateRequest{Integration: "", Params: &v2.BridgeQuoteParams{InChain: int64(q.Chain), OutChain: int64(q.OutChain), InChainCaip2: fmt.Sprintf("eip155:%d", q.Chain), OutChainCaip2: fmt.Sprintf("eip155:%d", q.OutChain), Account: w.agent, DstAddress: q.Recipient, InTokenAddress: q.InToken, OutTokenAddress: q.OutToken, InTokenAmount: q.InAmount, SlippagePpm: ppm}}
		if err = c.Bridge(ctx, "Create", req, &created, prepare); err != nil {
			return q, nil, apiError(err, "bridge Create")
		}
		q.OrderID = created.OrderId
		for attempt := 0; attempt < 5; attempt++ {
			var quotes v2.BridgeGetQuotesResponse
			err = c.Bridge(ctx, "GetQuotes", &v2.BridgeGetQuotesRequest{OrderId: q.OrderID}, &quotes, prepare)
			if err != nil {
				return q, nil, apiError(err, "bridge GetQuotes")
			}
			keys := make([]string, 0, len(quotes.Quotes))
			for key := range quotes.Quotes {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			best := math.Inf(-1)
			for _, key := range keys {
				entry := quotes.Quotes[key]
				if entry == nil || !entry.Fetched || entry.Error != nil || entry.Quote == nil {
					continue
				}
				v := entry.Quote
				if v.IntentBased || v.InChainId != int32(q.Chain) || v.OutChainId != int32(q.OutChain) || v.Simulation == nil || !v.Simulation.Success || !safeInfo(v.ExecutionInfo) || !onchain.ValidAddress(routeTarget(v.ExecutionInfo)) || len(v.SigningRequest.GetTypedData()) > 0 {
					continue
				}
				if v.InToken == nil || v.OutToken == nil || !strings.EqualFold(v.InToken.Address, q.InToken) || !strings.EqualFold(v.OutToken.Address, q.OutToken) {
					continue
				}
				input, ie := positiveRaw(v.InAmount)
				min, me := positiveRaw(v.MinAmountOut)
				if ie != nil || me != nil || input.String() != q.InAmount {
					continue
				}
				net := v.OutUsdValue - v.FeesUsdValue - v.GasUsdValue
				if math.IsNaN(net) || math.IsInf(net, 0) || v.InUsdValue > 0 && net < v.InUsdValue*0.85 {
					continue
				}
				if net > best {
					best = net
					q.QuoteID = entry.QuoteId
					q.Route = entry.Bridge
					q.Target = routeTarget(v.ExecutionInfo)
					q.MinOut = min.String()
					q.OutAmount = v.OutAmount
					q.Spender = quoteSpender(v.Approvals, q.InToken, q.InAmount)
				}
			}
			if q.QuoteID != "" {
				break
			}
			if attempt < 4 {
				select {
				case <-ctx.Done():
					return q, nil, ctx.Err()
				case <-time.After(350 * time.Millisecond):
				}
			}
		}
	}
	if q.OrderID == "" || q.QuoteID == "" {
		return q, nil, output.Errorf(output.CodeRejected, "no safe transaction route with verified simulation and explicit target")
	}
	q.ExpiresAt = a.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano)
	return q, w, nil
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
	if !strings.EqualFold(q.Target, okuRouter) || q.InToken != "native" && q.Spender != "" && !strings.EqualFold(q.Spender, okuRouter) {
		return nil, output.Errorf(output.CodeRejected, "swap route or approval spender is not OkuRouter")
	}
	if q.InToken != "native" {
		q.Spender = okuRouter
	}
	id := str(e, "intent-id")
	if id == "" {
		id = randomID()
	}
	placeholders := []journal.OnchainTemplate{{ChainID: q.Chain, To: q.Target, Value: "0x0"}}
	if q.InToken != "native" {
		placeholders = append([]journal.OnchainTemplate{{ChainID: q.Chain, To: q.InToken, Value: "0x0", Token: q.InToken, Amount: onchain.Hex(mustRaw(q.InAmount))}}, placeholders...)
	}
	in := journal.OnchainIntent{ID: id, Kind: kind, ChainID: q.Chain, OutChain: q.OutChain, Sender: w.agent, Recipient: q.Recipient, Token: q.InToken, OutToken: q.OutToken, Amount: q.InAmount, OrderID: q.OrderID, QuoteID: q.QuoteID, Route: q.Route, Target: q.Target, Spender: q.Spender, MinOut: q.MinOut, ExpiresAt: q.ExpiresAt, CreatedAt: a.Now().UTC().Format(time.RFC3339Nano), Templates: placeholders}
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
