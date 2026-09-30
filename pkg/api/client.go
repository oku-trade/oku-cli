// Package api is a minimal Connect-protocol JSON client for the Oku account
// API (proto: account/proto/gfxcafe/oku/account/v2/perps.proto).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	perpsPrefix  = "/connect/gfxcafe.oku.account.v2.PerpsService/"
	devicePrefix = "/connect/gfxcafe.oku.account.v2.CliDeviceService/"
	maxBody      = 16 << 20
	// DeviceTokenHeader carries the scoped CLI device credential.
	DeviceTokenHeader = "x-oku-device-token"
)

// Client calls Oku Connect endpoints.
type Client struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
	// Token is the device token; sent only when non-empty and only for
	// calls made with Authed.
	Token string
}

// Error is a Connect error or transport failure.
type Error struct {
	Code    string // Connect code, e.g. "not_found", or "transport"
	Message string
	HTTP    int
	// Ambiguous is true when the request may have reached the server and
	// its effect is unknown (timeouts, dropped connections, 5xx).
	Ambiguous bool
	cause     error
}

func (e *Error) Error() string {
	if e.HTTP != 0 {
		return fmt.Sprintf("%s (http %d): %s", e.Code, e.HTTP, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
func (e *Error) Unwrap() error { return e.cause }

// AsError extracts *Error.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Perps calls a PerpsService method.
func (c *Client) Perps(ctx context.Context, method string, req, resp any, authed bool) error {
	return c.call(ctx, perpsPrefix+method, req, resp, authed, false)
}

// Device calls a CliDeviceService method.
func (c *Client) Device(ctx context.Context, method string, req, resp any, authed bool) error {
	// Device enrollment requests and responses carry codes, poll secrets
	// and tokens; server error text is never surfaced for them.
	return c.call(ctx, devicePrefix+method, req, resp, authed, true)
}

func (c *Client) call(ctx context.Context, path string, reqBody, respBody any, authed, sensitive bool) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("api call requires a context deadline")
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if authed {
		if c.Token == "" {
			return &Error{Code: "unauthenticated", Message: "device credential required"}
		}
		req.Header.Set(DeviceTokenHeader, c.Token)
	}
	if dl, ok := ctx.Deadline(); ok {
		if ms := time.Until(dl).Milliseconds(); ms > 0 {
			req.Header.Set("Connect-Timeout-Ms", strconv.FormatInt(ms, 10))
		}
	}
	hc := c.HTTP
	if hc == nil {
		return errors.New("api client has no HTTP client")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return transportError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err == nil && len(raw) > maxBody {
		err = errors.New("response exceeds size limit")
	}
	if err != nil {
		return &Error{Code: "transport", Message: "reading response: " + err.Error(), Ambiguous: true, cause: err}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// Refused redirect. Not ambiguous: a redirect response means the
		// request was not handled by the Connect handler.
		return &Error{Code: "redirect_refused", Message: "server responded with a redirect; refusing to follow", HTTP: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		var ce struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &ce)
		if ce.Code == "" || !connectCodeRE.MatchString(ce.Code) {
			ce.Code = codeFromHTTP(resp.StatusCode)
			ce.Message = ""
		}
		switch {
		case sensitive:
			ce.Message = "request failed"
		case ce.Message == "":
			ce.Message = http.StatusText(resp.StatusCode)
		default:
			ce.Message = Redact(truncateString(ce.Message, 300))
		}
		return &Error{
			Code:      ce.Code,
			Message:   ce.Message,
			HTTP:      resp.StatusCode,
			Ambiguous: ambiguousCode(ce.Code),
		}
	}
	if respBody == nil {
		return nil
	}
	if err := json.Unmarshal(raw, respBody); err != nil {
		// The server returned success but we cannot read the result. For a
		// mutation the effect is unknown, so this is ambiguous.
		return &Error{Code: "invalid_response", Message: "could not decode successful response", HTTP: resp.StatusCode, Ambiguous: true, cause: err}
	}
	return nil
}

var (
	connectCodeRE = regexp.MustCompile(`^[a-z_]{1,32}$`)
	// secretRE matches device tokens and long opaque strings that could be
	// credentials or poll secrets reflected by a server.
	secretRE = regexp.MustCompile(`okud_[A-Za-z0-9_\-]+|[A-Za-z0-9_\-]{32,}`)
)

// Redact removes credential-like substrings. Addresses and hashes are
// 0x-prefixed hex and kept only if shorter than 32 chars after the prefix,
// which errs on the side of redaction.
func Redact(s string) string { return secretRE.ReplaceAllString(s, "[redacted]") }

func truncateString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func transportError(ctx context.Context, err error) error {
	code := "unavailable"
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()) {
		code = "deadline_exceeded"
	}
	return &Error{Code: code, Message: err.Error(), Ambiguous: true, cause: err}
}

// ambiguousCode mirrors isAmbiguousConfirmError in the trade frontend
// (hooks/usePerpsTrading.ts).
func ambiguousCode(code string) bool {
	switch code {
	case "aborted", "canceled", "deadline_exceeded", "internal", "unavailable", "unknown":
		return true
	}
	return false
}

func codeFromHTTP(status int) string {
	switch status {
	case 400:
		return "invalid_argument"
	case 401:
		return "unauthenticated"
	case 403:
		return "permission_denied"
	case 404:
		return "unimplemented"
	case 429:
		return "resource_exhausted"
	case 502, 503, 504:
		return "unavailable"
	}
	return "unknown"
}

// Int64 decodes protojson int64 (string) or JSON number.
type Int64 int64

// UnmarshalJSON implements json.Unmarshaler.
func (i *Int64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*i = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*i = Int64(v)
	return nil
}

// MarshalJSON encodes as a decimal string (protojson form).
func (i Int64) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatInt(int64(i), 10) + `"`), nil
}

// NewHTTPClient returns an HTTP client that never follows redirects, so the
// device token header can never be forwarded to another origin, and that
// has a hard overall timeout as a backstop to the context deadline.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
