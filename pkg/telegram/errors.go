package telegram

import (
	"errors"

	"github.com/oku-trade/oku-cli/pkg/output"
)

// errorHints are the only error texts the bot sends. Messages, details and
// upstream text of an *output.Error are never forwarded: they can carry file
// paths, URLs or backend responses.
var errorHints = map[output.Code]string{
	output.CodeAuthRequired:         "Session expired or not logged in. Run `oku auth login` on the bot host.",
	output.CodeAuthUnavailable:      "Authentication is unavailable on this Oku deployment.",
	output.CodeSignerUnavailable:    "The agent wallet signer is unavailable. Check `oku auth status` on the bot host.",
	output.CodeSignerRejected:       "The signer rejected the request.",
	output.CodeApprovalRequired:     "Approval required. Finish it with the CLI.",
	output.CodeKeychainLocked:       "The credential store is locked on the bot host.",
	output.CodeKeychainAccessDenied: "The credential store denied access on the bot host.",
	output.CodeKeychainCanceled:     "The credential prompt was canceled on the bot host.",
	output.CodeAccountRequired:      "The bot profile has no enrolled account.",
	output.CodeProfileNotFound:      "The bot profile was not found.",
	output.CodeConfigError:          "Configuration error on the bot host.",
	output.CodeUsage:                "Invalid request.",
	output.CodeInvalidArgument:      "Invalid input. Check the values and try again.",
	output.CodeInvalidDecimal:       "Invalid amount.",
	output.CodeNetworkUnsupported:   "Network not supported.",
	output.CodeNetworkMismatch:      "Network mismatch.",
	output.CodeUpstream:             "Oku or the chain RPC returned an error. Try again later.",
	output.CodeUpstreamTimeout:      "The request timed out. Try again.",
	output.CodeRateLimited:          "Rate limited. Wait a moment and try again.",
	output.CodeNotFound:             "Not found.",
	output.CodeInsufficientGas:      "Not enough native gas in the agent wallet.",
	output.CodeInsufficientBalance:  "Insufficient balance.",
	output.CodeIntentConflict:       "This action already exists or conflicts with another one.",
	output.CodeIntentState:          "The intent is no longer in a state that allows this. Check its status with the CLI.",
	output.CodePayloadMismatch:      "Verification failed: the transaction did not match what was approved. Nothing was sent.",
	output.CodeRejected:             "Rejected: no eligible route or the request was refused.",
	output.CodeOutcomeUnknown:       "Outcome unknown. Do not retry.",
}

const genericError = "Something went wrong. Check the bot host logs."

// userError returns the fixed text shown for err: the error code plus a
// curated hint. Nothing from the error's message or details is included
// except the high_impact reason, which selects a fixed hint.
func userError(err error) string {
	var oe *output.Error
	if !errors.As(err, &oe) {
		return genericError
	}
	if oe.Code == output.CodeRejected {
		if reason, _ := oe.Details["reason"].(string); reason == "high_impact" {
			return "REJECTED: high price impact. Review and override with the CLI if intended."
		}
	}
	hint, ok := errorHints[oe.Code]
	if !ok {
		return genericError
	}
	return string(oe.Code) + ": " + hint
}

// errorCode returns the code of err for logs, never its message.
func errorCode(err error) string {
	var oe *output.Error
	if errors.As(err, &oe) {
		return string(oe.Code)
	}
	var ne *NetError
	if errors.As(err, &ne) {
		return ne.Error()
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Error()
	}
	return "error"
}

func isOutcomeUnknown(err error) bool {
	var oe *output.Error
	return errors.As(err, &oe) && oe.Code == output.CodeOutcomeUnknown
}

func isUpstreamTimeout(err error) bool {
	var oe *output.Error
	return errors.As(err, &oe) && oe.Code == output.CodeUpstreamTimeout
}
