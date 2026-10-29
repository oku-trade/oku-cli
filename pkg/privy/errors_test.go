package privy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/output"
)

func TestStructuredErrorDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, cause string
	}{
		{"broadcast prose does not mask code", `{"error":"Missing or invalid parameters. Details: nonce too low: address private-wallet, tx: 12 state: 13","code":"transaction_broadcast_failure"}`, "transaction_broadcast_failure", "nonce too low"},
		{"message", `{"code":"transaction_broadcast_failure","message":"replacement transaction underpriced: private-wallet"}`, "transaction_broadcast_failure", "replacement transaction underpriced"},
		{"nested", `{"error":{"code":"transaction_broadcast_failure","message":"insufficient funds for private-wallet"}}`, "transaction_broadcast_failure", "insufficient funds"},
		{"simulation is diagnostic only", `{"error":"simulation failed: private-wallet"}`, "remote_error", "simulation failed"},
		{"revert", `{"message":"execution reverted: private-wallet"}`, "remote_error", "execution reverted"},
		{"oauth", `{"error":"invalid_grant","error_description":"private-wallet"}`, "invalid_grant", ""},
		{"unrecognized", `{"code":"private-wallet","message":"private-wallet"}`, "remote_error", ""},
		{"explicit code wins", `{"code":"transaction_broadcast_failure","error":"invalid_grant"}`, "transaction_broadcast_failure", ""},
		{"malformed", `{"code":"transaction_broadcast_failure","error":`, "remote_error", ""},
		{"html", `<html>private-wallet</html>`, "remote_error", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-request-id", "private-wallet")
				w.Header().Set("privy-request-id", "private-wallet")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := &Client{AuthURL: server.URL}
			err := client.request(context.Background(), "POST", "/wallets/test/rpc", "private-wallet", []byte(`{}`), true, "private-wallet", nil)
			var upstream *Error
			if !errors.As(err, &upstream) || upstream.Code != tc.code || upstream.Status != 400 || upstream.RequestID != "" {
				t.Fatalf("unexpected error metadata: %+v", upstream)
			}
			if tc.cause == "" && upstream.Description != tc.code || tc.cause != "" && !strings.Contains(upstream.Description, tc.cause) {
				t.Fatalf("unexpected safe description: %q", upstream.Description)
			}
			if strings.Contains(err.Error(), "()") || !strings.Contains(err.Error(), tc.code) || tc.cause != "" && !strings.Contains(err.Error(), tc.cause) {
				t.Fatalf("unexpected rendered error: %v", err)
			}
			assertNoCredential(t, err, "private-wallet")
			wire, _ := json.Marshal(output.Wrap(output.CodeOutcomeUnknown, err, "never resubmit"))
			if !strings.Contains(string(wire), tc.code) {
				t.Fatalf("missing diagnostic in JSON: %s", wire)
			}
		})
	}
}

func TestOnlyFixedErrorLabelsEscape(t *testing.T) {
	const secret = "secret-access-refresh-device-signature"
	for _, field := range []string{"error", "message", "error_description"} {
		for _, cause := range []string{"nonce too low", "replacement transaction underpriced", "insufficient funds", "execution reverted", "simulation failed"} {
			body, _ := json.Marshal(map[string]string{"code": "transaction_broadcast_failure", field: secret + " " + cause + " " + secret})
			code, description := safeErrorMetadata(body, 400)
			err := &Error{Status: 400, Code: code, Description: description}
			assertNoCredential(t, err, secret)
			if !strings.Contains(description, cause) || !strings.Contains(description, "broadcast outcome not determined") {
				t.Fatalf("missing safe diagnostic: %+v", err)
			}
		}
	}
}

func TestSafeHTTPErrorFallback(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429} {
		for _, body := range []string{"", "{}", "<html>secret</html>", `{"code":`} {
			code, description := safeErrorMetadata([]byte(body), status)
			want := http.StatusText(status)
			if code != want || description != want {
				t.Fatalf("status=%d body=%q code=%q description=%q", status, body, code, description)
			}
		}
	}
}
