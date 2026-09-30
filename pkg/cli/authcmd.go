package cli

import (
	"errors"
	"fmt"
	urfave "github.com/urfave/cli/v3"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/signing"
)

const (
	minPollInterval = 5 * time.Second
	maxEnrollWait   = 15 * time.Minute
)

func authSetupFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "device-name", Usage: "name shown on the Oku devices page (default: hostname)"},
		&urfave.BoolFlag{Name: "code-stdin", Usage: "read the enrollment code from stdin instead of a prompt"},
		&urfave.BoolFlag{Name: "resume", Usage: "resume polling an interrupted enrollment"},
	}
}

func authStatusFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.BoolFlag{Name: "verify", Usage: "check the device credential with Oku (requires unlock)"}}
}

func (a *App) cmdAuthSetup(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" || p.Account == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "auth setup needs a profile with an account").
			WithDetail("hint", "oku profiles add NAME --account 0x... --default")
	}
	resume := e.cmd.Bool("resume")
	md, err := auth.LoadMetadata(p.Name)
	switch {
	case err == nil && md.Status == "active":
		return nil, output.Errorf(output.CodeConflict, "profile %q is already enrolled as device %s; run 'oku auth revoke' first", p.Name, md.DeviceID)
	case err == nil && !resume:
		return nil, output.Errorf(output.CodeConflict, "an enrollment for %q is pending; use --resume or 'oku auth revoke' to discard it", p.Name)
	case err != nil && !errors.Is(err, auth.ErrNoCredentials):
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "loading credentials")
	case errors.Is(err, auth.ErrNoCredentials) && resume:
		return nil, output.Errorf(output.CodeNotFound, "no pending enrollment for %q", p.Name)
	}
	if err == nil {
		if berr := checkBinding(md, p); berr != nil {
			return nil, berr
		}
		pass, perr := e.passphrase(false)
		if perr != nil {
			return nil, perr
		}
		_, sec, uerr := auth.Unlock(p.Name, pass)
		if uerr != nil {
			return nil, output.Wrap(output.CodeSignerUnavailable, uerr, "unlocking pending enrollment")
		}
		return e.pollEnrollment(p, md, sec, pass)
	}

	name := e.cmd.String("device-name")
	if name == "" {
		name, _ = os.Hostname()
	}
	name, err = signing.ValidateDeviceName(name)
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "device name")
	}
	code, err := e.enrollmentCode(e.cmd.Bool("code-stdin"))
	if err != nil {
		return nil, err
	}
	pass, err := e.passphrase(true)
	if err != nil {
		return nil, err
	}
	key, err := signing.GenerateKey()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "generating agent key")
	}
	signer, err := signing.NewKeySigner(key)
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "agent key")
	}
	agent := strings.ToLower(signer.Address())
	sig, err := signing.SignPersonal(signer, signing.EnrollmentMessage(code, agent, name))
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "signing enrollment")
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	var red api.RedeemEnrollmentResponse
	err = e.client(p).Device(ctx, "RedeemEnrollment", api.RedeemEnrollmentRequest{
		EnrollmentCode: code, AgentAddress: agent, DeviceName: name, Signature: sig,
	}, &red, false)
	if err != nil {
		return nil, enrollError(err, "redeeming enrollment code")
	}
	if red.EnrollmentID == "" || red.PollSecret == "" {
		return nil, output.Errorf(output.CodeUpstream, "incomplete enrollment response")
	}
	md = &auth.Metadata{
		Profile: p.Name, Network: p.Network, APIURL: p.APIURL, DeviceName: name,
		AgentAddress: agent, AccountAddress: strings.ToLower(p.Account),
		CreatedAt: e.app.Now().UTC().Format(time.RFC3339), Status: "pending",
		EnrollmentID: red.EnrollmentID, ExpiresAt: red.ExpiresAt,
	}
	sec := &auth.Secrets{AgentKey: key, PollSecret: red.PollSecret}
	if err := auth.Save(p.Name, *md, *sec, pass); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "saving pending enrollment")
	}
	e.diag("Confirmation code: %s", red.ConfirmationCode)
	e.diag("Enter this code on the Oku website to approve device %q for %s.", name, p.Account)
	e.diag("Waiting for approval (Ctrl-C to stop; resume with 'oku auth setup --resume')...")
	return e.pollEnrollment(p, md, sec, pass)
}

