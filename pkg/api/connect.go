package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	accountv1 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1"
	"github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1/accountv1connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	accountv2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2/accountv2connect"
)

// handler invokes one generated Connect client method. in is the request
// encoded as JSON; it is decoded strictly (unknown fields rejected) into the
// generated request message so handwritten request shapes cannot drift from
// the vendored proto silently.
type handler func(ctx context.Context, perps accountv2connect.PerpsServiceClient, dev accountv2connect.CliDeviceServiceClient, auth accountv1connect.AuthServiceClient, in []byte, h http.Header) (proto.Message, error)

var strictJSON = protojson.UnmarshalOptions{DiscardUnknown: false}

func unary[Req, Resp any, PReq interface {
	*Req
	proto.Message
}](fn func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error), ctx context.Context, in []byte, h http.Header) (proto.Message, error) {
	var m Req
	if err := strictJSON.Unmarshal(in, PReq(&m)); err != nil {
		return nil, &requestEncodeError{err}
	}
	r := connect.NewRequest(&m)
	for k, v := range h {
		r.Header()[k] = v
	}
	resp, err := fn(ctx, r)
	if err != nil {
		return nil, err
	}
	msg, ok := any(resp.Msg).(proto.Message)
	if !ok {
		return nil, errors.New("generated response is not a proto message")
	}
	return msg, nil
}

type requestEncodeError struct{ err error }

func (e *requestEncodeError) Error() string {
	return "request does not match API schema: " + e.err.Error()
}
func (e *requestEncodeError) Unwrap() error { return e.err }

func perps[Req, Resp any, PReq interface {
	*Req
	proto.Message
}](pick func(accountv2connect.PerpsServiceClient) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error)) handler {
	return func(ctx context.Context, p accountv2connect.PerpsServiceClient, _ accountv2connect.CliDeviceServiceClient, _ accountv1connect.AuthServiceClient, in []byte, h http.Header) (proto.Message, error) {
		return unary[Req, Resp, PReq](pick(p), ctx, in, h)
	}
}

func device[Req, Resp any, PReq interface {
	*Req
	proto.Message
}](pick func(accountv2connect.CliDeviceServiceClient) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error)) handler {
	return func(ctx context.Context, _ accountv2connect.PerpsServiceClient, d accountv2connect.CliDeviceServiceClient, _ accountv1connect.AuthServiceClient, in []byte, h http.Header) (proto.Message, error) {
		return unary[Req, Resp, PReq](pick(d), ctx, in, h)
	}
}

type P = accountv2connect.PerpsServiceClient
type D = accountv2connect.CliDeviceServiceClient

func authRPC[Req, Resp any, PReq interface {
	*Req
	proto.Message
}](pick func(accountv1connect.AuthServiceClient) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error)) handler {
	return func(ctx context.Context, _ accountv2connect.PerpsServiceClient, _ accountv2connect.CliDeviceServiceClient, a accountv1connect.AuthServiceClient, in []byte, h http.Header) (proto.Message, error) {
		return unary[Req, Resp, PReq](pick(a), ctx, in, h)
	}
}

var authMethods = map[string]handler{
	"PrivySessionExchange": authRPC[accountv1.PrivySessionExchangeRequest, accountv1.PrivySessionExchangeResponse](func(a accountv1connect.AuthServiceClient) func(context.Context, *connect.Request[accountv1.PrivySessionExchangeRequest]) (*connect.Response[accountv1.PrivySessionExchangeResponse], error) {
		return a.PrivySessionExchange
	}),
	"ExtendSession": authRPC[accountv1.ExtendSessionRequest, accountv1.ExtendSessionResponse](func(a accountv1connect.AuthServiceClient) func(context.Context, *connect.Request[accountv1.ExtendSessionRequest]) (*connect.Response[accountv1.ExtendSessionResponse], error) {
		return a.ExtendSession
	}),
	"Logout": authRPC[accountv1.LogoutRequest, accountv1.LogoutResponse](func(a accountv1connect.AuthServiceClient) func(context.Context, *connect.Request[accountv1.LogoutRequest]) (*connect.Response[accountv1.LogoutResponse], error) {
		return a.Logout
	}),
}

