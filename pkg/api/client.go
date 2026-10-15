// Package api is the CLI's client for the Oku account API. Calls go through
// connect-go clients generated from the vendored protos (pkg/gen), over an
// HTTP client that refuses redirects, with a single attempt and no retries.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v1/accountv1connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/oku-trade/oku-cli/pkg/gen/gfxcafe/oku/account/v2/accountv2connect"
)

const (
	maxBody            = 16 << 20
	SessionHeader      = "x-oku-account-session"
	SessionTokenHeader = "x-oku-account-session-token"
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
	Token        string
	SessionID    string
	SessionToken string
	// OnUnauthenticated is notified only for a definite server rejection of
	// this session. It must not retry the request (especially mutations).
	OnUnauthenticated func()
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

// Perps calls a PerpsService method through the generated Connect client.
func (c *Client) Perps(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := perpsMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "PerpsService/" + method + " is not supported by this CLI"}
	}
	return c.call(ctx, h, req, resp, authed, false)
}

// Device calls a CliDeviceService method through the generated Connect client.
func (c *Client) Device(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := deviceMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "CliDeviceService/" + method + " is not supported by this CLI"}
	}
	// Device enrollment requests and responses carry codes, poll secrets
	// and tokens; server error text is never surfaced for them.
	return c.call(ctx, h, req, resp, authed, true)
}

// Auth calls an allowlisted AuthService method without exposing sensitive server errors.
func (c *Client) Auth(ctx context.Context, method string, req, resp any, authed bool) error {
	h, ok := authMethods[method]
	if !ok {
		return &Error{Code: "unimplemented", Message: "AuthService/" + method + " is not supported"}
	}
	return c.call(ctx, h, req, resp, authed, true)
}

// call performs exactly one HTTP attempt. There is no retry at any layer:
// the HTTP client refuses redirects and connect-go does not retry unary
// calls. Errors are classified from observed wire evidence.
func (c *Client) call(ctx context.Context, h handler, reqBody, respBody any, authed, sensitive bool) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("api call requires a context deadline")
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	if c.HTTP == nil {
		return errors.New("api client has no HTTP client")
	}
	hdr := http.Header{}
	if c.UserAgent != "" {
		hdr.Set("User-Agent", c.UserAgent)
	}
	if authed {
		switch {
		case c.Token != "" && (c.SessionID != "" || c.SessionToken != ""):
			return &Error{Code: "invalid_argument", Message: "mutually exclusive credentials"}
		case c.SessionID != "" && c.SessionToken != "":
			hdr.Set(SessionHeader, c.SessionID)
			hdr.Set(SessionTokenHeader, c.SessionToken)
		case c.Token != "":
			hdr.Set(DeviceTokenHeader, c.Token)
		default:
			return &Error{Code: "unauthenticated", Message: "credential required"}
		}
	}
	st := &callState{}
	doer := observingDoer{hc: c.HTTP, st: st}
	base := strings.TrimRight(c.BaseURL, "/") + "/connect"
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithReadMaxBytes(maxBody)}
	msg, err := h(ctx,
		accountv2connect.NewPerpsServiceClient(doer, base, opts...),
		accountv2connect.NewCliDeviceServiceClient(doer, base, opts...),
		accountv1connect.NewAuthServiceClient(doer, base, opts...),
		fundsClients{
			swap:    accountv2connect.NewSimpleSwapServiceV2Client(doer, base, opts...),
			bridge:  accountv2connect.NewBridgeServiceClient(doer, base, opts...),
			onchain: accountv2connect.NewOnchainServiceClient(doer, base, opts...),
			token:   accountv2connect.NewTokenServiceClient(doer, base, opts...),
		},
		body, hdr)
	if err != nil {
		classified := classify(ctx, st, err, sensitive)
		if ae, ok := AsError(classified); ok && authed && c.SessionID != "" && ae.Code == "unauthenticated" && !ae.Ambiguous && st.status == http.StatusUnauthorized && c.OnUnauthenticated != nil {
			c.OnUnauthenticated()
		}
		return classified
	}
	if respBody == nil {
		return nil
	}
	out, err := protojson.Marshal(msg)
	if err == nil {
		err = json.Unmarshal(out, respBody)
	}
	if err != nil {
		// Success status but unreadable result: a mutation's effect is unknown.
		return &Error{Code: "invalid_response", Message: "could not decode successful response", HTTP: st.status, Ambiguous: true, cause: err}
	}
	return nil
}

func classify(ctx context.Context, st *callState, err error, sensitive bool) error {
	var re *requestEncodeError
	switch {
	case errors.As(err, &re):
		// Nothing was sent. Schema errors may quote request values, which
		// for device calls can be codes or secrets.
		m := re.Error()
		if sensitive {
			m = "request does not match API schema"
		}
		return &Error{Code: "invalid_argument", Message: m}
	case !st.doCalled:
		// Nothing left the process.
		code := "unavailable"
		if ctx.Err() != nil {
			code = "deadline_exceeded"
		}
		return &Error{Code: code, Message: "request not sent: " + err.Error(), cause: err}
	case st.transportErr != nil:
		return transportError(ctx, st.transportErr)
	case st.readErr != nil:
		return &Error{Code: "transport", Message: "reading response: " + st.readErr.Error(), Ambiguous: true, cause: st.readErr}
	case st.oversized:
		return &Error{Code: "transport", Message: "reading response: response exceeds size limit", Ambiguous: true, cause: err}
	case st.status >= 300 && st.status < 400:
		// Not ambiguous: a redirect response means the request was not
		// handled by the Connect handler.
		return &Error{Code: "redirect_refused", Message: "server responded with a redirect; refusing to follow", HTTP: st.status}
	case st.status != http.StatusOK:
		var ce struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(st.errBody, &ce)
		if ce.Code == "" || !connectCodeRE.MatchString(ce.Code) {
			ce.Code = codeFromHTTP(st.status)
			ce.Message = ""
		}
		switch {
		case sensitive:
			ce.Message = "request failed"
		case ce.Message == "":
			ce.Message = http.StatusText(st.status)
		default:
			ce.Message = Redact(truncateString(ce.Message, 300))
		}
		return &Error{Code: ce.Code, Message: ce.Message, HTTP: st.status, Ambiguous: ambiguousCode(ce.Code)}
	}
	// HTTP 200 whose body could not be read as the expected message, or a
	// failure after the server accepted the request: effect unknown.
	msg := "could not decode successful response"
	if ctx.Err() != nil {
		return &Error{Code: "deadline_exceeded", Message: msg, HTTP: st.status, Ambiguous: true, cause: err}
	}
	return &Error{Code: "invalid_response", Message: msg, HTTP: st.status, Ambiguous: true, cause: err}
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
