// Package privy implements Privy's OAuth device-grant wallet signing protocol.
package privy

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
)

const DefaultAuthURL = "https://auth.privy.io"
const maxResponse = 1 << 20

var (
	ErrAuthorizationPending = errors.New("authorization pending")
	ErrSlowDown             = errors.New("authorization polling slowed")
	ErrExpiredToken         = errors.New("token expired")
	ErrAccessDenied         = errors.New("access denied")
	ErrUnauthorized         = errors.New("unauthorized")
)

type Client struct {
	AuthURL, AppID string
	HTTP           *http.Client
	Now            func() time.Time
}
type DeviceAuthorization struct {
	DeviceCode, UserCode, VerificationURI, VerificationURIComplete string
	ExpiresIn, Interval                                            int
}
type Tokens struct {
	AccessToken, RefreshToken string
	AccessExpiresAt           time.Time
}
type Wallet struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	ChainType string `json:"chain_type"`
}

// Error contains only safe server metadata. RetryAfter is set for HTTP 429 responses.
type Error struct {
	Status                       int
	Code, Description, RequestID string
	RetryAfter                   string
}

func (e *Error) Error() string {
	message := fmt.Sprintf("privy status %d: %s", e.Status, e.Code)
	if e.Description != "" && e.Description != e.Code {
		message += ": " + e.Description
	}
	if e.RequestID != "" {
		message += " (" + e.RequestID + ")"
	}
	return message
}
func (e *Error) Is(target error) bool {
	switch target {
	case ErrAuthorizationPending:
		return e.Code == "authorization_pending"
	case ErrSlowDown:
		return e.Code == "slow_down"
	case ErrExpiredToken:
		return e.Code == "expired_token"
	case ErrAccessDenied:
		return e.Code == "access_denied"
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized || e.Code == "invalid_grant"
	}
	return false
}
func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c *Client) http() *http.Client {
	var client http.Client
	if c.HTTP != nil {
		client = *c.HTTP // Do not mutate a caller's shared client.
	} else {
		client.Timeout = 20 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
func (c *Client) base() string {
	if c.AuthURL != "" {
		return strings.TrimRight(c.AuthURL, "/")
	}
	return DefaultAuthURL
}
func (c *Client) endpoint(path string) string { return c.base() + "/api/oauth/v2" + path }
func (c *Client) request(ctx context.Context, method, path, token string, body []byte, grant bool, signature string, out any) error {
	return c.requestURL(ctx, method, c.endpoint(path), token, body, grant, signature, out)
}
func (c *Client) requestURL(ctx context.Context, method, endpoint, token string, body []byte, grant bool, signature string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid privy request URL")
	}
	req.Header.Set("privy-app-id", c.AppID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if grant {
		req.Header.Set("privy-grant-type", "device_code")
	}
	if signature != "" {
		req.Header.Set("privy-authorization-signature", signature)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return errors.New("privy transport failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return errors.New("privy response read failed")
	}
	if len(raw) > maxResponse {
		return errors.New("privy response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code, description := safeErrorMetadata(raw, resp.StatusCode)
		// Remote IDs are untrusted and may contain reflected access, refresh,
		// device or authorization secrets. Never render them.
		retryAfter := resp.Header.Get("Retry-After")
		if seconds, err := strconv.ParseUint(retryAfter, 10, 32); err != nil || seconds > 86400 {
			if _, err := http.ParseTime(retryAfter); err != nil || len(retryAfter) > 64 {
				retryAfter = ""
			}
		}
		return &Error{Status: resp.StatusCode, Code: code, Description: description, RetryAfter: retryAfter}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("invalid privy response JSON")
		}
	}
	return nil
}
func encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, errors.New("invalid privy request body")
	}
	return b, nil
}
func (c *Client) StartDeviceAuthorization(ctx context.Context) (*DeviceAuthorization, error) {
	var wire struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := c.request(ctx, "POST", "/device_authorization", "", []byte("{}"), false, "", &wire); err != nil {
		return nil, err
	}
	if wire.DeviceCode == "" || wire.UserCode == "" || wire.ExpiresIn <= 0 {
		return nil, errors.New("incomplete device authorization response")
	}
	return &DeviceAuthorization{wire.DeviceCode, wire.UserCode, wire.VerificationURI, wire.VerificationURIComplete, wire.ExpiresIn, wire.Interval}, nil
}
func defaultSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (c *Client) PollDeviceToken(ctx context.Context, da *DeviceAuthorization, sleep func(context.Context, time.Duration) error) (*Tokens, error) {
	if da == nil || da.DeviceCode == "" || da.ExpiresIn <= 0 {
		return nil, errors.New("invalid device authorization")
	}
	if sleep == nil {
		sleep = defaultSleep
	}
	deadline := c.now().Add(time.Duration(da.ExpiresIn) * time.Second)
	interval := da.Interval
	if interval <= 0 {
		interval = 5
	}
	body, _ := encode(map[string]string{"grant_type": "urn:ietf:params:oauth:grant-type:device_code", "device_code": da.DeviceCode})
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !c.now().Before(deadline) {
			return nil, &Error{Code: "expired_token"}
		}
		if err := sleep(ctx, time.Duration(interval)*time.Second); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !c.now().Before(deadline) {
			return nil, &Error{Code: "expired_token"}
		}
		var result tokenResponse
		err := c.request(ctx, "POST", "/token", "", body, false, "", &result)
		if err == nil {
			return c.tokens(result)
		}
		if errors.Is(err, ErrSlowDown) {
			interval += 5
			continue
		}
		if errors.Is(err, ErrAuthorizationPending) {
			continue
		}
		return nil, err
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (c *Client) tokens(r tokenResponse) (*Tokens, error) {
	if r.AccessToken == "" || r.RefreshToken == "" || r.ExpiresIn <= 0 {
		return nil, errors.New("incomplete privy token response")
	}
	return &Tokens{r.AccessToken, r.RefreshToken, c.now().Add(time.Duration(r.ExpiresIn) * time.Second)}, nil
}
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	if refreshToken == "" {
		return nil, errors.New("missing refresh token")
	}
	body, _ := encode(map[string]string{"grant_type": "refresh_token", "refresh_token": refreshToken})
	var result tokenResponse
	if err := c.request(ctx, "POST", "/token", "", body, false, "", &result); err != nil {
		return nil, err
	}
	return c.tokens(result)
}
func (c *Client) RevokeGrant(ctx context.Context, accessToken string) error {
	if accessToken == "" {
		return errors.New("missing access token")
	}
	var list struct {
		Data []struct {
			ID        string `json:"id"`
			GrantType string `json:"grant_type"`
		} `json:"data"`
	}
	if err := c.request(ctx, "GET", "/grants", accessToken, nil, false, "", &list); err != nil {
		return err
	}
	for _, grant := range list.Data {
		if grant.GrantType != "device_code" || grant.ID == "" {
			continue
		}
		if err := c.request(ctx, "DELETE", "/grants/"+url.PathEscape(grant.ID), accessToken, nil, false, "", nil); err != nil {
			return err
		}
	}
	return nil
}

type Session struct {
	authKey     *ecdsa.PrivateKey
	expiresAt   time.Time
	wallets     []Wallet
	accessToken string
	client      *Client
}

func (s *Session) Wallets() []Wallet    { return append([]Wallet(nil), s.wallets...) }
func (s *Session) ExpiresAt() time.Time { return s.expiresAt }
func parseExpiry(raw json.RawMessage) (time.Time, error) {
	var text string
	if len(raw) == 0 {
		return time.Time{}, errors.New("missing authorization expiry")
	}
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return time.Time{}, errors.New("invalid authorization expiry")
		}
	} else {
		text = string(raw)
	}
	if ms, err := strconv.ParseInt(text, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	t, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, errors.New("invalid authorization expiry")
	}
	return t, nil
}
func parseAuthorizationKey(raw []byte) (*ecdsa.PrivateKey, error) {
	text := strings.TrimSpace(string(raw))
	text = strings.TrimPrefix(text, "wallet-auth:")
	text = strings.TrimSpace(text)
	var der []byte
	if block, _ := pem.Decode([]byte(text)); block != nil {
		der = block.Bytes
	} else {
		var err error
		der, err = base64.StdEncoding.DecodeString(text)
		if err != nil {
			return nil, errors.New("invalid authorization key encoding")
		}
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("invalid authorization key")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		return nil, errors.New("authorization key is not P-256")
	}
	return key, nil
}
func (c *Client) Authenticate(ctx context.Context, accessToken string) (*Session, error) {
	if accessToken == "" {
		return nil, errors.New("missing access token")
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, errors.New("recipient key generation failed")
	}
	der, err := x509.MarshalPKIXPublicKey(ephemeral.PublicKey())
	if err != nil {
		return nil, errors.New("recipient key encoding failed")
	}
	body, _ := encode(map[string]string{"encryption_type": "HPKE", "recipient_public_key": base64.StdEncoding.EncodeToString(der)})
	var result struct {
		EncryptedAuthorizationKey struct {
			EncapsulatedKey string `json:"encapsulated_key"`
			Ciphertext      string `json:"ciphertext"`
		} `json:"encrypted_authorization_key"`
		ExpiresAt json.RawMessage `json:"expires_at"`
		Wallets   []struct {
			ID        string `json:"id"`
			Address   string `json:"address"`
			ChainType string `json:"chain_type"`
		} `json:"wallets"`
	}
	if err := c.request(ctx, "POST", "/wallets/authenticate", accessToken, body, true, "", &result); err != nil {
		return nil, err
	}
	enc, err := base64.StdEncoding.DecodeString(result.EncryptedAuthorizationKey.EncapsulatedKey)
	if err != nil {
		return nil, errors.New("invalid encapsulated key")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(result.EncryptedAuthorizationKey.Ciphertext)
	if err != nil {
		return nil, errors.New("invalid authorization ciphertext")
	}
	sk, err := hpke.NewDHKEMPrivateKey(ephemeral)
	if err != nil {
		return nil, errors.New("invalid recipient key")
	}
	receiver, err := hpke.NewRecipient(enc, sk, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), nil)
	if err != nil {
		return nil, errors.New("authorization decryption failed")
	}
	plaintext, err := receiver.Open(nil, ciphertext)
	if err != nil {
		return nil, errors.New("authorization decryption failed")
	}
	key, err := parseAuthorizationKey(plaintext)
	if err != nil {
		return nil, err
	}
	expiry, err := parseExpiry(result.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if !expiry.After(c.now()) {
		return nil, errors.New("authorization key expired")
	}
	wallets := make([]Wallet, 0, len(result.Wallets))
	for _, w := range result.Wallets {
		wallets = append(wallets, Wallet{w.ID, w.Address, w.ChainType})
	}
	return &Session{authKey: key, expiresAt: expiry, wallets: wallets, accessToken: accessToken, client: c}, nil
}
func (s *Session) SignedRPC(ctx context.Context, walletID string, body any, out any) error {
	if s == nil || s.client == nil || s.authKey == nil || !s.expiresAt.After(s.client.now()) {
		return errors.New("privy authorization unavailable or expired")
	}
	if walletID == "" || strings.ContainsAny(walletID, "/?#") {
		return errors.New("invalid wallet ID")
	}
	wire, err := encode(body)
	if err != nil {
		return err
	}
	var parsed any
	if err := json.Unmarshal(wire, &parsed); err != nil {
		return errors.New("invalid RPC body")
	}
	endpoint := s.client.endpoint("/wallets/" + url.PathEscape(walletID) + "/rpc")
	signed, err := encode(map[string]any{"version": 1, "method": "POST", "url": endpoint, "body": parsed, "headers": map[string]string{"privy-app-id": s.client.AppID}})
	if err != nil {
		return err
	}
	canonical, err := jcs.Transform(signed)
	if err != nil {
		return errors.New("RPC canonicalization failed")
	}
	digest := sha256.Sum256(canonical)
	signature, err := ecdsa.SignASN1(rand.Reader, s.authKey, digest[:])
	if err != nil {
		return errors.New("RPC authorization signing failed")
	}
	return s.client.requestURL(ctx, "POST", endpoint, s.accessToken, wire, true, base64.StdEncoding.EncodeToString(signature), out)
}

type TypedData struct {
	Domain      map[string]any          `json:"domain"`
	Types       map[string][]TypedField `json:"types"`
	PrimaryType string                  `json:"primary_type"`
	Message     map[string]any          `json:"message"`
}
type TypedField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s *Session) SignTypedData(ctx context.Context, walletID string, typedData TypedData) ([65]byte, error) {
	var result struct {
		Data struct {
			Signature string `json:"signature"`
		} `json:"data"`
		Signature string `json:"signature"`
	}
	var signature [65]byte
	err := s.SignedRPC(ctx, walletID, map[string]any{"method": "eth_signTypedData_v4", "params": map[string]any{"typed_data": typedData}}, &result)
	if err != nil {
		return signature, err
	}
	value := result.Data.Signature
	if value == "" {
		value = result.Signature
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil || len(raw) != 65 {
		return signature, errors.New("invalid typed data signature")
	}
	copy(signature[:], raw)
	if signature[64] < 2 {
		signature[64] += 27
	}
	if signature[64] != 27 && signature[64] != 28 {
		return [65]byte{}, errors.New("invalid typed data recovery ID")
	}
	return signature, nil
}
func HyperliquidAgentTypedData(source string, connectionID [32]byte) TypedData {
	return TypedData{Domain: map[string]any{"name": "Exchange", "version": "1", "chainId": 1337, "verifyingContract": "0x0000000000000000000000000000000000000000"}, Types: map[string][]TypedField{"EIP712Domain": {{"name", "string"}, {"version", "string"}, {"chainId", "uint256"}, {"verifyingContract", "address"}}, "Agent": {{"source", "string"}, {"connectionId", "bytes32"}}}, PrimaryType: "Agent", Message: map[string]any{"source": source, "connectionId": "0x" + hex.EncodeToString(connectionID[:])}}
}
