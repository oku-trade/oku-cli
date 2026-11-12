package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// DefaultWebOrigin is the Oku web app origin used when OKU_WEB_URL is unset.
const DefaultWebOrigin = "https://oku.trade"

// DefaultHyperliquidInfoURL is the public read-only venue endpoint.
const DefaultHyperliquidInfoURL = "https://api.hyperliquid.xyz/info"

// LendingEndpoints is the validated lending endpoint configuration.
// MorphoIndexer is set only when OKU_MORPHO_API_URL overrides discovery
// from the web app's runtime config at WebOrigin.
type LendingEndpoints struct {
	WebOrigin     *url.URL
	MorphoIndexer *url.URL
}

// LoadLendingEndpoints reads and validates OKU_WEB_URL and
// OKU_MORPHO_API_URL through getenv.
func LoadLendingEndpoints(getenv func(string) string) (LendingEndpoints, error) {
	var out LendingEndpoints
	web := strings.TrimRight(strings.TrimSpace(getenv("OKU_WEB_URL")), "/")
	if web == "" {
		web = DefaultWebOrigin
	}
	u, err := ParseHTTPSURL(web)
	if err != nil {
		return out, fmt.Errorf("OKU_WEB_URL %w", err)
	}
	out.WebOrigin = u
	if v := strings.TrimSpace(getenv("OKU_MORPHO_API_URL")); v != "" {
		if out.MorphoIndexer, err = ParseHTTPSURL(v); err != nil {
			return out, fmt.Errorf("OKU_MORPHO_API_URL %w", err)
		}
	}
	return out, nil
}

// ParseHTTPSURL accepts an https URL without credentials, query or fragment.
func ParseHTTPSURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must be an HTTPS URL without credentials, query or fragment")
	}
	return u, nil
}
