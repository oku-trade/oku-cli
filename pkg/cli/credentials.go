package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/signing"
)

// passphrase obtains the credential passphrase from OKU_PASSPHRASE_FILE
// (must be mode 0600) or an interactive masked prompt. It never falls back
// to plaintext storage and never reads the passphrase from argv.
func (e *env) passphrase(confirm bool) ([]byte, error) {
	if path := e.app.Getenv("OKU_PASSPHRASE_FILE"); path != "" {
		fi, err := os.Stat(path)
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading OKU_PASSPHRASE_FILE")
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return nil, output.Errorf(output.CodeSignerUnavailable, "OKU_PASSPHRASE_FILE must not be accessible by other users (chmod 600)")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading OKU_PASSPHRASE_FILE")
		}
		return bytes.TrimRight(raw, "\r\n"), nil
	}
	if !e.app.StdinIsTTY {
		return nil, output.Errorf(output.CodeSignerUnavailable, "credential passphrase unavailable: no terminal and OKU_PASSPHRASE_FILE is not set").
			WithDetail("hint", "run interactively, or set OKU_PASSPHRASE_FILE to a 0600 file")
	}
	p, err := e.app.ReadSecret("Oku CLI credential passphrase: ")
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase")
	}
	if confirm {
		again, err := e.app.ReadSecret("Repeat passphrase: ")
		if err != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, err, "reading passphrase")
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

// enrollmentCode reads the one-time code from a masked prompt or, with
// --code-stdin, one line of stdin. Codes are never accepted as arguments.
func (e *env) enrollmentCode(fromStdin bool) (string, error) {
	var raw string
	switch {
	case fromStdin:
		line, err := bufio.NewReader(io.LimitReader(e.app.Stdin, 256)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", output.Wrap(output.CodeInvalidArgument, err, "reading enrollment code from stdin")
		}
		raw = line
	case e.app.StdinIsTTY:
		b, err := e.app.ReadSecret("Enrollment code from the Oku website: ")
		if err != nil {
			return "", output.Wrap(output.CodeInvalidArgument, err, "reading enrollment code")
		}
		raw = string(b)
	default:
		return "", output.Errorf(output.CodeUsage, "no terminal for the enrollment code prompt").
			WithDetail("hint", "pipe the code with --code-stdin (never pass it as an argument)")
	}
	code := signing.NormalizeEnrollmentCode(raw)
	if len(code) != 16 || strings.Trim(code, "0123456789ABCDEFGHJKMNPQRSTVWXYZ") != "" {
		return "", output.Errorf(output.CodeInvalidArgument, "enrollment code must be 16 Crockford base32 characters (XXXX-XXXX-XXXX-XXXX)")
	}
	return code, nil
}

// session is an unlocked, verified credential bound to the current profile.
type session struct {
	md     *auth.Metadata
	token  string
	signer *signing.KeySigner
}

// checkBinding refuses to use a credential with a profile whose account,
// network or API origin differs from what it was enrolled for.
func checkBinding(md *auth.Metadata, p *resolved) error {
	switch {
	case md.Profile != p.Name:
		return output.Errorf(output.CodeConfigError, "credential belongs to profile %q, not %q", md.Profile, p.Name)
	case md.Network != p.Network:
		return output.Errorf(output.CodeNetworkMismatch, "credential network %s does not match profile network %s", md.Network, p.Network)
	case md.APIURL != p.APIURL:
		return output.Errorf(output.CodeConfigError, "credential was enrolled against %s but profile uses %s; re-enroll", md.APIURL, p.APIURL)
	case !strings.EqualFold(md.AccountAddress, p.Account):
		return output.Errorf(output.CodeConfigError, "credential account %s does not match profile account %s", md.AccountAddress, p.Account)
	}
	return nil
}

func (e *env) unlock(p *resolved, needScope string) (*session, error) {
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "a profile is required").
			WithDetail("hint", "oku profiles add NAME --account 0x...")
	}
	md, err := auth.LoadMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, output.Errorf(output.CodeAuthRequired, "profile %q is not enrolled", p.Name).
			WithDetail("hint", "oku auth setup --profile "+p.Name)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "loading credentials")
	}
	if err := checkBinding(md, p); err != nil {
		return nil, err
	}
	if md.Status != "active" {
		return nil, output.Errorf(output.CodeAuthRequired, "enrollment for %q is not complete (status %s)", p.Name, md.Status).
			WithDetail("hint", "oku auth setup --resume --profile "+p.Name)
	}
	if needScope != "" && !md.HasScope(needScope) {
		return nil, output.Errorf(output.CodeAuthRequired, "device credential lacks %q scope; enroll with trading enabled on the Oku website", needScope)
	}
	pass, err := e.passphrase(false)
	if err != nil {
		return nil, err
	}
	_, sec, err := auth.Unlock(p.Name, pass)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "unlocking credentials")
	}
	signer, err := signing.NewKeySigner(sec.AgentKey)
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "loading agent key")
	}
	if !strings.EqualFold(signer.Address(), md.AgentAddress) {
		return nil, output.Errorf(output.CodeSignerUnavailable, "stored agent key does not match enrolled agent address")
	}
	if sec.DeviceToken == "" {
		return nil, output.Errorf(output.CodeAuthRequired, "no device token stored")
	}
	return &session{md: md, token: sec.DeviceToken, signer: signer}, nil
}

func describeScopes(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return fmt.Sprint(strings.Join(s, ","))
}
