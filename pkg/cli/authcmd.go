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
	return []urfave.Flag{&urfave.StringFlag{Name: "api-url", Usage: "Oku API origin for the profile created when none exists"}, &urfave.BoolFlag{Name: "no-open", Usage: "print verification URL without opening browser"}, &urfave.DurationFlag{Name: "timeout", Value: 10 * time.Minute, Usage: "maximum time to wait for authorization"}, &urfave.StringFlag{Name: "credential-store", Usage: "Linux: where to keep the credential, file (0600 file, default) or keyring (desktop Secret Service); default $OKU_CREDENTIAL_STORE, else the profile's current store. Ignored on macOS (Keychain)"}}
}
func authStatusFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.BoolFlag{Name: "verify", Usage: "refresh the credential and check Hyperliquid agent approval"}}
}

// cmdAuthUnlock converts a credential written by an older release, which was
// sealed with a passphrase, into the current plain 0600 file. It asks for the
// old passphrase once in the user's own terminal (or reads
// OKU_PASSPHRASE_FILE). Current credentials need nothing, so it is a no-op.
func (a *App) cmdAuthUnlock(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	release, err := auth.LockProfile(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "locking credential")
	}
	defer release()
	st, err := auth.StoreFor(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading credential store")
	}
	noop := func(msg string) *result {
		return &result{data: map[string]any{"profile": p.Name, "credential_store": string(st), "migrated": false, "message": msg}, human: func(w io.Writer) {
			fmt.Fprintln(w, msg)
		}}
	}
	if st != auth.StoreFile {
		return noop("Nothing to unlock: this profile keeps its credential in the " + storeName(st) + "."), nil
	}
	needPass, err := auth.LegacyNeedsPassphrase(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, missingCredential(p.Name)
	}
	if err != nil {
		if _, mdErr := e.loadMetadata(p.Name); mdErr == nil {
			return noop("Nothing to unlock: the credential is already in the current format."), nil
		}
		return nil, credentialLoadError(err)
	}
	var md *auth.Metadata
	for attempt := 1; ; attempt++ {
		var pass []byte
		if needPass {
			if pass, err = e.legacyPassphrase(); err != nil {
				return nil, err
			}
		}
		md, err = auth.MigrateLegacy(p.Name, pass)
		if err == nil {
			break
		}
		retry := needPass && errors.Is(err, auth.ErrBadPassphrase) && e.app.StdinIsTTY && e.app.Getenv("OKU_PASSPHRASE_FILE") == "" && attempt < maxPassphraseAttempts
		if !retry {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "converting credential")
		}
		e.diag("Wrong passphrase. Try again.")
	}
	if err = e.checkBinding(md, p); err != nil {
		e.diag("The converted credential does not match profile %q; run 'oku auth login' if commands report a binding error.", p.Name)
	}
	path, _ := auth.PrivyPath(p.Name)
	return &result{data: map[string]any{"profile": p.Name, "credential_store": string(st), "migrated": true, "path": path}, human: func(w io.Writer) {
		fmt.Fprintf(w, "Converted profile %s. The credential is now a plain file readable only by you (%s); no passphrase is needed anymore.\n", p.Name, path)
		fmt.Fprintln(w, "You can delete any old passphrase file and unset OKU_PASSPHRASE_FILE.")
	}}, nil
}

