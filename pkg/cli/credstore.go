package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

// requestedStore returns the store asked for by --credential-store or
// OKU_CREDENTIAL_STORE, or "" when neither is set.
func (e *env) requestedStore(flag string) (auth.Store, bool, error) {
	v := ""
	if e.cmd != nil {
		v = e.cmd.String(flag)
	}
	if v == "" {
		v = e.app.Getenv("OKU_CREDENTIAL_STORE")
	}
	if v == "" {
		return "", false, nil
	}
	st, err := auth.ParseStore(v)
	if err != nil {
		return "", false, output.Wrap(output.CodeInvalidArgument, err, "invalid credential store")
	}
	return st, true, nil
}

// loginStore picks the store for a login and checks it is reachable before
// anything is authorized remotely. There is never a fallback: an unreachable
// keyring fails the login.
func (e *env) loginStore(profile string, create bool) (st, prev auth.Store, err error) {
	prev = auth.DefaultStore()
	if !create {
		if prev, err = auth.StoreFor(profile); err != nil {
			return "", "", output.Wrap(output.CodeConfigError, err, "loading credential store")
		}
	}
	st, set, err := e.requestedStore("credential-store")
	if err != nil {
		return "", "", err
	}
	if !set {
		st = prev
	}
	if err = auth.CheckStore(st); err != nil {
		return "", "", storeUnavailable(st, err)
	}
	return st, prev, nil
}

func storeUnavailable(st auth.Store, err error) error {
	if st == auth.StoreKeyring {
		return output.Wrap(output.CodeSignerUnavailable, err, "the desktop keyring (Secret Service) is not usable here; on servers, SSH sessions or machines without a desktop keyring use the default file store (--credential-store file)")
	}
	return output.Wrap(output.CodeSignerUnavailable, err, "credential store unavailable")
}

// configStore is the profile config value for a store. The file store is the
// default and is left implicit so older configs stay unchanged.
func configStore(st auth.Store) string {
	if st == auth.StoreKeyring {
		return string(auth.StoreKeyring)
	}
	return ""
}

func setProfileStore(profile string, st auth.Store) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p := cfg.Profiles[profile]
	if p == nil {
		return fmt.Errorf("profile %q not found", profile)
	}
	p.CredentialStore = configStore(st)
	return cfg.Save()
}

func authMigrateFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "to", Usage: "target credential store on Linux: file (passphrase) or keyring (desktop Secret Service)"}}
}

// cmdAuthMigrate moves a profile's credential between the file and keyring
// stores without a new login: read from the current store (prompting for the
// file passphrase in the user's own terminal), write to the target, verify the
// read-back, record the store in the profile, then delete the old copy.
func (a *App) cmdAuthMigrate(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	if e.cmd.String("to") == "" {
		return nil, output.Errorf(output.CodeUsage, "--to file|keyring is required")
	}
	to, err := auth.ParseStore(e.cmd.String("to"))
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "invalid --to")
	}
	release, err := auth.LockProfile(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "locking credential")
	}
	defer release()
	from, err := auth.StoreFor(p.Name)
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading credential store")
	}
	if from == to {
		return &result{data: map[string]any{"profile": p.Name, "credential_store": string(to), "migrated": false}, human: func(w io.Writer) {
			fmt.Fprintf(w, "Profile %s already uses the %s store.\n", p.Name, to)
		}}, nil
	}
	if !e.app.StdinIsTTY {
		return nil, output.Errorf(output.CodeUsage, "auth migrate requires an interactive terminal")
	}
	if err = auth.CheckStore(to); err != nil {
		return nil, storeUnavailable(to, err)
	}
	if _, err = auth.LoadPrivyMetadata(p.Name); errors.Is(err, auth.ErrNoCredentials) {
		return nil, missingCredential(p.Name)
	} else if err != nil {
		return nil, credentialLoadError(err)
	}
	md, sec, _, err := e.unlockCredential(p.Name)
	if err != nil {
		var oe *output.Error
		if errors.As(err, &oe) {
			return nil, err
		}
		return nil, credentialLoadError(err)
	}
	if err = e.checkBinding(md, p); err != nil {
		return nil, err
	}
	var pass []byte
	md.Unprotected = false
	if to == auth.StoreFile {
		e.pass = nil
		np, err := e.chooseFilePassphrase()
		if err != nil {
			return nil, err
		}
		pass, md.Unprotected = np.pass, np.unprotected
	}
	if err = auth.SavePrivyIn(to, p.Name, *md, *sec, pass); err != nil {
		_ = auth.DeletePrivyIn(to, p.Name)
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "writing credential to the "+string(to)+" store")
	}
	if got, gotSec, err := auth.UnlockPrivyIn(to, p.Name, pass); err != nil || got.Generation != md.Generation || *gotSec != *sec {
		_ = auth.DeletePrivyIn(to, p.Name)
		return nil, output.Errorf(output.CodeSignerUnavailable, "credential read-back from the %s store failed; nothing was changed", to)
	}
	if err = setProfileStore(p.Name, to); err != nil {
		_ = auth.DeletePrivyIn(to, p.Name)
		return nil, output.Wrap(output.CodeConfigError, err, "recording credential store")
	}
	if from == auth.StoreFile {
		e.forgetPassphrase(p.Name)
	}
	warning := ""
	if err = auth.DeletePrivyIn(from, p.Name); err != nil {
		warning = fmt.Sprintf("could not delete the old %s credential: %v", from, err)
	}
	if to == auth.StoreFile && !md.Unprotected && e.rememberPassphrase(p.Name, pass, e.unlockTTL()) {
		e.diag("Unlocked for %s. Run 'oku auth lock' to lock now.", e.unlockTTL())
	}
	data := map[string]any{"profile": p.Name, "credential_store": string(to), "from": string(from), "migrated": true}
	if warning != "" {
		data["warning"] = warning
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "Moved profile %s credential from the %s store to the %s store.\n", p.Name, from, to)
		if warning != "" {
			fmt.Fprintln(w, "Warning: "+warning)
		}
	}}, nil
}
