package config

import (
	"fmt"
	"net/url"
	"strconv"
)

// maxChainID bounds plausible EVM chain IDs (2^53, the largest integer a JSON
// number carries exactly).
const maxChainID = 1 << 53

// ValidChainID reports whether id is a plausible EVM chain ID.
func ValidChainID(id uint64) bool { return id > 0 && id < maxChainID }

// ParseChainID parses a decimal chain ID in canonical form (no sign, no
// leading zeros) so a chain has exactly one key in rpc_urls.
func ParseChainID(s string) (uint64, error) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil || !ValidChainID(id) || strconv.FormatUint(id, 10) != s {
		return 0, fmt.Errorf("chain ID %q must be a positive decimal integer below 2^53", s)
	}
	return id, nil
}

// ValidateRPCURL requires an http or https URL with a host. The CLI speaks
// JSON-RPC over HTTP POST only, so websocket schemes are not accepted.
func ValidateRPCURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("RPC URL is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("RPC URL must use http or https")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("RPC URL needs a host")
	}
	return nil
}

// RedactRPCURL keeps only scheme and host. RPC URLs commonly carry API keys in
// the path, query or userinfo, so anything beyond the host is hidden.
func RedactRPCURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return "<redacted>"
	}
	out := u.Scheme + "://" + u.Host
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		out += "/<redacted>"
	}
	return out
}

// validateRPCURLs checks every rpc_urls entry, naming the offending key.
func validateRPCURLs(m map[string]string) error {
	for k, v := range m {
		if _, err := ParseChainID(k); err != nil {
			return fmt.Errorf("rpc_urls key %q: %w", k, err)
		}
		if err := ValidateRPCURL(v); err != nil {
			return fmt.Errorf("rpc_urls[%q]: %w", k, err)
		}
	}
	return nil
}

// SetRPCURL validates and stores the RPC URL for chain.
func (f *File) SetRPCURL(chain uint64, rawURL string) error {
	if !ValidChainID(chain) {
		return fmt.Errorf("chain ID %d must be a positive integer below 2^53", chain)
	}
	if err := ValidateRPCURL(rawURL); err != nil {
		return err
	}
	if f.RPCURLs == nil {
		f.RPCURLs = map[string]string{}
	}
	f.RPCURLs[strconv.FormatUint(chain, 10)] = rawURL
	return nil
}

// UnsetRPCURL removes the stored RPC URL and reports whether one existed.
func (f *File) UnsetRPCURL(chain uint64) bool {
	k := strconv.FormatUint(chain, 10)
	_, ok := f.RPCURLs[k]
	delete(f.RPCURLs, k)
	return ok
}
