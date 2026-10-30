// Package telegram runs an allowlisted Telegram bot in front of the Oku
// in-process service. It speaks the Telegram Bot API over plain net/http and
// never imports pkg/cli: callers supply a Backend.
//
// The bot token is part of every Bot API URL. No error, log line or message
// produced here ever contains a URL or the token.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Telegram Bot API origin.
const DefaultBaseURL = "https://api.telegram.org"

// pollTimeout is the getUpdates long-poll timeout in seconds. The HTTP client
// timeout must exceed it.
const (
	pollTimeout   = 30
	clientTimeout = 45 * time.Second
	maxRetryAfter = 60 * time.Second
	maxBodyBytes  = 4 << 20
)

// Client is a minimal Telegram Bot API client.
type Client struct {
	base  string
	token string
	hc    *http.Client
	// sleep waits for a 429 retry_after; overridable in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient returns a client for baseURL (DefaultBaseURL when empty).
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		hc: &http.Client{Timeout: clientTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		sleep: sleepCtx,
	}
}

// APIError is a Bot API failure. Description is the Telegram description with
// the token scrubbed; it never contains the request URL.
type APIError struct {
	Method      string
	Status      int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Status, e.Description)
}

// NetError is a transport failure, rebuilt from url.Error.Err so it never
// carries the request URL.
type NetError struct {
	Method  string
	Message string
	Timeout bool
}

func (e *NetError) Error() string { return "telegram " + e.Method + ": " + e.Message }

// User is a Telegram user.
type User struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot,omitempty"`
}

// Chat is a Telegram chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Message is the subset of a Telegram message the bot reads.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text,omitempty"`
}

// CallbackQuery is an inline keyboard tap.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// Update is one getUpdates entry.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Button is an inline keyboard button.
type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// Keyboard is an inline keyboard markup.
type Keyboard struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) scrub(s string) string {
	if c.token != "" {
		s = strings.ReplaceAll(s, c.token, "<token>")
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// do posts one Bot API call. It retries a 429 up to twice after retry_after.
func (c *Client) do(ctx context.Context, method string, req, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("telegram %s: encode request", method)
	}
	for attempt := 0; ; attempt++ {
		err := c.once(ctx, method, body, out)
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests && ae.RetryAfter > 0 && attempt < 2 {
			if serr := c.sleep(ctx, ae.RetryAfter); serr != nil {
				return err
			}
			continue
		}
		return err
	}
}

func (c *Client) once(ctx context.Context, method string, body []byte, out any) error {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return &NetError{Method: method, Message: "invalid request"}
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(hreq)
	if err != nil {
		return c.netError(method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return c.netError(method, err)
	}
	var r apiResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return &APIError{Method: method, Status: resp.StatusCode, Description: "unreadable response"}
	}
	if !r.OK {
		status := r.ErrorCode
		if status == 0 {
			status = resp.StatusCode
		}
		ae := &APIError{Method: method, Status: status, Description: c.scrub(r.Description)}
		if r.Parameters.RetryAfter > 0 {
			ae.RetryAfter = min(time.Duration(r.Parameters.RetryAfter)*time.Second, maxRetryAfter)
		}
		return ae
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return &APIError{Method: method, Status: resp.StatusCode, Description: "unreadable result"}
		}
	}
	return nil
}

func (c *Client) netError(method string, err error) error {
	ne := &NetError{Method: method, Message: "network error"}
	var ue *url.Error
	if errors.As(err, &ue) {
		ne.Timeout = ue.Timeout()
		err = ue.Err
	}
	switch {
	case errors.Is(err, context.Canceled):
		ne.Message = "canceled"
	case errors.Is(err, context.DeadlineExceeded) || ne.Timeout:
		ne.Message, ne.Timeout = "timeout", true
	case err != nil:
		msg := c.scrub(err.Error())
		if strings.Contains(msg, "://") || strings.Contains(msg, "/bot") {
			msg = "network error"
		}
		ne.Message = msg
	}
	return ne
}

// GetUpdates long-polls for updates after offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var out []Update
	err := c.do(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeout, "allowed_updates": []string{"message", "callback_query"},
	}, &out)
	return out, err
}

// SendMessage sends plain text (no parse mode) with an optional keyboard.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, kb *Keyboard) (*Message, error) {
	req := map[string]any{"chat_id": chatID, "text": capText(text), "disable_web_page_preview": true}
	if kb != nil {
		req["reply_markup"] = kb
	}
	var m Message
	if err := c.do(ctx, "sendMessage", req, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// EditMessageText replaces a message's text and keyboard in place. An
// unchanged message is not an error.
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *Keyboard) error {
	req := map[string]any{"chat_id": chatID, "message_id": messageID, "text": capText(text), "disable_web_page_preview": true}
	if kb == nil {
		kb = &Keyboard{InlineKeyboard: [][]Button{}}
	}
	req["reply_markup"] = kb
	err := c.do(ctx, "editMessageText", req, nil)
	var ae *APIError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

// AnswerCallbackQuery clears the client's loading spinner.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id string) error {
	return c.do(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id}, nil)
}

// maxText is the Bot API message length limit.
const maxText = 4096

const truncatedNote = "\n... (truncated; use the CLI for the full output)"

// capText truncates to the Telegram limit and says so. Telegram counts
// UTF-16 code units, so runes outside the BMP count twice.
func capText(s string) string {
	if utf16Len(s) <= maxText {
		return s
	}
	limit := maxText - utf16Len(truncatedNote)
	n, cut := 0, 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if n+w > limit {
			cut = i
			break
		}
		n += w
	}
	return s[:cut] + truncatedNote
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
