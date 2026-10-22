package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// maxPassphraseAttempts bounds interactive re-prompts so a stuck terminal
// cannot loop forever.
const maxPassphraseAttempts = 5

// newPassphrase is the login-time choice: a passphrase, or (after explicit
// confirmation) none at all.
type newPassphrase struct {
	pass        []byte
	unprotected bool
}

// passphrase returns the cached or entered passphrase for unlocking an existing
// credential. With confirm it prompts for a new one (tests and legacy callers).
func (e *env) passphrase(confirm bool) ([]byte, error) {
	if runtime.GOOS == "darwin" {
		return nil, nil
	}
	if confirm {
		np, err := e.chooseFilePassphrase()
		if err != nil {
			return nil, err
		}
		return np.pass, nil
	}
	return e.filePassphrase()
}

// choosePassphrase picks the passphrase for a new credential. Invalid or
// mismatched entries re-prompt instead of failing the login. An empty entry
// offers to skip the passphrase, which requires two explicit confirmations.
func (e *env) choosePassphrase(st auth.Store) (*newPassphrase, error) {
	if st != auth.StoreFile {
		return &newPassphrase{}, nil
	}
	return e.chooseFilePassphrase()
}
func (e *env) chooseFilePassphrase() (*newPassphrase, error) {
	if e.app.Getenv("OKU_PASSPHRASE_FILE") != "" || !e.app.StdinIsTTY {
		p, err := e.filePassphrase()
		if err != nil {
			return nil, err
		}
		return &newPassphrase{pass: p}, nil
	}
	for attempt := 0; attempt < maxPassphraseAttempts; attempt++ {
		p, err := e.app.ReadSecret("New Oku Agent credential passphrase (8+ characters, empty to skip): ")
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase")
		}
		if len(p) == 0 {
			ok, err := e.confirmNoPassphrase()
			if err != nil {
				return nil, err
			}
			if ok {
				return &newPassphrase{unprotected: true}, nil
			}
			continue
		}
		if len(p) < 8 {
			e.diag("Passphrase must be at least 8 characters. Try again.")
			continue
		}
		again, err := e.app.ReadSecret("Repeat passphrase: ")
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase")
		}
		if !bytes.Equal(p, again) {
			e.diag("Passphrases do not match. Try again.")
			continue
		}
		e.pass = bytes.Clone(p)
		return &newPassphrase{pass: p}, nil
	}
	return nil, output.Errorf(output.CodeInvalidArgument, "no valid passphrase after %d attempts", maxPassphraseAttempts)
}

// confirmNoPassphrase asks twice before storing a credential without a
// passphrase. Anything but the exact answers keeps the passphrase.
func (e *env) confirmNoPassphrase() (bool, error) {
	e.diag("\nWARNING: storing the credential WITHOUT a passphrase is INSECURE.")
	e.diag("Anyone or any program that can read %s can trade and sign as your agent until you revoke it.", credentialDirHint())
	ans, err := e.readLine("ARE YOU SURE you want to skip the passphrase? Type 'yes' to continue: ")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(ans) != "yes" {
		e.diag("Keeping passphrase protection.")
		return false, nil
	}
	ans, err = e.readLine("ARE YOU REALLY, REALLY SURE? THIS IS INSECURE. Type 'store without passphrase' to confirm: ")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(ans) != "store without passphrase" {
		e.diag("Keeping passphrase protection.")
		return false, nil
	}
	e.diag("Storing credential without a passphrase.")
	return true, nil
}

func credentialDirHint() string {
	if p, err := auth.PrivyPath("x"); err == nil {
		return filepath.Dir(p)
	}
	return "the credential file"
}

// readLine reads one echoed line from stdin. It reads byte by byte so no input
// beyond the newline is consumed.
func (e *env) readLine(prompt string) (string, error) {
	if !e.app.StdinIsTTY || e.app.Stdin == nil {
		return "", output.Errorf(output.CodeSignerUnavailable, "confirmation requires an interactive terminal")
	}
	fmt.Fprint(e.app.Stderr, prompt)
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := e.app.Stdin.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", output.Wrap(output.CodeSignerUnavailable, err, "reading confirmation")
		}
	}
	return strings.TrimRight(string(line), "\r"), nil
}

// unlockCache keeps a passphrase in the kernel user keyring keyed by the
// credential file path (not the profile name), so separate config dirs never
// share an entry.
type unlockCache interface {
	Get(path string) []byte
	Put(path string, pass []byte, ttl time.Duration) error
	Forget(path string)
}

type keyringCache struct{}

func cacheKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:12])
}
func (keyringCache) Get(path string) []byte { return auth.CachedPassphrase(cacheKey(path)) }
func (keyringCache) Put(path string, pass []byte, ttl time.Duration) error {
	return auth.CachePassphrase(cacheKey(path), pass, ttl)
}
func (keyringCache) Forget(path string) { auth.ForgetPassphrase(cacheKey(path)) }

// defaultUnlockTTL is how long an interactively entered passphrase stays
// cached. OKU_UNLOCK_TTL overrides it; 0 disables caching.
const defaultUnlockTTL = 8 * time.Hour

