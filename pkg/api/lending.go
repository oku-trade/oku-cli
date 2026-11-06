package api

import (
	"context"

	"connectrpc.com/connect"
	accountv2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
)

// Lending calls an allowlisted LendingService method: Euler vault, market and
// position reads, Euler calldata builders, and the leveraged-loop RPCs.
func (c *Client) Lending(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := lendingMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "LendingService/" + method + " is not supported"}
	}
	return c.call(ctx, h, req, resp, authed, false)
}

var lendingMethods = map[string]handler{
	"GetLendingVaults": fundsRPC[accountv2.GetLendingVaultsRequest, accountv2.GetLendingVaultsResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.GetLendingVaultsRequest]) (*connect.Response[accountv2.GetLendingVaultsResponse], error) {
		return f.lending.GetLendingVaults
	}),
	"GetLendingMarkets": fundsRPC[accountv2.GetLendingMarketsRequest, accountv2.GetLendingMarketsResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.GetLendingMarketsRequest]) (*connect.Response[accountv2.GetLendingMarketsResponse], error) {
		return f.lending.GetLendingMarkets
	}),
	"GetLendingPosition": fundsRPC[accountv2.GetLendingPositionRequest, accountv2.GetLendingPositionResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.GetLendingPositionRequest]) (*connect.Response[accountv2.GetLendingPositionResponse], error) {
		return f.lending.GetLendingPosition
	}),
	"GetLendingUserPositions": fundsRPC[accountv2.GetLendingUserPositionsRequest, accountv2.GetLendingUserPositionsResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.GetLendingUserPositionsRequest]) (*connect.Response[accountv2.GetLendingUserPositionsResponse], error) {
		return f.lending.GetLendingUserPositions
	}),
	"BuildLendingDeposit": fundsRPC[accountv2.BuildLendingDepositRequest, accountv2.BuildLendingDepositResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.BuildLendingDepositRequest]) (*connect.Response[accountv2.BuildLendingDepositResponse], error) {
		return f.lending.BuildLendingDeposit
	}),
	"BuildLendingWithdraw": fundsRPC[accountv2.BuildLendingWithdrawRequest, accountv2.BuildLendingWithdrawResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.BuildLendingWithdrawRequest]) (*connect.Response[accountv2.BuildLendingWithdrawResponse], error) {
		return f.lending.BuildLendingWithdraw
	}),
	"BuildLendingBorrow": fundsRPC[accountv2.BuildLendingBorrowRequest, accountv2.BuildLendingBorrowResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.BuildLendingBorrowRequest]) (*connect.Response[accountv2.BuildLendingBorrowResponse], error) {
		return f.lending.BuildLendingBorrow
	}),
	"BuildLendingRepay": fundsRPC[accountv2.BuildLendingRepayRequest, accountv2.BuildLendingRepayResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.BuildLendingRepayRequest]) (*connect.Response[accountv2.BuildLendingRepayResponse], error) {
		return f.lending.BuildLendingRepay
	}),
	"QuoteLoop": fundsRPC[accountv2.QuoteLoopRequest, accountv2.LoopStatus](func(f fundsClients) func(context.Context, *connect.Request[accountv2.QuoteLoopRequest]) (*connect.Response[accountv2.LoopStatus], error) {
		return f.lending.QuoteLoop
	}),
	"ConfirmLoop": fundsRPC[accountv2.LoopOrderRequest, accountv2.ConfirmLoopResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.LoopOrderRequest]) (*connect.Response[accountv2.ConfirmLoopResponse], error) {
		return f.lending.ConfirmLoop
	}),
	"SubmitLoop": fundsRPC[accountv2.SubmitLoopRequest, accountv2.SubmitLoopResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.SubmitLoopRequest]) (*connect.Response[accountv2.SubmitLoopResponse], error) {
		return f.lending.SubmitLoop
	}),
	"GetLoop": fundsRPC[accountv2.LoopOrderRequest, accountv2.LoopStatus](func(f fundsClients) func(context.Context, *connect.Request[accountv2.LoopOrderRequest]) (*connect.Response[accountv2.LoopStatus], error) {
		return f.lending.GetLoop
	}),
}
