package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/buildinfo"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/privy"
	"github.com/oku-trade/oku-cli/pkg/signing"
)

func (e *env) passphrase(confirm bool) ([]byte, error) {
	if runtime.GOOS == "darwin" {
		return nil, nil
	}
	return e.legacyPassphrase(confirm)
}
func (e *env) legacyPassphrase(confirm bool) ([]byte, error) {
	if path := e.app.Getenv("OKU_PASSPHRASE_FILE"); path != "" {
		fi, err := os.Stat(path)
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase file")
		}
		if fi.Mode().Perm()&0077 != 0 {
			return nil, output.Errorf(output.CodeSignerUnavailable, "OKU_PASSPHRASE_FILE must be mode 0600")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase file")
		}
		return bytes.TrimRight(b, "\r\n"), nil
	}
	if !e.app.StdinIsTTY {
		return nil, output.Errorf(output.CodeSignerUnavailable, "credential passphrase unavailable: set OKU_PASSPHRASE_FILE to a 0600 file")
	}
	p, err := e.app.ReadSecret("Oku Agent credential passphrase: ")
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase")
	}
	if confirm {
		again, err := e.app.ReadSecret("Repeat passphrase: ")
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(p, again) {
			return nil, output.Errorf(output.CodeInvalidArgument, "passphrases do not match")
		}
	}
	if len(p) < 8 {
		return nil, output.Errorf(output.CodeInvalidArgument, "passphrase must be at least 8 characters")
	}
	return p, nil
}

type session struct {
	md        *auth.Metadata
	token     string
	sessionID string
	signer    signing.Signer
}