func (e *env) unlockTTL() time.Duration {
	if v := e.app.Getenv("OKU_UNLOCK_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return defaultUnlockTTL
}

// rememberPassphrase caches a verified passphrase so later non-interactive
// commands (coding agents, MCP) can use the credential.
func (e *env) rememberPassphrase(profile string, pass []byte, ttl time.Duration) bool {
	if e.app.UnlockCache == nil || len(pass) == 0 || ttl <= 0 {
		return false
	}
	path, err := auth.PrivyPath(profile)
	if err != nil {
		return false
	}
	return e.app.UnlockCache.Put(path, pass, ttl) == nil
}
func (e *env) cachedPassphrase(profile string) []byte {
	if e.app.UnlockCache == nil {
		return nil
	}
	path, err := auth.PrivyPath(profile)
	if err != nil {
		return nil
	}
	return e.app.UnlockCache.Get(path)
}
func (e *env) forgetPassphrase(profile string) {
	if e.app.UnlockCache == nil {
		return
	}
	if path, err := auth.PrivyPath(profile); err == nil {
		e.app.UnlockCache.Forget(path)
	}
}

// unlockCredential loads and decrypts a profile's credential. Sources, in
// order: an Unprotected credential, OKU_PASSPHRASE_FILE, the unlock cache,
// then an interactive prompt. A wrong interactive passphrase re-prompts.
// A successful interactive unlock is cached for OKU_UNLOCK_TTL.
func (e *env) unlockCredential(profile string) (*auth.Metadata, *auth.Secrets, []byte, error) {
	st, err := auth.StoreFor(profile)
	if err != nil {
		return nil, nil, nil, output.Wrap(output.CodeConfigError, err, "loading credential store")
	}
	if st != auth.StoreFile {
		// Keychain and Secret Service unlock with the user's login session;
		// there is no oku passphrase and nothing to cache.
		md, sec, err := auth.UnlockPrivy(profile, nil)
		return md, sec, nil, err
	}
	{
		if pre, err := auth.LoadPrivyMetadata(profile); err == nil && pre.Unprotected {
			md, sec, err := auth.UnlockPrivy(profile, []byte{})
			return md, sec, []byte{}, err
		}
	}
	if e.pass == nil && e.app.Getenv("OKU_PASSPHRASE_FILE") == "" {
		if cached := e.cachedPassphrase(profile); cached != nil {
			md, sec, err := auth.UnlockPrivy(profile, cached)
			if err == nil {
				e.pass = bytes.Clone(cached)
				return md, sec, cached, nil
			}
			if errors.Is(err, auth.ErrBadPassphrase) {
				e.forgetPassphrase(profile)
			}
		}
	}
	interactive := e.app.Getenv("OKU_PASSPHRASE_FILE") == "" && e.app.StdinIsTTY
	prompted := interactive && e.pass == nil
	for attempt := 1; ; attempt++ {
		pass, err := e.passphrase(false)
		if err != nil {
			return nil, nil, nil, err
		}
		md, sec, err := auth.UnlockPrivy(profile, pass)
		if err == nil {
			if prompted {
				e.rememberPassphrase(profile, pass, e.unlockTTL())
			}
			return md, sec, pass, nil
		}
		if !errors.Is(err, auth.ErrBadPassphrase) || !interactive || attempt >= maxPassphraseAttempts {
			return nil, nil, nil, err
		}
		e.pass = nil
		prompted = true
		e.diag("Wrong passphrase. Try again.")
	}
}

func (e *env) filePassphrase() ([]byte, error) {
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
		return nil, output.Errorf(output.CodeSignerUnavailable, "credential is locked: run 'oku auth unlock' in your own terminal (keeps it unlocked for %s), or set OKU_PASSPHRASE_FILE to a 0600 file", defaultUnlockTTL)
	}
	if e.pass != nil {
		return bytes.Clone(e.pass), nil
	}
	p, err := e.app.ReadSecret("Oku Agent credential passphrase: ")
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase")
	}
	e.pass = bytes.Clone(p)
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
	pre, err := auth.LoadPrivyMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, missingCredential(p.Name)
	}
	if err != nil {
		return nil, credentialLoadError(err)
	}
	if err = e.checkBinding(pre, p); err != nil {
		return nil, err
	}
	md, sec, pass, err := e.unlockCredential(p.Name)
	if err != nil {
		var oe *output.Error
		if errors.As(err, &oe) {
			return nil, err
		}
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
	if err := e.refreshLocked(ctx, p.Name, md, sec, pass, pc); err != nil {
		return nil, err
	}
	save := func() error { md.Generation++; return auth.SavePrivy(p.Name, *md, *sec, pass) }
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
	md, sec, pass, err := e.unlockCredential(profile)
	if err != nil {
		return
	}
	if md.SessionID != failed.sessionID || sec.SessionToken != failed.token || md.Generation != failed.md.Generation {
		return
	}
	md.SessionID, md.SessionExpiresAt, sec.SessionToken = "", "", ""
	md.Generation++
	_ = auth.SavePrivy(profile, *md, *sec, pass)
}

// refreshLocked must be called after reloading under LockProfile. Rotation is
// persisted before the new access token is used for any network operation.
func (e *env) refreshLocked(ctx context.Context, profile string, md *auth.Metadata, sec *auth.Secrets, pass []byte, pc *privy.Client) error {
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
	if err := auth.SavePrivy(profile, *md, *sec, pass); err != nil {
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
	md, sec, pass, err := e.unlockCredential(p.Name)
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
	if err := e.refreshLocked(ctx, p.Name, md, sec, pass, pc); err != nil {
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
// item vanished is reported as such instead of asking for a passphrase.
func missingCredential(profile string) error {
	if st, err := auth.StoreFor(profile); err == nil && st == auth.StoreKeyring {
		return output.Errorf(output.CodeAuthRequired, "profile %q uses the desktop keyring but its credential item is missing; run oku auth login", profile)
	}
	return output.Errorf(output.CodeAuthRequired, "run oku auth login")
}

// credentialLoadError maps an unreachable native store to a clear error.
// It never falls back to another store.
func credentialLoadError(err error) error {
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