func (e *env) pollEnrollment(p *resolved, md *auth.Metadata, sec *auth.Secrets, pass []byte) (*result, error) {
	deadline := e.app.Now().Add(maxEnrollWait)
	if t, err := time.Parse(time.RFC3339, md.ExpiresAt); err == nil && t.Before(deadline) {
		deadline = t.Add(30 * time.Second)
	}
	interval := minPollInterval
	for {
		if e.app.Now().After(deadline) {
			return nil, output.Errorf(output.CodeAuthRequired, "enrollment expired before approval; run 'oku auth revoke' then start again")
		}
		if err := e.app.Sleep(e.ctx, interval); err != nil {
			return nil, output.Wrap(output.CodeInternal, err, "waiting")
		}
		ctx, cancel := e.requestCtx()
		var poll api.PollEnrollmentResponse
		err := e.client(p).Device(ctx, "PollEnrollment", api.PollEnrollmentRequest{
			EnrollmentID: md.EnrollmentID, PollSecret: sec.PollSecret,
		}, &poll, false)
		cancel()
		if err != nil {
			ae, _ := api.AsError(err)
			if ae != nil && (ae.Code == "resource_exhausted" || ae.Ambiguous) {
				interval = min(interval*2, time.Minute)
				continue
			}
			return nil, enrollError(err, "polling enrollment")
		}
		switch poll.Status {
		case "pending_redeem", "pending_approval", "device_verified":
			continue
		case "approved", "claimed":
			return e.finishEnrollment(p, md, sec, pass, &poll)
		case "denied", "expired":
			return nil, output.Errorf(output.CodeAuthRequired, "enrollment %s on the Oku website; run 'oku auth revoke' to discard local state", poll.Status)
		default:
			return nil, output.Errorf(output.CodeUpstream, "unexpected enrollment status %q", poll.Status)
		}
	}
}

func (e *env) finishEnrollment(p *resolved, md *auth.Metadata, sec *auth.Secrets, pass []byte, poll *api.PollEnrollmentResponse) (*result, error) {
	if poll.DeviceToken == "" {
		// Token is delivered once. A claimed enrollment without a token here
		// means it was lost; never assume success.
		return nil, output.Errorf(output.CodeAuthRequired,
			"enrollment was approved but the device token was not delivered to this session; revoke the device on the Oku website and enroll again")
	}
	if !strings.HasPrefix(poll.DeviceToken, "okud_") {
		return nil, output.Errorf(output.CodeUpstream, "unexpected device token format")
	}
	if !strings.EqualFold(poll.AccountAddress, md.AccountAddress) {
		return nil, output.Errorf(output.CodeConfigError,
			"Oku approved this device for account %s but profile %q uses %s; not storing the credential. Revoke the device on the Oku website.",
			poll.AccountAddress, p.Name, md.AccountAddress)
	}
	for _, s := range poll.Scopes {
		if s != "read" && s != "trade" && s != "risk" {
			return nil, output.Errorf(output.CodeUpstream, "unexpected scope %q", s)
		}
	}
	md.Status, md.DeviceID, md.Scopes = "active", poll.DeviceID, poll.Scopes
	sec.DeviceToken, sec.PollSecret = poll.DeviceToken, ""
	if err := auth.Save(p.Name, *md, *sec, pass); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "saving device credential")
	}
	return e.statusResult(md, nil), nil
}

func enrollError(err error, what string) error {
	ae, ok := api.AsError(err)
	if !ok {
		return output.Wrap(output.CodeInternal, err, what)
	}
	switch ae.Code {
	case "unimplemented":
		return output.Errorf(output.CodeAuthUnavailable, "%s: device enrollment is not available on this Oku API deployment", what)
	case "not_found", "invalid_argument", "failed_precondition", "permission_denied", "unauthenticated":
		return output.Errorf(output.CodeAuthRequired, "%s: rejected by Oku (%s); the code may be invalid, expired or already used", what, ae.Code)
	case "resource_exhausted":
		return output.Errorf(output.CodeRateLimited, "%s: rate limited", what).WithRetry()
	}
	return output.Errorf(output.CodeUpstream, "%s: %s", what, ae.Code).WithRetry()
}

// boundaries is included in auth output so agents and people see what the
// credential can and cannot do.
var boundaries = map[string]string{
	"mode":         "trusted-host delegated signing (alpha)",
	"agent_key":    "a local Hyperliquid agent key, not your wallet key; anyone who can unlock it on this host can trade this account on Hyperliquid directly",
	"oku_revoke":   "oku auth revoke stops Oku API access for this device only",
	"not_revoked":  "Hyperliquid agent approval, open orders and positions are unaffected by Oku revocation; remove the agent and manage orders from the Oku website or Hyperliquid",
	"local_limits": "local checks are not a security boundary against this host",
}

