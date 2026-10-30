// Package output implements the versioned JSON envelope and error taxonomy.
//
// Contract: stdout carries exactly one JSON document per invocation when
// --json is set. Diagnostics go to stderr only.
package output

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// SchemaVersion identifies the stdout JSON contract.
const SchemaVersion = "oku.cli/v1"

// Code is a stable, documented error code.
type Code string

const (
	CodeUsage                Code = "USAGE"
	CodeInvalidArgument      Code = "INVALID_ARGUMENT"
	CodeInvalidDecimal       Code = "INVALID_DECIMAL"
	CodeNetworkMismatch      Code = "NETWORK_MISMATCH"
	CodeNetworkUnsupported   Code = "NETWORK_UNSUPPORTED"
	CodeAccountRequired      Code = "ACCOUNT_REQUIRED"
	CodeProfileNotFound      Code = "PROFILE_NOT_FOUND"
	CodeConfigError          Code = "CONFIG_ERROR"
	CodeAuthRequired         Code = "AUTH_REQUIRED"
	CodeAuthUnavailable      Code = "AUTH_UNAVAILABLE"
	CodeSignerUnavailable    Code = "SIGNER_UNAVAILABLE"
	CodeKeychainLocked       Code = "KEYCHAIN_LOCKED"
	CodeKeychainAccessDenied Code = "KEYCHAIN_ACCESS_DENIED"
	CodeKeychainCanceled     Code = "KEYCHAIN_CANCELED"
	CodeSignerRejected       Code = "SIGNER_REJECTED"
	CodeApprovalRequired     Code = "APPROVAL_REQUIRED"
	CodePayloadMismatch      Code = "PAYLOAD_MISMATCH"
	CodeUpstream             Code = "UPSTREAM_ERROR"
	CodeUpstreamTimeout      Code = "UPSTREAM_TIMEOUT"
	CodeRateLimited          Code = "RATE_LIMITED"
	CodeNotFound             Code = "NOT_FOUND"
	CodeOutcomeUnknown       Code = "OUTCOME_UNKNOWN"
	CodeInsufficientGas      Code = "INSUFFICIENT_GAS"
	CodeInsufficientBalance  Code = "INSUFFICIENT_BALANCE"
	CodeIntentConflict       Code = "INTENT_CONFLICT"
	CodeIntentState          Code = "INTENT_STATE"
	CodeRejected             Code = "REJECTED"
	CodeConflict             Code = "FILE_EXISTS"
	CodeInternal             Code = "INTERNAL"
)

// ExitCode maps an error code to the process exit status.
func (c Code) ExitCode() int {
	switch c {
	case CodeUsage, CodeInvalidArgument, CodeInvalidDecimal, CodeAccountRequired,
		CodeProfileNotFound, CodeNetworkMismatch, CodeNetworkUnsupported, CodeConfigError:
		return 2
	case CodeAuthRequired, CodeAuthUnavailable, CodeSignerUnavailable, CodeSignerRejected, CodeApprovalRequired, CodeKeychainLocked, CodeKeychainAccessDenied, CodeKeychainCanceled:
		return 3
	case CodeNotFound:
		return 4
	case CodeUpstream, CodeUpstreamTimeout, CodeRateLimited:
		return 5
	case CodeOutcomeUnknown:
		return 6
	case CodeIntentConflict, CodeIntentState, CodePayloadMismatch, CodeRejected, CodeConflict, CodeInsufficientGas, CodeInsufficientBalance:
		return 7
	default:
		return 1
	}
}

// Execution states reported for mutating commands.
const (
	ExecNone      = "not_submitted"
	ExecUnknown   = "unknown"
	ExecRejected  = "rejected"
	ExecSubmitted = "submitted"
)

// Error is a structured CLI error.
type Error struct {
	Code           Code           `json:"code"`
	Message        string         `json:"message"`
	Retryable      bool           `json:"retryable"`
	ExecutionState string         `json:"execution_state,omitempty"`
	Details        map[string]any `json:"details,omitempty"`
	cause          error
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }
func (e *Error) Unwrap() error { return e.cause }

// Errorf builds an Error.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds an Error with a cause.
func Wrap(code Code, err error, msg string) *Error {
	return &Error{Code: code, Message: msg + ": " + err.Error(), cause: err}
}

// WithDetail adds a detail field.
func (e *Error) WithDetail(k string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}

// As converts any error to *Error.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error(), cause: err}
}

// Envelope is the stdout JSON document.
type Envelope struct {
	SchemaVersion string `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
	Data          any    `json:"data"`
	Error         *Error `json:"error"`
}

// NewRequestID returns a random 128-bit hex id.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Write encodes the envelope to w.
func Write(w io.Writer, env Envelope) error {
	env.SchemaVersion = SchemaVersion
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// WithRetry marks the error retryable.
func (e *Error) WithRetry() *Error {
	e.Retryable = true
	return e
}

// WithExec sets the execution state.
func (e *Error) WithExec(state string) *Error {
	e.ExecutionState = state
	return e
}
