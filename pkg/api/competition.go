package api

import (
	"context"

	"connectrpc.com/connect"
	accountv2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
)

// Competition calls an allowlisted CompetitionService method: the trading
// competition (scored by Voltrade) and its public registration flow. Every
// RPC is public, so callers pass authed=false.
func (c *Client) Competition(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := competitionMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "CompetitionService/" + method + " is not supported"}
	}
	return c.call(ctx, h, req, resp, authed, false)
}

var competitionMethods = map[string]handler{
	"GetCompetition": fundsRPC[accountv2.GetCompetitionRequest, accountv2.GetCompetitionResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.GetCompetitionRequest]) (*connect.Response[accountv2.GetCompetitionResponse], error) {
		return f.competition.GetCompetition
	}),
	"ListLeaderboard": fundsRPC[accountv2.ListLeaderboardRequest, accountv2.ListLeaderboardResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.ListLeaderboardRequest]) (*connect.Response[accountv2.ListLeaderboardResponse], error) {
		return f.competition.ListLeaderboard
	}),
	"GetParticipant": fundsRPC[accountv2.GetParticipantRequest, accountv2.GetParticipantResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.GetParticipantRequest]) (*connect.Response[accountv2.GetParticipantResponse], error) {
		return f.competition.GetParticipant
	}),
	"StartRegistration": fundsRPC[accountv2.StartRegistrationRequest, accountv2.StartRegistrationResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.StartRegistrationRequest]) (*connect.Response[accountv2.StartRegistrationResponse], error) {
		return f.competition.StartRegistration
	}),
	"CompleteRegistration": fundsRPC[accountv2.CompleteRegistrationRequest, accountv2.CompleteRegistrationResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.CompleteRegistrationRequest]) (*connect.Response[accountv2.CompleteRegistrationResponse], error) {
		return f.competition.CompleteRegistration
	}),
}

// Merkl calls an allowlisted MerklService method. Only GetUserRewards is
// vendored: it returns the proofs for Merkl reward claims.
func (c *Client) Merkl(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := merklMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "MerklService/" + method + " is not supported"}
	}
	return c.call(ctx, h, req, resp, authed, false)
}

var merklMethods = map[string]handler{
	"GetUserRewards": fundsRPC[accountv2.GetUserRewardsRequest, accountv2.GetUserRewardsResponse](func(f fundsClients) func(context.Context, *connect.Request[accountv2.GetUserRewardsRequest]) (*connect.Response[accountv2.GetUserRewardsResponse], error) {
		return f.merkl.GetUserRewards
	}),
}
