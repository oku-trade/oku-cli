package cli

import (
	"bytes"
	"context"
	"errors"
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

// maxPassphraseAttempts bounds interactive re-prompts when migrating a
// legacy passphrase-protected credential.
const maxPassphraseAttempts = 5

// legacyCredentialError tells the user to convert a credential written by an
// older release before it can be used.
func legacyCredentialError(profile string) error {
	return output.Errorf(output.CodeAuthRequired, "profile %q has a credential in the old passphrase-protected format; run 'oku auth unlock' in your own terminal once to convert it", profile).
		WithDetail("hint", "oku auth unlock --profile "+profile)
}

// loadCredential reads a profile's credential under the caller's profile lock.
func (e *env) loadCredential(profile string) (*auth.Metadata, *auth.Secrets, error) {
	md, sec, err := auth.LoadPrivyWithInteraction(profile, e.mayPromptForKeychain(), e.guiSession())
	if errors.Is(err, auth.ErrLegacyCredential) {
		return nil, nil, legacyCredentialError(profile)
	}
	// Translate native store failures once so every caller (signing included)
	// reports KEYCHAIN_* codes. A missing credential stays raw for callers
	// that handle it.
	if err != nil && !errors.Is(err, auth.ErrNoCredentials) {
		return nil, nil, credentialLoadError(err)
	}
	return md, sec, err
}

func (e *env) mayPromptForKeychain() bool {
	return e.app.StdinIsTTY && e.app.StderrIsTTY && !e.g.json && !e.g.inProcess
}

func (e *env) guiSession() bool {
	return e.app.Getenv("SSH_CONNECTION") == "" && e.app.Getenv("SSH_TTY") == ""
}

func (e *env) loadMetadata(profile string) (*auth.Metadata, error) {
	return auth.LoadPrivyMetadataWithInteraction(profile, e.mayPromptForKeychain(), e.guiSession())
}

// legacyPassphrase reads the passphrase of a legacy credential from
// OKU_PASSPHRASE_FILE or an interactive prompt. Only auth unlock uses it.
func (e *env) legacyPassphrase() ([]byte, error) {
	if path := e.app.Getenv("OKU_PASSPHRASE_FILE"); path != "" {
		fi, err := os.Stat(path)
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase file")
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return nil, output.Errorf(output.CodeSignerUnavailable, "OKU_PASSPHRASE_FILE must be mode 0600")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase file")
		}
		return bytes.TrimRight(b, "\r\n"), nil
	}
	if !e.app.StdinIsTTY {
		return nil, output.Errorf(output.CodeUsage, "converting the old credential needs its passphrase: run 'oku auth unlock' in your own terminal")
	}
	p, err := e.app.ReadSecret("Passphrase of your existing Oku Agent credential: ")
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase")
	}
	return p, nil
}

type session struct {
	md         *auth.Metadata
	token      string
	sessionID  string
	signer     signing.Signer
	invalidate func()
}

func (s *session) apply(c *api.Client) {
	c.SessionID = s.sessionID
	c.SessionToken = s.token
	c.OnUnauthenticated = s.invalidate
}
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
	pre, err := e.loadMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, missingCredential(p.Name)
	}
	if err != nil {
		return nil, credentialLoadError(err)
	}
	if err = e.checkBinding(pre, p); err != nil {
		return nil, err
	}
	md, sec, err := e.loadCredential(p.Name)
	if err != nil {
		var oe *output.Error
		if errors.As(err, &oe) {
			return nil, err
		}
		return nil, credentialLoadError(err)
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
	if err := e.refreshLocked(ctx, p.Name, md, sec, pc); err != nil {
		return nil, err
	}
	save := func() error { md.Generation++; return auth.SavePrivy(p.Name, *md, *sec) }
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
			c.SessionID, c.SessionToken = "", ""
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
	s := &session{md: md, token: sec.SessionToken, sessionID: md.SessionID}
	s.invalidate = func() { e.invalidateSession(p.Name, s) }
	return s, nil
}

