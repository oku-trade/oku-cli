package api

import (
	"context"
	"net/http"

	accountv1 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1"
	accountv1connect "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1/accountv1connect"
	accountv2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2/accountv2connect"
	"google.golang.org/protobuf/proto"
)

type fundsClients struct {
	history     accountv1connect.OrderHistoryServiceClient
	swap        accountv2connect.SimpleSwapServiceV2Client
	bridge      accountv2connect.BridgeServiceClient
	onchain     accountv2connect.OnchainServiceClient
	token       accountv2connect.TokenServiceClient
	portfolio   accountv2connect.PortfolioServiceClient
	lending     accountv2connect.LendingServiceClient
	competition accountv2connect.CompetitionServiceClient
	merkl       accountv2connect.MerklServiceClient
}

func (c *Client) History(ctx context.Context, req *accountv1.OrderHistoryRequest, resp *accountv1.OrderHistoryResponse) error {
	return c.call(ctx, fundsRPC[accountv1.OrderHistoryRequest, accountv1.OrderHistoryResponse](func(f fundsClients) func(context.Context, *accountv1.OrderHistoryRequest) (*accountv1.OrderHistoryResponse, error) {
		return f.history.History
	}), req, resp, true, false)
}

func fundsRPC[Req, Resp any, PReq interface {
	*Req
	proto.Message
}](pick func(fundsClients) func(context.Context, *Req) (*Resp, error)) handler {
	return func(ctx context.Context, _ accountv2connect.PerpsServiceClient, _ accountv1connect.AuthServiceClient, f fundsClients, in []byte, h http.Header) (proto.Message, error) {
		return unary[Req, Resp, PReq](pick(f), ctx, in, h)
	}
}

// Swap calls a strictly allowlisted SimpleSwapServiceV2 method through the
// same generated Connect transport as the perps API.
func (c *Client) Swap(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := swapMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "SimpleSwapServiceV2/" + method + " is not supported"}
	}
	return c.call(ctx, h, req, resp, authed, false)
}
func (c *Client) Bridge(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := bridgeMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "BridgeService/" + method + " is not supported"}
	}
	return c.call(ctx, h, req, resp, authed, false)
}
func (c *Client) Onchain(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := onchainMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "OnchainService/" + method + " is not supported"}
	}
	return c.call(ctx, h, req, resp, authed, false)
}

func (c *Client) TokenRPC(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := tokenMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "TokenService/" + method + " is not supported"}
	}
	return c.call(ctx, h, req, resp, authed, false)
}

var tokenMethods = map[string]handler{
	"SearchTokens": fundsRPC[accountv2.SearchTokensRequest, accountv2.SearchTokensResponse](func(f fundsClients) func(context.Context, *accountv2.SearchTokensRequest) (*accountv2.SearchTokensResponse, error) {
		return f.token.SearchTokens
	}),
	"GetTokenPrice": fundsRPC[accountv2.GetTokenPriceRequest, accountv2.GetTokenPriceResponse](func(f fundsClients) func(context.Context, *accountv2.GetTokenPriceRequest) (*accountv2.GetTokenPriceResponse, error) {
		return f.token.GetTokenPrice
	}),
}

