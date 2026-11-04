package cli

import (
	"context"
	"math"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oku-trade/oku-cli/pkg/api"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/lending"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// The payer is the router's msg.sender inside the bundle: Bundler3 for Morpho
// (it calls the router, GeneralAdapter1 only receives) and the Swapper for
// Euler. The backend quotes the same way (account repo,
// src/temporal/workflows/orders/lending_loop/morpho.go routerQuote: account
// Bundler3, DstAddress GeneralAdapter1), and the quote's warrant is only
// signed for the caller's account, otherwise the router reverts with
// WARRANT_REQUIRED_FOR_RECIPIENT.
//
// loopSwap quotes one exact-in swap leg of a loop bundle through the same
// swap service and route selection as `oku swap`, with the bundle hop
// as recipient and the contract that calls the router as payer, then
// fetches and decodes the router calldata. The fill must sell exactly
// `amount` of `sell` for `buy` to `hop`; MinOut is the quote's own bound.
type loopSwapQuote struct {
	lending.Swap
	Quote routeQuote
}

func (a *App) loopSwap(ctx context.Context, e *env, w *resolvedWallet, chain uint64, sell, buy, payer, hop common.Address, amount *big.Int, decimals int) (loopSwapQuote, error) {
	router, err := lending.OkuRouter(chain)
	if err != nil {
		return loopSwapQuote{}, output.Wrap(output.CodeRejected, err, "loop swap")
	}
	c := e.client(w.profile)
	s, err := e.unlock(w.profile, "")
	if err != nil {
		return loopSwapQuote{}, err
	}
	s.apply(c)
	q := routeQuote{Chain: chain, OutChain: chain, InToken: sell.Hex(), OutToken: buy.Hex(), InAmount: amount.String(), Recipient: hop.Hex(), amountText: formatUnits(amount, decimals), payer: payer.Hex()}
	q, rej, err := a.fetchRouteAttempt(ctx, e, "swap", true, w, c, q)
	if err != nil {
		return loopSwapQuote{}, err
	}
	if rej != nil {
		return loopSwapQuote{}, rej.error(str(e, "route"))
	}
	if q.Intent || q.QuoteID == "" {
		return loopSwapQuote{}, output.Errorf(output.CodeRejected, "no router swap route for the loop")
	}
	if q.HighImpact && !e.cmd.Bool("allow-high-impact") {
		return loopSwapQuote{}, output.Errorf(output.CodeRejected, "loop swap loses %d bps to the market (limit %d); pass --allow-high-impact to accept", q.LossBps, int64(math.Round(maxLossFraction(e)*10_000)))
	}
	trade, err := a.loopTrade(ctx, c, q)
	if err != nil {
		return loopSwapQuote{}, err
	}
	if trade.Sell != sell || trade.Buy != buy || trade.Recipient != hop || trade.Amount.Cmp(amount) != 0 {
		return loopSwapQuote{}, output.Errorf(output.CodePayloadMismatch, "router fill does not sell the quoted amount to the bundle")
	}
	min, err := positiveRaw(q.MinOut)
	if err != nil {
		return loopSwapQuote{}, output.Wrap(output.CodePayloadMismatch, err, "swap minimum")
	}
	return loopSwapQuote{Swap: lending.Swap{Router: router, Trade: trade, MinOut: min}, Quote: q}, nil
}

// loopTrade selects the quote and fetches its router calldata, the same
// calls `swap submit` makes, without signing or sending anything.
func (a *App) loopTrade(ctx context.Context, c *api.Client, q routeQuote) (lending.RouterTrade, error) {
	var selected v2.SwapUpdateQuoteSelectResponse
	if err := c.Swap(ctx, "UpdateQuoteSelect", &v2.SwapUpdateQuoteSelectRequest{OrderId: q.OrderID, Router: q.Route, QuoteId: q.QuoteID}, &selected, true); err != nil {
		return lending.RouterTrade{}, apiError(err, "UpdateQuoteSelect")
	}
	if selected.QuoteId != q.QuoteID || selected.Router != q.Route {
		return lending.RouterTrade{}, output.Errorf(output.CodePayloadMismatch, "selected swap quote changed")
	}
	var confirmed v2.SwapConfirmQuoteSelectResponse
	if err := c.Swap(ctx, "ConfirmQuoteSelect", &v2.SwapConfirmQuoteSelectRequest{OrderId: q.OrderID, Router: q.Route, QuoteId: q.QuoteID}, &confirmed, true); err != nil {
		return lending.RouterTrade{}, apiError(err, "ConfirmQuoteSelect")
	}
	var resp v2.GetExecutionInfoResponse
	if err := c.Swap(ctx, "GetExecutionInfo", &v2.GetExecutionInfoRequest{OrderId: q.OrderID, QuoteId: q.QuoteID, Request: &v2.ExecutionRequest{}}, &resp, true); err != nil {
		return lending.RouterTrade{}, apiError(err, "GetExecutionInfo")
	}
	info := resp.Info
	if !safeInfo(info) || !strings.EqualFold(info.Trade.To, routerFor(q.Chain)) || info.Trade.ChainId != int64(q.Chain) {
		return lending.RouterTrade{}, output.Errorf(output.CodePayloadMismatch, "swap execution is not a router call on chain %d", q.Chain)
	}
	if v, verr := backendValue(info.Trade.Value); verr != nil || v.Sign() != 0 {
		return lending.RouterTrade{}, output.Errorf(output.CodePayloadMismatch, "swap execution carries native value")
	}
	data, err := decodeHexData(info.Trade.Data)
	if err != nil {
		return lending.RouterTrade{}, output.Wrap(output.CodePayloadMismatch, err, "swap calldata")
	}
	t, err := lending.DecodeRouterTrade(data)
	if err != nil {
		return lending.RouterTrade{}, output.Wrap(output.CodePayloadMismatch, err, "swap calldata")
	}
	return t, nil
}