// invalidateSession compares the failed session under the profile lock so a
// newer credential or session written by another process is never erased.
func (e *env) invalidateSession(profile string, failed *session) {
	release, err := auth.LockProfile(profile)
	if err != nil {
		return
	}
	defer release()
	md, sec, err := e.loadCredential(profile)
	if err != nil {
		return
	}
	if md.SessionID != failed.sessionID || sec.SessionToken != failed.token || md.Generation != failed.md.Generation {
		return
	}
	md.SessionID, md.SessionExpiresAt, sec.SessionToken = "", "", ""
	md.Generation++
	_ = auth.SavePrivy(profile, *md, *sec)
}

// refreshLocked must be called after reloading under LockProfile. Rotation is
// persisted before the new access token is used for any network operation.
func (e *env) refreshLocked(ctx context.Context, profile string, md *auth.Metadata, sec *auth.Secrets, pc *privy.Client) error {
	expiry, _ := time.Parse(time.RFC3339, md.AccessExpiresAt)
	if !expiry.Before(e.app.Now().Add(2 * time.Minute)) {
		return nil
	}
	tok, err := pc.Refresh(ctx, sec.RefreshToken)
	if err != nil {
		return output.Wrap(output.CodeAuthRequired, err, "Privy refresh failed; run oku auth login")
	}
	sec.RefreshToken, sec.AccessToken = tok.RefreshToken, tok.AccessToken
	md.AccessExpiresAt = tok.AccessExpiresAt.UTC().Format(time.RFC3339)
	md.Generation++
	if err := auth.SavePrivy(profile, *md, *sec); err != nil {
		return output.Wrap(output.CodeSignerUnavailable, err, "persisting rotated refresh token; run oku auth login if rotation was lost")
	}
	return nil
}
func (e *env) signerFor(p *resolved, s *session) (signing.Signer, error) {
	release, err := auth.LockProfile(p.Name)
	if err != nil {
		return nil, err
	}
	defer release()
	md, sec, err := e.loadCredential(p.Name)
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
	if err := e.refreshLocked(ctx, p.Name, md, sec, pc); err != nil {
		return nil, err
	}
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

// missingCredential explains a missing credential. A keyring profile whose
// item vanished is reported as such.
func missingCredential(profile string) error {
	if st, err := auth.StoreFor(profile); err == nil && st == auth.StoreKeyring {
		return output.Errorf(output.CodeAuthRequired, "profile %q uses the desktop keyring but its credential item is missing; run oku auth login", profile)
	}
	return output.Errorf(output.CodeAuthRequired, "run oku auth login")
}

// credentialLoadError maps an unreachable native store to a clear error.
// It never falls back to another store.
func credentialLoadError(err error) error {
	var oe *output.Error
	if errors.As(err, &oe) {
		return err
	}
	if runtime.GOOS == "darwin" {
		switch {
		case errors.Is(err, auth.ErrKeychainLocked):
			return output.Wrap(output.CodeKeychainLocked, err, "macOS login Keychain is locked; run 'security unlock-keychain ~/Library/Keychains/login.keychain-db' in your own terminal, then retry")
		case errors.Is(err, auth.ErrKeychainAccessDenied):
			return output.Wrap(output.CodeKeychainAccessDenied, err, "this oku binary cannot access the macOS Keychain item after an update; rerun from a terminal in the macOS desktop session and choose Always Allow in the Keychain dialog")
		case errors.Is(err, auth.ErrKeychainCanceled):
			return output.Wrap(output.CodeKeychainCanceled, err, "macOS Keychain authorization was canceled; rerun interactively when ready")
		case errors.Is(err, auth.ErrKeychainUnavailable):
			return output.Wrap(output.CodeSignerUnavailable, err, "macOS Keychain unavailable; unlock the login Keychain in your own terminal or sign in to a graphical session")
		}
	}
	if errors.Is(err, auth.ErrKeychainUnavailable) {
		return output.Wrap(output.CodeSignerUnavailable, err, "credential store unavailable (unlock your desktop keyring, or use 'oku auth migrate --to file' on headless machines)")
	}
	return output.Wrap(output.CodeSignerUnavailable, err, "loading credential")
}

// storeLabel names the store a profile uses, for status output.
func storeLabel(profile string) string {
	st, err := auth.StoreFor(profile)
	if err != nil {
		return "unknown"
	}
	return string(st)
}
