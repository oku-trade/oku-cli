package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