var swapMethods = map[string]handler{
	"GetSupportedMarkets": fundsRPC[accountv2.GetSupportedMarketsRequest, accountv2.GetSupportedMarketsResponse](func(f fundsClients) func(context.Context, *accountv2.GetSupportedMarketsRequest) (*accountv2.GetSupportedMarketsResponse, error) {
		return f.swap.GetSupportedMarkets
	}),
	"Ping": fundsRPC[accountv2.PingRequest, accountv2.PingResponse](func(f fundsClients) func(context.Context, *accountv2.PingRequest) (*accountv2.PingResponse, error) {
		return f.swap.Ping
	}),
	"Cancel": fundsRPC[accountv2.CancelRequest, accountv2.CancelResponse](func(f fundsClients) func(context.Context, *accountv2.CancelRequest) (*accountv2.CancelResponse, error) {
		return f.swap.Cancel
	}),
	"Create": fundsRPC[accountv2.SwapCreateRequest, accountv2.SwapCreateResponse](func(f fundsClients) func(context.Context, *accountv2.SwapCreateRequest) (*accountv2.SwapCreateResponse, error) {
		return f.swap.Create
	}),
	"GetQuotes": fundsRPC[accountv2.SwapGetQuotesRequest, accountv2.SwapGetQuotesResponse](func(f fundsClients) func(context.Context, *accountv2.SwapGetQuotesRequest) (*accountv2.SwapGetQuotesResponse, error) {
		return f.swap.GetQuotes
	}),
	"GetFlowstreamQuotes": fundsRPC[accountv2.SwapGetFlowstreamQuotesRequest, accountv2.SwapGetFlowstreamQuotesResponse](func(f fundsClients) func(context.Context, *accountv2.SwapGetFlowstreamQuotesRequest) (*accountv2.SwapGetFlowstreamQuotesResponse, error) {
		return f.swap.GetFlowstreamQuotes
	}),
	"GetQuote": fundsRPC[accountv2.SwapGetQuoteRequest, accountv2.SwapGetQuoteResponse](func(f fundsClients) func(context.Context, *accountv2.SwapGetQuoteRequest) (*accountv2.SwapGetQuoteResponse, error) {
		return f.swap.GetQuote
	}),
	"UpdateQuoteSelect": fundsRPC[accountv2.SwapUpdateQuoteSelectRequest, accountv2.SwapUpdateQuoteSelectResponse](func(f fundsClients) func(context.Context, *accountv2.SwapUpdateQuoteSelectRequest) (*accountv2.SwapUpdateQuoteSelectResponse, error) {
		return f.swap.UpdateQuoteSelect
	}),
	"ConfirmQuoteSelect": fundsRPC[accountv2.SwapConfirmQuoteSelectRequest, accountv2.SwapConfirmQuoteSelectResponse](func(f fundsClients) func(context.Context, *accountv2.SwapConfirmQuoteSelectRequest) (*accountv2.SwapConfirmQuoteSelectResponse, error) {
		return f.swap.ConfirmQuoteSelect
	}),
	"GetExecutionInfo": fundsRPC[accountv2.GetExecutionInfoRequest, accountv2.GetExecutionInfoResponse](func(f fundsClients) func(context.Context, *accountv2.GetExecutionInfoRequest) (*accountv2.GetExecutionInfoResponse, error) {
		return f.swap.GetExecutionInfo
	}),
	"Finish": fundsRPC[accountv2.FinishRequest, accountv2.FinishResponse](func(f fundsClients) func(context.Context, *accountv2.FinishRequest) (*accountv2.FinishResponse, error) {
		return f.swap.Finish
	}),
}
var bridgeMethods = map[string]handler{
	"FinishClaim": fundsRPC[accountv2.FinishClaimRequest, accountv2.FinishClaimResponse](func(f fundsClients) func(context.Context, *accountv2.FinishClaimRequest) (*accountv2.FinishClaimResponse, error) {
		return f.bridge.FinishClaim
	}),
	"GetSupportedBridges": fundsRPC[accountv2.GetSupportedBridgesRequest, accountv2.GetSupportedBridgesResponse](func(f fundsClients) func(context.Context, *accountv2.GetSupportedBridgesRequest) (*accountv2.GetSupportedBridgesResponse, error) {
		return f.bridge.GetSupportedBridges
	}),
	"Ping": fundsRPC[accountv2.PingRequest, accountv2.PingResponse](func(f fundsClients) func(context.Context, *accountv2.PingRequest) (*accountv2.PingResponse, error) {
		return f.bridge.Ping
	}),
	"Cancel": fundsRPC[accountv2.CancelRequest, accountv2.CancelResponse](func(f fundsClients) func(context.Context, *accountv2.CancelRequest) (*accountv2.CancelResponse, error) {
		return f.bridge.Cancel
	}),
	"Create": fundsRPC[accountv2.BridgeCreateRequest, accountv2.BridgeCreateResponse](func(f fundsClients) func(context.Context, *accountv2.BridgeCreateRequest) (*accountv2.BridgeCreateResponse, error) {
		return f.bridge.Create
	}),
	"UpdateQuoteParams": fundsRPC[accountv2.BridgeUpdateQuoteParamsRequest, accountv2.BridgeUpdateQuoteParamsResponse](func(f fundsClients) func(context.Context, *accountv2.BridgeUpdateQuoteParamsRequest) (*accountv2.BridgeUpdateQuoteParamsResponse, error) {
		return f.bridge.UpdateQuoteParams
	}),
	"GetNewQuotes": fundsRPC[accountv2.BridgeGetNewQuotesRequest, accountv2.BridgeGetQuotesResponse](func(f fundsClients) func(context.Context, *accountv2.BridgeGetNewQuotesRequest) (*accountv2.BridgeGetQuotesResponse, error) {
		return f.bridge.GetNewQuotes
	}),
	"GetQuotes": fundsRPC[accountv2.BridgeGetQuotesRequest, accountv2.BridgeGetQuotesResponse](func(f fundsClients) func(context.Context, *accountv2.BridgeGetQuotesRequest) (*accountv2.BridgeGetQuotesResponse, error) {
		return f.bridge.GetQuotes
	}),
	"GetQuote": fundsRPC[accountv2.BridgeGetQuoteRequest, accountv2.BridgeGetQuoteResponse](func(f fundsClients) func(context.Context, *accountv2.BridgeGetQuoteRequest) (*accountv2.BridgeGetQuoteResponse, error) {
		return f.bridge.GetQuote
	}),
	"UpdateQuoteSelect": fundsRPC[accountv2.BridgeUpdateQuoteSelectRequest, accountv2.BridgeUpdateQuoteSelectResponse](func(f fundsClients) func(context.Context, *accountv2.BridgeUpdateQuoteSelectRequest) (*accountv2.BridgeUpdateQuoteSelectResponse, error) {
		return f.bridge.UpdateQuoteSelect
	}),
	"ConfirmQuoteSelect": fundsRPC[accountv2.BridgeConfirmQuoteSelectRequest, accountv2.BridgeConfirmQuoteSelectResponse](func(f fundsClients) func(context.Context, *accountv2.BridgeConfirmQuoteSelectRequest) (*accountv2.BridgeConfirmQuoteSelectResponse, error) {
		return f.bridge.ConfirmQuoteSelect
	}),
	"GetExecutionInfo": fundsRPC[accountv2.GetExecutionInfoRequest, accountv2.GetExecutionInfoResponse](func(f fundsClients) func(context.Context, *accountv2.GetExecutionInfoRequest) (*accountv2.GetExecutionInfoResponse, error) {
		return f.bridge.GetExecutionInfo
	}),
	"Finish": fundsRPC[accountv2.FinishRequest, accountv2.FinishResponse](func(f fundsClients) func(context.Context, *accountv2.FinishRequest) (*accountv2.FinishResponse, error) {
		return f.bridge.Finish
	}),
}
var onchainMethods = map[string]handler{
	"GetOnchainTokenBalances": fundsRPC[accountv2.GetOnchainTokenBalancesRequest, accountv2.GetOnchainTokenBalancesResponse](func(f fundsClients) func(context.Context, *accountv2.GetOnchainTokenBalancesRequest) (*accountv2.GetOnchainTokenBalancesResponse, error) {
		return f.onchain.GetOnchainTokenBalances
	}),
	"GetNativeBalance": fundsRPC[accountv2.GetNativeBalanceRequest, accountv2.GetNativeBalanceResponse](func(f fundsClients) func(context.Context, *accountv2.GetNativeBalanceRequest) (*accountv2.GetNativeBalanceResponse, error) {
		return f.onchain.GetNativeBalance
	}),
	"GetGasPrice": fundsRPC[accountv2.GetGasPriceRequest, accountv2.GetGasPriceResponse](func(f fundsClients) func(context.Context, *accountv2.GetGasPriceRequest) (*accountv2.GetGasPriceResponse, error) {
		return f.onchain.GetGasPrice
	}),
	"GetTransactionReceipt": fundsRPC[accountv2.GetTransactionReceiptRequest, accountv2.GetTransactionReceiptResponse](func(f fundsClients) func(context.Context, *accountv2.GetTransactionReceiptRequest) (*accountv2.GetTransactionReceiptResponse, error) {
		return f.onchain.GetTransactionReceipt
	}),
}
