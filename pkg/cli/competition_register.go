package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	v2 "github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

// A challenge this close to expiry is not signed: a fresh one is fetched.
const competitionExpiryMargin = 30 * time.Second

func competitionRegisterFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.BoolFlag{Name: "accept", Usage: "accept the consent and sign the registration message with the agent wallet"},
		&urfave.StringFlag{Name: "digest", Usage: "exact consent digest from 'competition register' (required with --accept)"},
	}
}

// registrationFailure maps CompleteRegistration's typed failure to an error.
func registrationFailure(f v2.CompetitionRegistrationFailure, msg string) error {
	name := enumName(f.String(), "COMPETITION_REGISTRATION_FAILURE_")
	text := map[v2.CompetitionRegistrationFailure]string{
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_EXPIRED:                      "the challenge expired; run register again",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_USED:                         "the challenge was already used; run register again",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_INVALID_SIGNATURE:            "the signature does not match the wallet",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_INVALID_CHALLENGE:            "the challenge is invalid; run register again",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_REGISTRATION_CLOSED:          "registration is closed",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_REGISTERED_WITH_OTHER_WALLET: "this wallet's Voltrade account already entered with another wallet",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_BANNED:                       "this wallet's Voltrade account is banned from competitions",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_UNSUPPORTED:                  "this wallet cannot join through Oku; register on Voltrade",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_RATE_LIMITED:                 "too many attempts; retry shortly",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_UNAVAILABLE:                  "registration is unavailable right now; retry shortly",
		v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_REFUSED:                      "registration refused",
	}[f]
	if text == "" {
		text = "registration failed"
	}
	if msg != "" {
		text += ": " + msg
	}
	code, retry := output.CodeRejected, false
	switch f {
	case v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_RATE_LIMITED:
		code, retry = output.CodeRateLimited, true
	case v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_UNAVAILABLE, v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_UNSPECIFIED:
		code, retry = output.CodeUpstream, true
	case v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_EXPIRED, v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_USED, v2.CompetitionRegistrationFailure_COMPETITION_REGISTRATION_FAILURE_INVALID_CHALLENGE:
		retry = true
	}
	return (&output.Error{Code: code, Message: text, Retryable: retry}).WithDetail("failure", name)
}

func startCompetitionRegistration(e *env, wallet string) (*v2.StartRegistrationResponse, error) {
	var resp v2.StartRegistrationResponse
	if err := e.competitionCall("StartRegistration", &v2.StartRegistrationRequest{Wallet: wallet}, &resp); err != nil {
		return nil, err
	}
	if resp.GetAlreadyRegistered() == nil {
		c := resp.GetChallenge()
		if c == nil || c.Message == "" || c.ChallengeToken == "" || c.ExpiresAt == nil {
			return nil, output.Errorf(output.CodeUpstream, "StartRegistration returned no challenge")
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
	resp, err := startCompetitionRegistration(e, wallet)
	if err != nil {
		return nil, err
	}
	if resp.GetAlreadyRegistered() != nil {
		return competitionRegistrationResult(wallet, ""), nil
	}
	ch := resp.GetChallenge()
	if accept && time.Until(ch.ExpiresAt.AsTime()) < competitionExpiryMargin {
		resp, err = startCompetitionRegistration(e, wallet)
		if err != nil {
			return nil, err
		}
		if resp.GetAlreadyRegistered() != nil {
			return competitionRegistrationResult(wallet, ""), nil
		}
		ch = resp.GetChallenge()
	}

	// SHA-256 over the exact UTF-8 bytes displayed for review, not the EIP-191 signing digest.
	sum := sha256.Sum256([]byte(ch.Message))
	digest := hex.EncodeToString(sum[:])
	next := "oku competition register --accept --digest " + digest
	if !accept {
		data := map[string]any{"wallet": wallet, "registered": false, "message": ch.Message, "digest": digest, "expiresAt": tsPtr(ch.ExpiresAt), "next": next}
		return &result{data: data, human: func(out io.Writer) {
			fmt.Fprintf(out, "Registering signs this message with the agent wallet %s:\n\n%s\n\nNothing was signed. To accept and register, run: %s\n", wallet, ch.Message, next)
		}}, nil
	}
	if approved != digest {
		return nil, output.Errorf(output.CodePayloadMismatch, "consent digest does not match. Nothing was signed. Review the current message %q and, if approved, run: %s", ch.Message, next).
			WithExec(output.ExecNone).WithDetail("message", ch.Message).WithDetail("digest", digest)
	}

	ps, walletID, err := a.agentSigner(e, w)
	if err != nil {
		return nil, err
	}
	ctx, cancel := e.requestCtx()
	sig, err := ps.PersonalSign(ctx, walletID, ch.Message)
	cancel()
	if err != nil {
		return nil, output.Wrap(output.CodeSignerUnavailable, err, "signing registration message")
	}
	var done v2.CompleteRegistrationResponse
	if err := e.competitionCall("CompleteRegistration", &v2.CompleteRegistrationRequest{Wallet: wallet, ChallengeToken: ch.ChallengeToken, Signature: hexutil.Encode(sig[:])}, &done); err != nil {
		return nil, err
	}
	if done.GetRegistered() != nil {
		return competitionRegistrationResult(wallet, digest), nil
	}
	if f, ok := done.Outcome.(*v2.CompleteRegistrationResponse_Failure); ok {
		return nil, registrationFailure(f.Failure, done.FailureMessage)
	}
	return nil, output.Errorf(output.CodeUpstream, "CompleteRegistration returned no outcome")
}