// Methods the CLI is allowed to call. Anything else is refused locally.
var perpsMethods = map[string]handler{
	"ValidateAgentKey": perps[accountv2.ValidateAgentKeyRequest, accountv2.ValidateAgentKeyResponse](func(c P) func(context.Context, *connect.Request[accountv2.ValidateAgentKeyRequest]) (*connect.Response[accountv2.ValidateAgentKeyResponse], error) {
		return c.ValidateAgentKey
	}),
	"GetMarkets": perps[accountv2.GetMarketsRequest, accountv2.GetMarketsResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetMarketsRequest]) (*connect.Response[accountv2.GetMarketsResponse], error) {
		return c.GetMarkets
	}),
	"GetMarketPrice": perps[accountv2.GetMarketPriceRequest, accountv2.GetMarketPriceResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetMarketPriceRequest]) (*connect.Response[accountv2.GetMarketPriceResponse], error) {
		return c.GetMarketPrice
	}),
	"GetMarketFundingHistory": perps[accountv2.GetMarketFundingHistoryRequest, accountv2.GetMarketFundingHistoryResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetMarketFundingHistoryRequest]) (*connect.Response[accountv2.GetMarketFundingHistoryResponse], error) {
		return c.GetMarketFundingHistory
	}),
	"GetPerpPositions": perps[accountv2.GetPerpPositionsRequest, accountv2.GetPerpPositionsResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetPerpPositionsRequest]) (*connect.Response[accountv2.GetPerpPositionsResponse], error) {
		return c.GetPerpPositions
	}),
	"GetOpenOrders": perps[accountv2.GetOpenOrdersRequest, accountv2.GetOpenOrdersResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetOpenOrdersRequest]) (*connect.Response[accountv2.GetOpenOrdersResponse], error) {
		return c.GetOpenOrders
	}),
	"GetAccountBalances": perps[accountv2.GetAccountBalancesRequest, accountv2.GetAccountBalancesResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetAccountBalancesRequest]) (*connect.Response[accountv2.GetAccountBalancesResponse], error) {
		return c.GetAccountBalances
	}),
	"GetBuilderFeeStatus": perps[accountv2.GetBuilderFeeStatusRequest, accountv2.GetBuilderFeeStatusResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetBuilderFeeStatusRequest]) (*connect.Response[accountv2.GetBuilderFeeStatusResponse], error) {
		return c.GetBuilderFeeStatus
	}),
	"GetUserFills": perps[accountv2.GetUserFillsRequest, accountv2.GetUserFillsResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetUserFillsRequest]) (*connect.Response[accountv2.GetUserFillsResponse], error) {
		return c.GetUserFills
	}),
	"CreateAction": perps[accountv2.CreateActionRequest, accountv2.ActionPayloadResponse](func(c P) func(context.Context, *connect.Request[accountv2.CreateActionRequest]) (*connect.Response[accountv2.ActionPayloadResponse], error) {
		return c.CreateAction
	}),
	"ConfirmAction": perps[accountv2.ConfirmActionRequest, accountv2.ConfirmActionResponse](func(c P) func(context.Context, *connect.Request[accountv2.ConfirmActionRequest]) (*connect.Response[accountv2.ConfirmActionResponse], error) {
		return c.ConfirmAction
	}),
	"CancelAction": perps[accountv2.CancelActionRequest, accountv2.CancelActionResponse](func(c P) func(context.Context, *connect.Request[accountv2.CancelActionRequest]) (*connect.Response[accountv2.CancelActionResponse], error) {
		return c.CancelAction
	}),
	"GetAction": perps[accountv2.GetActionRequest, accountv2.GetActionResponse](func(c P) func(context.Context, *connect.Request[accountv2.GetActionRequest]) (*connect.Response[accountv2.GetActionResponse], error) {
		return c.GetAction
	}),
	"ReconcileActions": perps[accountv2.ReconcileActionsRequest, accountv2.ReconcileActionsResponse](func(c P) func(context.Context, *connect.Request[accountv2.ReconcileActionsRequest]) (*connect.Response[accountv2.ReconcileActionsResponse], error) {
		return c.ReconcileActions
	}),
}

var deviceMethods = map[string]handler{
	"RedeemEnrollment": device[accountv2.RedeemCliEnrollmentRequest, accountv2.RedeemCliEnrollmentResponse](func(c D) func(context.Context, *connect.Request[accountv2.RedeemCliEnrollmentRequest]) (*connect.Response[accountv2.RedeemCliEnrollmentResponse], error) {
		return c.RedeemEnrollment
	}),
	"PollEnrollment": device[accountv2.PollCliEnrollmentRequest, accountv2.PollCliEnrollmentResponse](func(c D) func(context.Context, *connect.Request[accountv2.PollCliEnrollmentRequest]) (*connect.Response[accountv2.PollCliEnrollmentResponse], error) {
		return c.PollEnrollment
	}),
	"GetCurrentDevice": device[accountv2.GetCurrentCliDeviceRequest, accountv2.GetCurrentCliDeviceResponse](func(c D) func(context.Context, *connect.Request[accountv2.GetCurrentCliDeviceRequest]) (*connect.Response[accountv2.GetCurrentCliDeviceResponse], error) {
		return c.GetCurrentDevice
	}),
	"RevokeCurrentDevice": device[accountv2.RevokeCurrentCliDeviceRequest, accountv2.RevokeCurrentCliDeviceResponse](func(c D) func(context.Context, *connect.Request[accountv2.RevokeCurrentCliDeviceRequest]) (*connect.Response[accountv2.RevokeCurrentCliDeviceResponse], error) {
		return c.RevokeCurrentDevice
	}),
}

// callState records what actually happened on the wire during one call so
// errors can be classified by evidence (was anything sent? what status?)
// rather than by the Connect library's generic mapping.
type callState struct {
	doCalled     bool
	transportErr error
	status       int
	readErr      error
	oversized    bool
	errBody      []byte
}

var errRedirect = errors.New("redirect refused")

// observingDoer wraps the no-redirect HTTP client. It enforces the response
// size limit, refuses 3xx, and records wire evidence. It never retries.
type observingDoer struct {
	hc *http.Client
	st *callState
}

func (d observingDoer) Do(req *http.Request) (*http.Response, error) {
	d.st.doCalled = true
	resp, err := d.hc.Do(req)
	if err != nil {
		d.st.transportErr = err
		return nil, err
	}
	d.st.status = resp.StatusCode
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	resp.Body.Close()
	if err != nil {
		d.st.readErr = err
		return nil, err
	}
	if len(raw) > maxBody {
		d.st.oversized = true
		return nil, errors.New("response exceeds size limit")
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, errRedirect
	}
	if resp.StatusCode != http.StatusOK {
		d.st.errBody = raw
	} else if ct := resp.Header.Get("Content-Type"); ct == "" || strings.HasPrefix(ct, "text/plain") {
		// The pre-Connect client never inspected Content-Type, and some
		// proxies/fixtures omit it (Go then sniffs text/plain). The body is
		// still decoded strictly as protojson, so this does not accept any
		// payload the schema would reject.
		resp.Header.Set("Content-Type", "application/json")
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	return resp, nil
}
