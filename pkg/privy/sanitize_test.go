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

type leakingTransport struct{ secret string }

func (l leakingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("transport URL contains %s", l.secret)
}

func TestCredentialNeverRenderedInErrors(t *testing.T) {
	const secret = "secret-refresh"
	for _, id := range []string{"trace_" + secret, secret + "_suffix", "trace_" + secret + "_suffix"} {
		t.Run(id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-request-id", id)
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
			}))
			defer server.Close()
			c := &Client{AuthURL: server.URL}
			err := c.request(context.Background(), "POST", "/token", "", []byte(`{"refresh_token":"`+secret+`"}`), false, "", nil)
			assertNoCredential(t, err, secret)
			var pe *Error
			if !errors.As(err, &pe) || pe.RequestID != "" || strings.Contains(pe.Description, secret) || strings.Contains(pe.Code, secret) {
				t.Fatalf("unsafe fields: %+v", pe)
			}
		})
	}
	c := &Client{AuthURL: "https://privy.invalid", HTTP: &http.Client{Transport: leakingTransport{secret}}}
	_, err := c.Refresh(context.Background(), secret)
	assertNoCredential(t, err, secret)
}
func assertNoCredential(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	rendered, marshalErr := json.Marshal(output.Wrap(output.CodeAuthRequired, err, "refresh failed"))
	if marshalErr != nil || strings.Contains(err.Error(), secret) || strings.Contains(string(rendered), secret) {
		t.Fatalf("credential leaked: err=%v json=%s marshal=%v", err, rendered, marshalErr)
	}
}