func storeName(st auth.Store) string {
	switch st {
	case auth.StoreKeychain:
		return "macOS Keychain"
	case auth.StoreKeyring:
		return "desktop keyring"
	}
	return string(st) + " store"
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

// autoProfileName is the profile auth login creates when none is configured.
const autoProfileName = "default"

func (a *App) cmdAuthLogin(e *env) (*result, error) {
	if e.g.account != "" {
		return nil, output.Errorf(output.CodeUsage, "the account is the wallet you sign with in the browser; do not pass --account to auth login")
	}
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	// With no profile configured at all, log in to a new "default" profile and
	// save it only after the login succeeds.
	create := false
	if p.Name == "" {
		f, loadErr := config.Load()
		if loadErr != nil {
			return nil, output.Wrap(output.CodeConfigError, loadErr, "loading config")
		}
		if len(f.Profiles) > 0 {
			return nil, output.Errorf(output.CodeProfileNotFound, "no default profile; pass --profile or run 'oku profiles use NAME'")
		}
		p.Name, create = autoProfileName, true
	}
	if u := e.cmd.String("api-url"); u != "" {
		if !create {
			return nil, output.Errorf(output.CodeUsage, "--api-url only applies when auth login creates a profile; profile %q already exists", p.Name)
		}
		if err := config.ValidateAPIURL(u); err != nil {
			return nil, output.Wrap(output.CodeInvalidArgument, err, "invalid --api-url")
		}
		p.APIURL = config.NormalizeAPIURL(u)
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
	// Check the credential store before anything is authorized remotely, so an
	// unusable keyring never leaves an approved session or grant behind.
	st, prevStore, err := e.loginStore(p.Name, create)
	if err != nil {
		return nil, err
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
	expires := e.app.Now().Add(time.Duration(da.ExpiresIn) * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(expires) {
		expires = dl
	}
	e.diag("Open %s and enter code %s", url, da.UserCode)
	e.diag("The code expires in %s (at %s).", expires.Sub(e.app.Now()).Round(time.Second), expires.Local().Format("15:04:05"))
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
	// From here on the device grant (and later the Oku session) exist remotely.
	// Any failure before the credential is saved revokes them so a failed login
	// never leaves an authorized session behind.
	var exchange api.PrivySessionExchangeResponse
	saved := false
	defer func() {
		if saved {
			return
		}
		cctx, ccancel := context.WithTimeout(context.WithoutCancel(e.ctx), 15*time.Second)
		defer ccancel()
		if exchange.Session != nil && exchange.Session.SessionID != "" && exchange.Session.Token != "" {
			c := e.client(p)
			c.SessionID, c.SessionToken = exchange.Session.SessionID, exchange.Session.Token
			if lerr := c.Auth(cctx, "Logout", struct{}{}, nil, true); lerr != nil {
				e.diag("Login failed and the Oku session could not be logged out: %s", api.Redact(lerr.Error()))
			}
		}
		if rerr := pc.RevokeGrant(cctx, tok.AccessToken); rerr != nil {
			e.diag("Login failed and the Privy grant could not be revoked: %s", api.Redact(rerr.Error()))
		}
	}()
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
	master, err := agentMaster(ctx, e.app.Getenv("OKU_HYPERLIQUID_INFO_URL"), wallet.Address)
	if err != nil {
		return nil, output.Wrap(output.CodeAuthRequired, err, "finish the steps on the Oku page")
	}
	p.Account = strings.ToLower(master)
	c := e.client(p)
	if err = c.Auth(ctx, "PrivySessionExchange", map[string]string{"accessToken": tok.AccessToken}, &exchange, false); err != nil {
		return nil, apiError(err, "PrivySessionExchange")
	}
	if exchange.Session == nil || exchange.Session.SessionID == "" || exchange.Session.Token == "" {
		return nil, output.Errorf(output.CodeAuthUnavailable, "Oku returned an incomplete session")
	}
	release, err := auth.LockProfile(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "locking credential")
	}
	defer release()
	if create {
		cfg, loadErr := config.Load()
		if loadErr != nil {
			return nil, output.Wrap(output.CodeConfigError, loadErr, "loading config")
		}
		if _, exists := cfg.Profiles[p.Name]; exists {
			return nil, output.Errorf(output.CodeConfigError, "profile changed during login; retry")
		}
		cfg.Profiles[p.Name] = &config.Profile{Network: p.Network, APIURL: p.APIURL, Account: strings.ToLower(p.Account), CredentialStore: configStore(st)}
		if cfg.DefaultProfile == "" {
			cfg.DefaultProfile = p.Name
		}
		if saveErr := cfg.Save(); saveErr != nil {
			return nil, output.Wrap(output.CodeConfigError, saveErr, "saving profile")
		}
		e.diag("Created profile %q", p.Name)
	} else {
		cfg, loadErr := config.Load()
		if loadErr != nil {
			return nil, output.Wrap(output.CodeConfigError, loadErr, "loading profile")
		}
		profile := cfg.Profiles[p.Name]
		if profile == nil || config.NormalizeAPIURL(profile.APIURL) != p.APIURL {
			return nil, output.Errorf(output.CodeConfigError, "profile changed during login; retry")
		}
		if !strings.EqualFold(profile.Account, p.Account) {
			e.diag("Profile %q account changed from %s to %s", p.Name, profile.Account, p.Account)
			profile.Account = p.Account
			if saveErr := cfg.Save(); saveErr != nil {
				return nil, output.Wrap(output.CodeConfigError, saveErr, "saving derived account")
			}
		}

	}
	md := auth.Metadata{Kind: auth.KindPrivy, Profile: p.Name, Network: p.Network, APIURL: p.APIURL, PrivyAppID: pc.AppID, AccountAddress: strings.ToLower(p.Account), AgentAddress: strings.ToLower(wallet.Address), PrivyWalletID: wallet.ID, AccessExpiresAt: tok.AccessExpiresAt.UTC().Format(time.RFC3339), SessionID: exchange.Session.SessionID, SessionExpiresAt: exchange.Session.ExpiresAt, CreatedAt: e.app.Now().UTC().Format(time.RFC3339), Status: "active", Generation: 1}
	sec := auth.Secrets{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, SessionToken: exchange.Session.Token}
	if err = auth.SavePrivyIn(st, p.Name, md, sec); err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "saving credential")
	}
	if !create && st != prevStore {
		if err = setProfileStore(p.Name, st); err != nil {
			_ = auth.DeletePrivyIn(st, p.Name)
			return nil, output.Wrap(output.CodeConfigError, err, "recording credential store")
		}
		if delErr := auth.DeletePrivyIn(prevStore, p.Name); delErr != nil {
			e.diag("Could not delete the old %s credential: %v", prevStore, delErr)
		}
		e.diag("Profile %q now keeps its credential in the %s store.", p.Name, st)
	}
	saved = true
	return statusResult(&md, true), nil
}
func statusResult(md *auth.Metadata, verified bool) *result {
	store := storeLabel(md.Profile)
	return &result{data: map[string]any{"credential": md, "credential_store": store, "verified": verified, "boundaries": boundaries}, human: func(w io.Writer) {
		fmt.Fprintf(w, "profile  %s\naccount  %s\nagent    %s\nwallet   %s\nstore    %s\naccess   %s\nsession  %s\n", md.Profile, md.AccountAddress, md.AgentAddress, md.PrivyWalletID, store, md.AccessExpiresAt, md.SessionExpiresAt)
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
	// Plain status is a diagnostic. Only --verify may request authorization.
	var md *auth.Metadata
	if e.cmd.Bool("verify") {
		md, err = e.loadMetadata(p.Name)
	} else {
		md, err = auth.LoadPrivyMetadata(p.Name)
	}
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, missingCredential(p.Name)
	}
	if err != nil {
		return nil, credentialLoadError(err)
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
	md, err := e.loadMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, output.Errorf(output.CodeNotFound, "no credential for profile")
	}
	if err != nil {
		return nil, credentialLoadError(err)
	}
	if err = e.checkBinding(md, p); err != nil {
		if deleteErr := auth.DeletePrivy(p.Name); deleteErr != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, deleteErr, "deleting unbound credential")
		}
		return &result{data: map[string]any{"local_credentials": "deleted", "oku_session": "not_attempted", "privy_grant": "not_attempted", "warning": "credential binding changed; revoke grants separately", "boundaries": boundaries}, human: func(w io.Writer) {
			fmt.Fprintln(w, "Local credential deleted; binding changed, remote grants were not revoked. "+boundaries["not_revoked"])
		}}, nil
	}
	md, sec, err := e.loadCredential(p.Name)
	if errors.Is(err, auth.ErrLegacyCredential) {
		// Logout must work without the old passphrase: delete the local copy
		// and report that remote sessions were not revoked.
		if deleteErr := auth.DeletePrivy(p.Name); deleteErr != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, deleteErr, "deleting credential")
		}
		return &result{data: map[string]any{"local_credentials": "deleted", "oku_session": "not_attempted", "privy_grant": "not_attempted", "warning": "old passphrase-protected credential deleted without revoking its session or grant", "boundaries": boundaries}, human: func(w io.Writer) {
			fmt.Fprintln(w, "Local credential deleted; it was in the old passphrase format, so its Oku session and Privy grant were not revoked. "+boundaries["not_revoked"])
		}}, nil
	}
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