func (e *env) statusResult(md *auth.Metadata, remote *api.Device) *result {
	data := map[string]any{"credential": md, "boundaries": boundaries}
	if remote != nil {
		data["remote_device"] = remote
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "profile   %s\nstatus    %s\naccount   %s\nagent     %s\ndevice    %s (%s)\nscopes    %s\napi       %s\n",
			md.Profile, md.Status, md.AccountAddress, md.AgentAddress, md.DeviceName, orDash(md.DeviceID), describeScopes(md.Scopes), md.APIURL)
		if remote != nil {
			fmt.Fprintf(w, "remote    %s (last used %s)\n", remote.Status, orDash(remote.LastUsedAt))
		}
		fmt.Fprintln(w, "note: "+boundaries["not_revoked"])
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
	md, err := auth.LoadMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, output.Errorf(output.CodeAuthRequired, "profile %q is not enrolled", p.Name).WithDetail("hint", "oku auth setup")
	}
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "loading credentials")
	}
	if err := checkBinding(md, p); err != nil {
		return nil, err
	}
	if !e.cmd.Bool("verify") {
		return e.statusResult(md, nil), nil
	}
	s, err := e.unlock(p, "")
	if err != nil {
		return nil, err
	}
	c := e.client(p)
	c.Token = s.token
	ctx, cancel := e.requestCtx()
	defer cancel()
	var resp api.DeviceResponse
	if err := c.Device(ctx, "GetCurrentDevice", struct{}{}, &resp, true); err != nil {
		return nil, enrollError(err, "verifying device")
	}
	if !strings.EqualFold(resp.Device.AgentAddress, md.AgentAddress) || !strings.EqualFold(resp.Device.AccountAddress, md.AccountAddress) {
		return nil, output.Errorf(output.CodeConfigError, "Oku reports a different agent/account for this credential")
	}
	return e.statusResult(md, &resp.Device), nil
}

func (a *App) cmdAuthRevoke(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "no profile selected")
	}
	md, err := auth.LoadMetadata(p.Name)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, output.Errorf(output.CodeNotFound, "profile %q has no stored credentials", p.Name)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "loading credentials")
	}
	remote := "not_enrolled"
	if md.Status == "active" {
		s, err := e.unlock(p, "")
		if err != nil {
			return nil, err
		}
		c := e.client(p)
		c.Token = s.token
		ctx, cancel := e.requestCtx()
		defer cancel()
		var resp api.DeviceResponse
		if err := c.Device(ctx, "RevokeCurrentDevice", struct{}{}, &resp, true); err != nil {
			ae, _ := api.AsError(err)
			if ae == nil || (ae.Code != "unauthenticated" && ae.Code != "not_found") {
				return nil, enrollError(err, "revoking device (local credentials kept)")
			}
			remote = "already_revoked_or_unknown"
		} else {
			remote = resp.Device.Status
		}
	}
	if err := auth.Delete(p.Name); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "deleting local credentials")
	}
	data := map[string]any{"profile": p.Name, "device_id": md.DeviceID, "remote_status": remote, "local_credentials": "deleted", "boundaries": boundaries}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "device %s: Oku API access %s; local credentials deleted\n", orDash(md.DeviceID), remote)
		fmt.Fprintln(w, "note: "+boundaries["not_revoked"])
	}}, nil
}

// cmdAuthMigrateKeychain explicitly moves a legacy encrypted-file credential
// into the macOS Keychain. It never runs implicitly.
func (a *App) cmdAuthMigrateKeychain(e *env) (*result, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, output.Errorf(output.CodeProfileNotFound, "a profile is required").WithDetail("hint", "pass --profile NAME")
	}
	if runtime.GOOS != "darwin" {
		return nil, output.Errorf(output.CodeUsage, "auth migrate-keychain is only available on macOS; %s keeps credentials in the encrypted file store", runtime.GOOS).
			WithDetail("hint", "no migration is needed on this platform")
	}
	pass, err := e.legacyPassphrase(false)
	if err != nil {
		return nil, err
	}
	if err := auth.MigrateLegacy(p.Name, pass); err != nil {
		if errors.Is(err, auth.ErrUnsupportedPlatform) {
			return nil, output.Wrap(output.CodeUsage, err, "auth migrate-keychain").WithDetail("hint", "use an official macOS build (CGO enabled)")
		}
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "migrating credential; the encrypted file was left in place unless the Keychain item was verified")
	}
	return &result{data: map[string]any{"profile": p.Name, "migrated": true}, human: func(w io.Writer) {
		fmt.Fprintf(w, "migrated %s credential to the macOS Keychain\n", p.Name)
	}}, nil
}
