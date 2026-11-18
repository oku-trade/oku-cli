package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/ethereum/go-ethereum/common/hexutil"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func competitionRegisterFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.BoolFlag{Name: "accept", Usage: "accept the consent and sign the registration message with the agent wallet"},
		&urfave.StringFlag{Name: "digest", Usage: "exact consent digest from 'competition register' (required with --accept)"},
	}
}

func registrationOutcomeUnknown() *output.Error {
	return output.Errorf(output.CodeOutcomeUnknown, "registration was not confirmed. Check 'oku competition status' before trying again").WithExec(output.ExecUnknown)
}

// Only explicit refusals confirm that CompleteRegistration did not register the wallet.
func registrationFailure(f v2.CompetitionRegistrationFailure, msg string) error {
	name := enumName(f.String(), "COMPETITION_REGISTRATION_FAILURE_")
	text := map[v2.CompetitionRegistrationFailure]string{
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_EXPIRED:                      "the challenge expired",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_USED:                         "the challenge was already used",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_INVALID_SIGNATURE:            "the signature does not match the wallet",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_INVALID_CHALLENGE:            "the challenge is invalid",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_REGISTRATION_CLOSED:          "registration is closed",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_REGISTERED_WITH_OTHER_WALLET: "this wallet's Voltrade account already entered with another wallet",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_BANNED:                       "this wallet's Voltrade account is banned from competitions",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_UNSUPPORTED:                  "this wallet cannot join through Oku; register on Voltrade",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_RATE_LIMITED:                 "too many attempts, wait before trying again",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_REFUSED:                      "registration refused",
	}[f]
	if text == "" {
		return registrationOutcomeUnknown().WithDetail("failure", name)
	}
	if msg != "" {
		text += ": " + msg
	}
	code, retry := output.CodeRejected, false
	switch f {
	case v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_RATE_LIMITED:
		code, retry = output.CodeRateLimited, true
	case v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_EXPIRED, v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_USED, v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_INVALID_CHALLENGE:
		retry = true
	}
	if retry {
		text += ". To try again, run 'oku competition register' without --accept and approve the new preview"
	}
	return (&output.Error{Code: code, Message: text, Retryable: retry}).WithExec(output.ExecRejected).WithDetail("failure", name)
}

func startCompetitionRegistration(e *env, wallet string) (*v2.StartRegistrationResponse, error) {
	var resp v2.StartRegistrationResponse
	if err := e.competitionCall("StartRegistration", &v2.StartRegistrationRequest{Wallet: wallet}, &resp); err != nil {
		return nil, err
	}
	if resp.GetAlreadyRegistered() == nil {
		c := resp.GetChallenge()
		if c == nil || c.Message == "" || c.ChallengeToken == "" || !c.ExpiresAt.IsValid() {
			return nil, output.Errorf(output.CodeUpstream, "StartRegistration returned no valid challenge")
		}
	}
	return &resp, nil
}

func competitionRegistrationResult(wallet, digest string) *result {
	// No digest means the server reported an existing registration and nothing was signed.
	already := digest == ""
	data := map[string]any{"wallet": wallet, "registered": true, "alreadyRegistered": already}
	if !already {
		data["digest"] = digest
	}
	return &result{data: data, human: func(out io.Writer) {
		if already {
			fmt.Fprintf(out, "%s is already registered for the competition.\n", wallet)
		} else {
			fmt.Fprintf(out, "Registered %s for the competition.\nSigned consent digest: %s\nCheck standing with 'oku competition status'.\n", wallet, digest)
		}
	}}
}