func (s *session) apply(c *api.Client) { c.SessionID = s.sessionID; c.SessionToken = s.token }
func (e *env) checkBinding(md *auth.Metadata, p *resolved) error {
	switch {
	case md.Profile != p.Name:
		return output.Errorf(output.CodeConfigError, "credential belongs to profile %q", md.Profile)
	case md.Network != p.Network:
		return output.Errorf(output.CodeNetworkMismatch, "credential network %s does not match %s", md.Network, p.Network)
	case md.APIURL != p.APIURL:
		return output.Errorf(output.CodeConfigError, "credential API origin differs; run oku auth login")
	case !strings.EqualFold(md.AccountAddress, p.Account):
		return output.Errorf(output.CodeConfigError, "credential account differs from profile; run oku auth login")
	case md.Kind == auth.KindPrivy && md.PrivyAppID != e.app.Getenv("OKU_PRIVY_APP_ID") && e.app.Getenv("OKU_PRIVY_APP_ID") != "":
		return output.Errorf(output.CodeConfigError, "credential Privy app differs; run oku auth login")
	}
	if md.Kind == auth.KindPrivy && e.app.Getenv("OKU_PRIVY_APP_ID") == "" && md.PrivyAppID != effectiveAppID(p) {
		return output.Errorf(output.CodeConfigError, "credential Privy app differs; run oku auth login")
	}
	return nil
}
func effectiveAppID(p *resolved) string {
	if p.PrivyAppID != "" {
		return p.PrivyAppID
	}
	return buildinfo.PrivyAppID
}
func (e *env) privyClient(p *resolved) (*privy.Client, error) {
	id := e.app.Getenv("OKU_PRIVY_APP_ID")
	if id == "" {
		id = p.PrivyAppID
	}
	if id == "" {
		id = buildinfo.PrivyAppID
	}
	if id == "" {
		return nil, output.Errorf(output.CodeConfigError, "Privy app id required: set OKU_PRIVY_APP_ID or profile privy_app_id")
	}
	base := e.app.Getenv("OKU_PRIVY_AUTH_URL")
	if base == "" {
		base = privy.DefaultAuthURL
	}
	return &privy.Client{AppID: id, AuthURL: base}, nil
}
func (e *env) unlock(p *resolved, _ string) (*session, error) {
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "a profile is required")
	}
	// Keep this lock over read, refresh, exchange and write. It prevents a second
	// process from refreshing an already-rotated token or overwriting a session.
	release, err := auth.LockProfile(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "locking credential")
	}
	defer release()
	pre, err := auth.LoadPrivyMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		if _, legacy := auth.LoadMetadata(p.Name); legacy == nil {
			return nil, output.Errorf(output.CodeAuthRequired, "legacy device credential cannot be used; run oku auth login")
		}
		return nil, output.Errorf(output.CodeAuthRequired, "run oku auth login")
	}
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "loading credential")
	}
	if err = e.checkBinding(pre, p); err != nil {
		return nil, err
	}
	pass, err := e.passphrase(false)
	if err != nil {
		return nil, err
	}
	md, sec, err := auth.UnlockPrivy(p.Name, pass)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "unlocking credential")
	}
	if md == nil || sec == nil {
		return nil, output.Errorf(output.CodeSignerUnavailable, "credential incomplete")
	}
	if err = e.checkBinding(md, p); err != nil {
		return nil, err
	}
	if md.Kind != auth.KindPrivy || md.Status != "active" || sec.RefreshToken == "" {
		return nil, output.Errorf(output.CodeAuthRequired, "run oku auth login")
	}
	pc, err := e.privyClient(p)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	save := func() error { md.Generation++; return auth.SavePrivy(p.Name, *md, *sec, pass) }
	expiry, _ := time.Parse(time.RFC3339, md.AccessExpiresAt)
	if expiry.Before(e.app.Now().Add(2 * time.Minute)) {
		tok, err := pc.Refresh(ctx, sec.RefreshToken)
		if err != nil {
			return nil, output.Wrap(output.CodeAuthRequired, err, "Privy refresh failed; run oku auth login")
		}
		sec.RefreshToken, sec.AccessToken = tok.RefreshToken, tok.AccessToken
		md.AccessExpiresAt = tok.AccessExpiresAt.UTC().Format(time.RFC3339)
		if err = save(); err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "persisting rotated refresh token; run oku auth login if rotation was lost")
		}
	}
	c := e.client(p)
	sessionExpiry, _ := time.Parse(time.RFC3339, md.SessionExpiresAt)
	if sessionExpiry.Before(e.app.Now().Add(15*time.Minute)) || md.SessionID == "" || sec.SessionToken == "" {
		if md.SessionID != "" && sec.SessionToken != "" {
			c.SessionID, c.SessionToken = md.SessionID, sec.SessionToken
			var ext api.ExtendSessionResponse
			err = c.Auth(ctx, "ExtendSession", struct{}{}, &ext, true)
			if err == nil && ext.Session == nil {
				return nil, output.Errorf(output.CodeAuthUnavailable, "Oku returned no extended session")
			}
			if err == nil {
				md.SessionID, sec.SessionToken, md.SessionExpiresAt = ext.Session.SessionID, ext.Session.Token, ext.Session.ExpiresAt
			}
			if err != nil {
				ae, ok := api.AsError(err)
				if !ok || ae.Code != "unauthenticated" {
					return nil, output.Wrap(output.CodeAuthUnavailable, err, "extending Oku session")
				}
			}
		}
		if err != nil || md.SessionID == "" || sec.SessionToken == "" {
			var ex api.PrivySessionExchangeResponse
			if err = c.Auth(ctx, "PrivySessionExchange", map[string]string{"accessToken": sec.AccessToken}, &ex, false); err != nil {
				return nil, output.Wrap(output.CodeAuthRequired, err, "exchanging Privy session")
			}
			if ex.Session == nil {
				return nil, output.Errorf(output.CodeAuthUnavailable, "Oku returned no session")
			}
			md.SessionID, sec.SessionToken, md.SessionExpiresAt = ex.Session.SessionID, ex.Session.Token, ex.Session.ExpiresAt
		}
		if err = save(); err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "saving Oku session")
		}
	}
	return &session{md: md, token: sec.SessionToken, sessionID: md.SessionID}, nil
}
func describeScopes(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

func (e *env) signerFor(p *resolved, s *session) (signing.Signer, error) {
	release, err := auth.LockProfile(p.Name)
	if err != nil {
		return nil, err
	}
	defer release()
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
	if !strings.EqualFold(md.AgentAddress, s.md.AgentAddress) || md.PrivyWalletID != s.md.PrivyWalletID {
		return nil, output.Errorf(output.CodeSignerUnavailable, "agent changed since unlock; retry submission")
	}
	pc, err := e.privyClient(p)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	ps, err := pc.Authenticate(ctx, sec.AccessToken)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "Privy wallet authentication")
	}
	for _, wallet := range ps.Wallets() {
		if wallet.ID == md.PrivyWalletID && strings.EqualFold(wallet.Address, md.AgentAddress) && strings.EqualFold(wallet.ChainType, "ethereum") {
			return &privySigner{session: ps, walletID: wallet.ID, address: md.AgentAddress}, nil
		}
	}
	return nil, output.Errorf(output.CodeSignerUnavailable, "stored Privy wallet is not the approved agent")
}

var _ io.Writer
var _ context.Context
