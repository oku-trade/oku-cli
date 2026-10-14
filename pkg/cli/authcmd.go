package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/privy"
	urfave "github.com/urfave/cli/v3"
)

func authLoginFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "account", Usage: "Hyperliquid master wallet address"}, &urfave.BoolFlag{Name: "no-open", Usage: "print verification URL without opening browser"}, &urfave.DurationFlag{Name: "timeout", Value: 10 * time.Minute, Usage: "maximum time to wait for authorization"}}
}
func authStatusFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.BoolFlag{Name: "verify", Usage: "refresh the credential and check Hyperliquid agent approval"}}
}

var boundaries = map[string]string{
	"mode":        "Privy delegated embedded-wallet signing",
	"grant":       "Anyone who can read the credential can sign anything permitted by its Privy grant until revoked; an access JWT can remain valid for 15 minutes after revocation.",
	"not_revoked": "Logging out does not remove Hyperliquid agent approval, open orders, or positions. Remove the agent separately on Hyperliquid or the Oku website.",
}

func validateAgent(ctx context.Context, c *api.Client, agent, account string) error {
	var resp struct {
		Valid  bool   `json:"valid"`
		Status string `json:"status"`
	}
	if err := c.Perps(ctx, "ValidateAgentKey", map[string]string{"agentAddress": agent, "userAddress": account}, &resp, true); err != nil {
		return apiError(err, "ValidateAgentKey")
	}
	if !resp.Valid || resp.Status != "approved" {
		return output.Errorf(output.CodeAuthRequired, "agent %s is not approved for %s (%s); finish the steps on the Oku page", agent, account, resp.Status)
	}
	return nil
}
func (a *App) cmdAuthLogin(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "auth login requires a profile")
	}
	if e.cmd.String("account") != "" {
		p.Account = e.cmd.String("account")
	} else if e.g.account != "" {
		p.Account = e.g.account
	}
	if !config.ValidAddress(p.Account) {
		return nil, output.Errorf(output.CodeAccountRequired, "auth login requires --account or a profile account")
	}
	if !e.app.StdinIsTTY {
		return nil, output.Errorf(output.CodeUsage, "auth login requires an interactive terminal; never run it through MCP or Service")
	}
	pc, err := e.privyClient(p)
	if err != nil {
		return nil, err
	}
	timeout := e.cmd.Duration("timeout")
	if timeout <= 0 {
		return nil, output.Errorf(output.CodeInvalidArgument, "timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(e.ctx, timeout)
	defer cancel()
	da, err := pc.StartDeviceAuthorization(ctx)
	if err != nil {
		return nil, output.Wrap(output.CodeAuthUnavailable, err, "starting Privy login")
	}
	url := da.VerificationURIComplete
	if url == "" {
		url = da.VerificationURI
	}
	e.diag("Open %s and enter code %s", url, da.UserCode)
	if !e.cmd.Bool("no-open") && e.app.OpenURL != nil {
		if openErr := e.app.OpenURL(url); openErr != nil {
			e.diag("Browser did not open: %v", openErr)
		}
	}
	// Browser opening is best effort and intentionally optional. The URL is
	// always printed, including in JSON mode, without leaking any access token.
	tok, err := pc.PollDeviceToken(ctx, da, e.app.Sleep)
	if err != nil {
		return nil, output.Wrap(output.CodeAuthRequired, err, "Privy authorization was not approved")
	}
	ps, err := pc.Authenticate(ctx, tok.AccessToken)
	if err != nil {
		return nil, output.Wrap(output.CodeAuthUnavailable, err, "authenticating embedded wallet")
	}
	var wallet *privy.Wallet
	for _, w := range ps.Wallets() {
		if strings.EqualFold(w.ChainType, "ethereum") {
			if wallet != nil {
				return nil, output.Errorf(output.CodeAuthRequired, "multiple embedded Ethereum wallets returned; select one on the Oku page")
			}
			copy := w
			wallet = &copy
		}
	}
	if wallet == nil || !config.ValidAddress(wallet.Address) || wallet.ID == "" {
		return nil, output.Errorf(output.CodeAuthRequired, "no embedded Ethereum agent wallet returned by Privy")
	}
	c := e.client(p)
	var exchange api.PrivySessionExchangeResponse
	if err = c.Auth(ctx, "PrivySessionExchange", map[string]string{"accessToken": tok.AccessToken}, &exchange, false); err != nil {
		return nil, apiError(err, "PrivySessionExchange")
	}
	if exchange.Session == nil || exchange.Session.SessionID == "" || exchange.Session.Token == "" {
		return nil, output.Errorf(output.CodeAuthUnavailable, "Oku returned an incomplete session")
	}
	c.SessionID, c.SessionToken = exchange.Session.SessionID, exchange.Session.Token
	if err = validateAgent(ctx, c, wallet.Address, p.Account); err != nil {
		return nil, err
	}
	pass, err := e.passphrase(true)
	if err != nil {
		return nil, err
	}
	release, err := auth.LockProfile(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "locking credential")
	}
	defer release()
	if selected := e.cmd.String("account"); selected != "" {
		cfg, loadErr := config.Load()
		if loadErr != nil {
			return nil, output.Wrap(output.CodeConfigError, loadErr, "loading profile")
		}
		profile := cfg.Profiles[p.Name]
		if profile == nil || config.NormalizeAPIURL(profile.APIURL) != p.APIURL {
			return nil, output.Errorf(output.CodeConfigError, "profile changed during login; retry")
		}
		if !strings.EqualFold(profile.Account, selected) {
			profile.Account = strings.ToLower(selected)
			if saveErr := cfg.Save(); saveErr != nil {
				return nil, output.Wrap(output.CodeConfigError, saveErr, "saving selected account")
			}
		}
	}
	md := auth.Metadata{Kind: auth.KindPrivy, Profile: p.Name, Network: p.Network, APIURL: p.APIURL, PrivyAppID: pc.AppID, AccountAddress: strings.ToLower(p.Account), AgentAddress: strings.ToLower(wallet.Address), PrivyWalletID: wallet.ID, AccessExpiresAt: tok.AccessExpiresAt.UTC().Format(time.RFC3339), SessionID: exchange.Session.SessionID, SessionExpiresAt: exchange.Session.ExpiresAt, CreatedAt: e.app.Now().UTC().Format(time.RFC3339), Status: "active", Generation: 1}
	sec := auth.Secrets{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, SessionToken: exchange.Session.Token}
	if err = auth.SavePrivy(p.Name, md, sec, pass); err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "saving credential")
	}
	return statusResult(&md, true), nil
}
func statusResult(md *auth.Metadata, verified bool) *result {
	return &result{data: map[string]any{"credential": md, "verified": verified, "boundaries": boundaries}, human: func(w io.Writer) {
		fmt.Fprintf(w, "profile  %s\naccount  %s\nagent    %s\nwallet   %s\naccess   %s\nsession  %s\n", md.Profile, md.AccountAddress, md.AgentAddress, md.PrivyWalletID, md.AccessExpiresAt, md.SessionExpiresAt)
	}}
}
func (a *App) cmdAuthStatus(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	md, err := auth.LoadPrivyMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, output.Errorf(output.CodeAuthRequired, "run oku auth login")
	}
	if err != nil {
		return nil, err
	}
	if err = e.checkBinding(md, p); err != nil {
		return nil, err
	}
	if !e.cmd.Bool("verify") {
		return statusResult(md, false), nil
	}
	s, err := e.unlock(p, "")
	if err != nil {
		return nil, err
	}
	c := e.client(p)
	s.apply(c)
	ctx, cancel := e.requestCtx()
	defer cancel()
	if err = validateAgent(ctx, c, s.md.AgentAddress, s.md.AccountAddress); err != nil {
		return nil, err
	}
	return statusResult(s.md, true), nil
}
func (a *App) cmdAuthLogout(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	release, err := auth.LockProfile(p.Name)
	if err != nil {
		return nil, err
	}
	defer release()
	md, err := auth.LoadPrivyMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		if _, legacyErr := auth.LoadMetadata(p.Name); legacyErr == nil {
			if err = auth.Delete(p.Name); err != nil {
				return nil, err
			}
			return &result{data: map[string]any{"local_credentials": "deleted", "legacy": true}, human: func(w io.Writer) { fmt.Fprintln(w, "Legacy device credential deleted. "+boundaries["not_revoked"]) }}, nil
		}
		return nil, output.Errorf(output.CodeNotFound, "no credential for profile")
	}
	if err != nil {
		return nil, err
	}
	if err = e.checkBinding(md, p); err != nil {
		if deleteErr := auth.DeletePrivy(p.Name); deleteErr != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, deleteErr, "deleting unbound credential")
		}
		return &result{data: map[string]any{"local_credentials": "deleted", "oku_session": "not_attempted", "privy_grant": "not_attempted", "warning": "credential binding changed; revoke grants separately", "boundaries": boundaries}, human: func(w io.Writer) {
			fmt.Fprintln(w, "Local credential deleted; binding changed, remote grants were not revoked. "+boundaries["not_revoked"])
		}}, nil
	}
	pass, err := e.passphrase(false)
	if err != nil {
		return nil, err
	}
	md, sec, err := auth.UnlockPrivy(p.Name, pass)
	if err != nil {
		return nil, err
	}
	if err = e.checkBinding(md, p); err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	remote := "not_attempted"
	c := e.client(p)
	c.SessionID, c.SessionToken = md.SessionID, sec.SessionToken
	if md.SessionID != "" && sec.SessionToken != "" {
		if err = c.Auth(ctx, "Logout", struct{}{}, nil, true); err != nil {
			remote = "Oku logout failed: " + api.Redact(err.Error())
		} else {
			remote = "Oku session logged out"
		}
	}
	pc, pcErr := e.privyClient(p)
	grant := "not_attempted"
	if pcErr == nil && sec.AccessToken != "" {
		if err = pc.RevokeGrant(ctx, sec.AccessToken); err != nil {
			grant = "revoke unavailable: " + api.Redact(err.Error())
		} else {
			grant = "revoked"
		}
	}
	if err = auth.DeletePrivy(p.Name); err != nil {
		return nil, err
	}
	return &result{data: map[string]any{"local_credentials": "deleted", "oku_session": remote, "privy_grant": grant, "boundaries": boundaries}, human: func(w io.Writer) {
		fmt.Fprintf(w, "Local credential deleted; %s; Privy grant %s.\n%s\n", remote, grant, boundaries["not_revoked"])
	}}, nil
}