func (a *App) cmdCompetitionRegister(e *env) (*result, error) {
	accept, approved := e.cmd.Bool("accept"), e.cmd.String("digest")
	if accept && approved == "" {
		return nil, output.Errorf(output.CodeUsage, "--accept requires --digest from the approved registration preview")
	}
	w, err := agentWalletMetadata(e)
	if err != nil {
		return nil, err
	}
	wallet := strings.ToLower(w.agent)
	j, err := openJournal(w.profile)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := j.Close(); err != nil {
			slog.New(slog.NewJSONHandler(e.app.Stderr, nil)).WarnContext(e.ctx, "closing registration journal failed", "profile", w.profile.Name)
		}
	}()
	if !accept {
		resp, err := startCompetitionRegistration(e, wallet)
		if err != nil {
			return nil, err
		}
		if resp.GetAlreadyRegistered() != nil {
			return competitionRegistrationResult(wallet, ""), nil
		}
		ch := resp.GetChallenge()
		preview := journal.RegistrationChallenge{Message: ch.Message, Token: ch.ChallengeToken, ExpiresAt: ch.ExpiresAt.AsTime()}
		if !a.Now().Before(preview.ExpiresAt) {
			return nil, output.Errorf(output.CodeUpstream, "StartRegistration returned an expired challenge; run competition register again")
		}
		if err := j.SaveRegistrationChallenge(wallet, preview); err != nil {
			return nil, output.Wrap(output.CodeInternal, err, "saving registration preview")
		}
		digest := competitionConsentDigest(preview.Message)
		next := "oku --profile " + w.profile.Name + " competition register --accept --digest " + digest
		data := map[string]any{"wallet": wallet, "registered": false, "message": preview.Message, "digest": digest, "expiresAt": preview.ExpiresAt, "next": next}
		return &result{data: data, human: func(out io.Writer) {
			fmt.Fprintf(out, "Registering signs this message with the agent wallet %s:\n\n%s\n\nNothing was signed. To accept and register, run: %s\n", wallet, preview.Message, next)
		}}, nil
	}
	ch, err := j.GetRegistrationChallenge(wallet)
	if errors.Is(err, journal.ErrNotFound) {
		return nil, output.Errorf(output.CodeIntentState, "no registration preview for this profile and agent wallet; run competition register without --accept and review the message").WithExec(output.ExecNone)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "loading registration preview")
	}
	expired := func() error {
		return output.Errorf(output.CodeIntentState, "registration preview expired; run competition register without --accept and review the new message").WithExec(output.ExecNone)
	}
	if !a.Now().Before(ch.ExpiresAt) {
		return nil, expired()
	}
	digest := competitionConsentDigest(ch.Message)
	if approved != digest {
		return nil, output.Errorf(output.CodePayloadMismatch, "consent digest does not match the saved preview; review the preview before accepting").WithExec(output.ExecNone)
	}

	ps, walletID, err := a.agentSigner(e, w)
	if err != nil {
		return nil, err
	}
	if err := j.ConsumeRegistrationChallenge(wallet, ch); errors.Is(err, journal.ErrNotFound) {
		return nil, output.Errorf(output.CodeIntentState, "registration preview changed or was used; run competition register without --accept and review the new message").WithExec(output.ExecNone)
	} else if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "consuming registration preview")
	}
	if !a.Now().Before(ch.ExpiresAt) {
		return nil, expired()
	}
	ctx, cancel := e.requestCtx()
	sig, err := ps.PersonalSign(ctx, walletID, ch.Message)
	cancel()
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "signing registration message")
	}
	var done v2.CompleteRegistrationResponse
	signature := hexutil.Encode(sig[:])
	if err := e.competitionCall("CompleteRegistration", &v2.CompleteRegistrationRequest{Wallet: wallet, ChallengeToken: ch.Token, Signature: signature}, &done); err != nil {
		return nil, registrationOutcomeUnknown().WithDetail("upstream_code", output.As(err).Code)
	}
	if done.GetRegistered() != nil {
		return competitionRegistrationResult(wallet, digest), nil
	}
	if f, ok := done.Outcome.(*v2.CompleteRegistrationResponse_Failure); ok {
		msg := strings.NewReplacer(ch.Token, "<redacted>", signature, "<redacted>").Replace(done.FailureMessage)
		return nil, registrationFailure(f.Failure, msg)
	}
	return nil, registrationOutcomeUnknown()
}

// SHA-256 over the exact UTF-8 bytes displayed for review, not the EIP-191 digest.
func competitionConsentDigest(message string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(message)))
}
