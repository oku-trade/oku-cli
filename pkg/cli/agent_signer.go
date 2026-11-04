package cli

import (
	"strings"

	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/privy"
)

// agentSigner unlocks the profile session and authenticates the Privy
// session, refusing unless it exposes exactly the enrolled agent wallet.
// It returns the session and the Privy wallet ID to sign with.
func (a *App) agentSigner(e *env, w *resolvedWallet) (*privy.Session, string, error) {
	s, err := e.unlock(w.profile, "")
	if err != nil {
		return nil, "", err
	}
	pc, err := e.privyClient(w.profile)
	if err != nil {
		return nil, "", err
	}
	_, sec, err := e.loadCredential(w.profile.Name)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	ps, err := pc.Authenticate(ctx, sec.AccessToken)
	if err != nil {
		return nil, "", output.Wrap(output.CodeAuthRequired, err, "Privy authentication")
	}
	for _, wallet := range ps.Wallets() {
		if wallet.ID == s.md.PrivyWalletID && strings.EqualFold(wallet.Address, w.agent) && wallet.ChainType == "ethereum" {
			return ps, s.md.PrivyWalletID, nil
		}
	}
	return nil, "", output.Errorf(output.CodeSignerUnavailable, "authenticated Privy wallet differs from agent")
}
