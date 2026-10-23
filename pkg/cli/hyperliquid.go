package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
)

const hyperliquidInfoURL = "https://api.hyperliquid.xyz/info"

// agentMaster reads the approval recorded by Hyperliquid, not a claim from the browser.
func agentMaster(ctx context.Context, endpoint, agent string) (string, error) {
	if endpoint == "" {
		endpoint = hyperliquidInfoURL
	}
	input, _ := json.Marshal(struct {
		Type string `json:"type"`
		User string `json:"user"`
	}{"userRole", agent})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(input))
	if err != nil {
		return "", errors.New("invalid hyperliquid info URL")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("hyperliquid info request failed")
	}
	const limit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return "", errors.New("invalid hyperliquid info response size")
	}
	var role struct {
		Role string `json:"role"`
		Data struct {
			User string `json:"user"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&role); err != nil {
		return "", errors.New("invalid hyperliquid info response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", errors.New("invalid hyperliquid info response trailing data")
	}
	if role.Role != "agent" || !config.ValidAddress(role.Data.User) {
		return "", errors.New("agent approval not found on hyperliquid")
	}
	return role.Data.User, nil
}

// fallbackHIP3Dexes is used when the perpDexs lookup fails.
var fallbackHIP3Dexes = []string{"xyz", "flx", "vntl", "hyna", "km", "abcd", "cash", "para", "mkts", "io"}

// hip3Dexes lists HIP-3 perp dex names from Hyperliquid's public info API.
// OKU_HYPERLIQUID_INFO_URL overrides the endpoint (tests).
func (e *env) hip3Dexes() []string {
	endpoint := e.app.Getenv("OKU_HYPERLIQUID_INFO_URL")
	if endpoint == "" {
		endpoint = hyperliquidInfoURL
	}
	ctx, cancel := e.requestCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(`{"type":"perpDexs"}`)))
	if err != nil {
		return fallbackHIP3Dexes
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fallbackHIP3Dexes
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return fallbackHIP3Dexes
	}
	var dexes []*struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &dexes) != nil {
		return fallbackHIP3Dexes
	}
	var out []string
	for _, d := range dexes {
		if d != nil && d.Name != "" && len(d.Name) <= 16 && !strings.ContainsAny(d.Name, ": \t\n\"\\/") {
			out = append(out, d.Name)
		}
	}
	if len(out) == 0 {
		return fallbackHIP3Dexes
	}
	return out
}

// hlOrderStatus reads one order's status from Hyperliquid's public info API
// ("filled", "canceled", "open", "marginCanceled", ...). It returns "" when
// Hyperliquid does not know the order.
func (e *env) hlOrderStatus(ctx context.Context, user string, oid int64) (string, error) {
	endpoint := e.app.Getenv("OKU_HYPERLIQUID_INFO_URL")
	if endpoint == "" {
		endpoint = hyperliquidInfoURL
	}
	input, _ := json.Marshal(map[string]any{"type": "orderStatus", "user": user, "oid": oid})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(input))
	if err != nil {
		return "", errors.New("invalid hyperliquid info URL")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", errors.New("hyperliquid orderStatus request failed")
	}
	var out struct {
		Status string `json:"status"`
		Order  struct {
			Order struct {
				Oid int64 `json:"oid"`
			} `json:"order"`
			Status string `json:"status"`
		} `json:"order"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("invalid hyperliquid orderStatus response")
	}
	if out.Status != "order" {
		return "", nil
	}
	if out.Order.Order.Oid != oid {
		return "", errors.New("hyperliquid returned a different order")
	}
	return out.Order.Status, nil
}

// hlTerminal maps a Hyperliquid order status to the server state reconcile
// records. Only final statuses count; open or unknown orders stay unresolved.
func hlTerminal(status string) string {
	switch {
	case status == "filled":
		return "filled"
	case status == "canceled" || strings.HasSuffix(status, "Canceled"):
		return "cancelled"
	}
	return ""
}
