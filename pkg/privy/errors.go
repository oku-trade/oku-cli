package privy

import (
	"encoding/json"
	"net/http"
	"strings"
)

// safeErrorMetadata never returns remote prose. Provider messages can contain
// credentials or signed payloads, so diagnostics are limited to fixed labels.
// These labels describe the response, not whether a transaction was broadcast.
func safeErrorMetadata(raw []byte, status int) (string, string) {
	var payload struct {
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
		Code             string          `json:"code"`
		Message          string          `json:"message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		code := safeErrorCode(http.StatusText(status))
		return code, code
	}
	var errorText string
	var nested struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload.Error, &errorText); err != nil {
		_ = json.Unmarshal(payload.Error, &nested)
	}
	code := payload.Code
	if code == "" {
		code = nested.Code
	}
	if code == "" {
		code = errorText
	}
	if code == "" {
		code = http.StatusText(status)
	}
	code = safeErrorCode(code)
	description := code
	text := strings.ToLower(strings.Join([]string{errorText, payload.ErrorDescription, payload.Message, nested.Message}, "\n"))
	switch {
	case strings.Contains(text, "nonce too low"):
		description = "upstream reports nonce too low; broadcast outcome not determined"
	case strings.Contains(text, "replacement transaction underpriced"):
		description = "upstream reports replacement transaction underpriced; broadcast outcome not determined"
	case strings.Contains(text, "insufficient funds"):
		description = "upstream reports insufficient funds; broadcast outcome not determined"
	case strings.Contains(text, "execution reverted"):
		description = "upstream reports execution reverted; broadcast outcome not determined"
	case strings.Contains(text, "simulation failed"):
		description = "upstream reports simulation failed; broadcast outcome not determined"
	}
	return code, description
}

func safeErrorCode(code string) string {
	switch code {
	case "authorization_pending", "slow_down", "expired_token", "access_denied", "invalid_grant", "device_auth_not_enabled", "policy_violation", "zero_correct_authorization_signatures", "user_session_keys_expired", "no_valid_user_session_keys", "request_expired", "transaction_broadcast_failure", "Too Many Requests", "Unauthorized", "Forbidden", "Not Found":
		return code
	default:
		return "remote_error"
	}
}
