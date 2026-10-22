// Package telemetry sends strictly opt-in, allowlisted usage events to
// PostHog. It is off by default, DO_NOT_TRACK always wins, and sending never
// blocks the CLI for more than a short bounded flush or changes its output.
//
// The official posthog-go client is intentionally not used: it starts
// background goroutines with retry/backoff and batching we do not need for
// one event per process. A single bounded HTTPS POST is simpler to reason
// about for the never-block rule and adds no dependencies.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Public, write-only project key and first-party ingestion proxy (same
// project as the Oku web app).
const (
	DefaultKey  = "phc_X9QM3tEAsU8bp71rq0wlODLXWnrX0AiRM9zHzklbiEp"
	DefaultHost = "https://piaget.icarus.tools"
	// FlushTimeout bounds the single send at process exit.
	FlushTimeout = 2 * time.Second
	EventCommand = "cli_command"
	Source       = "oku-cli"
)

// Reason explains why telemetry is on or off.
type Reason string

const (
	ReasonDefault    Reason = "default"
	ReasonConfig     Reason = "config"
	ReasonEnv        Reason = "env OKU_TELEMETRY"
	ReasonDoNotTrack Reason = "env DO_NOT_TRACK"
)

// Resolve applies precedence: DO_NOT_TRACK forces off, then OKU_TELEMETRY,
// then the config value, then off.
func Resolve(getenv func(string) string, configEnabled bool) (bool, Reason) {
	if v := strings.TrimSpace(getenv("DO_NOT_TRACK")); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		return false, ReasonDoNotTrack
	}
	switch strings.ToLower(strings.TrimSpace(getenv("OKU_TELEMETRY"))) {
	case "1", "true", "on", "yes":
		return true, ReasonEnv
	case "0", "false", "off", "no":
		return false, ReasonEnv
	}
	if configEnabled {
		return true, ReasonConfig
	}
	return false, ReasonDefault
}

// AllowedProperties is the exhaustive set of property keys that may be sent.
// Anything else is dropped by Build. Keep docs/telemetry.md in sync.
var AllowedProperties = map[string]string{
	"$lib":                    "always \"oku-cli\"",
	"source":                  "always \"oku-cli\"",
	"command":                 "command path, e.g. \"swap prepare\"",
	"success":                 "true or false",
	"error_code":              "structured error code, e.g. REJECTED (failures only)",
	"retryable":               "whether the error is marked retryable (failures only)",
	"duration_ms":             "command wall time in milliseconds",
	"cli_version":             "oku version",
	"cli_commit":              "oku build commit",
	"os":                      "GOOS, e.g. linux",
	"arch":                    "GOARCH, e.g. amd64",
	"json":                    "whether --json was used",
	"invoked_by":              "mcp, tty or non_tty",
	"api_env":                 "prod, staging, local or custom (never the URL)",
	"chain_id":                "funds commands: source chain id",
	"destination_chain":       "funds commands: destination chain id",
	"route":                   "funds commands: route/bridge the CLI selected, else the --route value",
	"route_forced":            "funds commands: whether --route forced the route",
	"intent":                  "funds commands: whether intent routes were allowed",
	"amount_source":           "funds commands: max or exact (never the amount)",
	"from_kind":               "funds commands: native or erc20 (never the address)",
	"to_kind":                 "funds commands: native or erc20 (never the address)",
	"routes_considered":       "funds REJECTED failures: number of routes considered",
	"routes_rejected":         "funds REJECTED failures: number of routes rejected",
	"route_attempts":          "funds REJECTED failures: number of quote attempts",
	"route_rejections":        "funds REJECTED failures: rejection category counts only (known categories, else other), e.g. amount_mismatch:2,not_answered:1",
	"$process_person_profile": "always false (no person profiles)",
}

var (
	safeToken  = regexp.MustCompile(`^[A-Za-z0-9_.:,\- ]{0,96}$`)
	addressRE  = regexp.MustCompile(`0x[0-9a-fA-F]{40}`)
	decimalRE  = regexp.MustCompile(`^\d+\.\d+$`)
	categoryRE = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
)

// Build filters props to the allowlist and drops any value that is not a
// bool, int, or short safe token string. Strings that look like addresses or
// decimal amounts are dropped as a belt-and-braces guard.
func Build(props map[string]any) map[string]any {
	out := map[string]any{"$lib": Source, "source": Source, "$process_person_profile": false}
	for k, v := range props {
		if _, ok := AllowedProperties[k]; !ok || k == "$lib" || k == "source" || k == "$process_person_profile" {
			continue
		}
		switch x := v.(type) {
		case bool, int, int64:
			out[k] = x
		case string:
			if x == "" || !safeToken.MatchString(x) || addressRE.MatchString(x) || decimalRE.MatchString(x) {
				continue
			}
			out[k] = x
		}
	}
	return out
}

// Categories formats rejection category counts as "a:2,b:1", keeping only
// snake_case category names.
func Categories(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k, n := range counts {
		if categoryRE.MatchString(k) && n > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	return strings.Join(parts, ",")
}

// Endpoint returns host and key, honoring OKU_TELEMETRY_HOST/KEY.
func Endpoint(getenv func(string) string) (host, key string) {
	host, key = DefaultHost, DefaultKey
	if v := getenv("OKU_TELEMETRY_HOST"); v != "" {
		host = v
	}
	if v := getenv("OKU_TELEMETRY_KEY"); v != "" {
		key = v
	}
	return strings.TrimRight(host, "/"), key
}

// Send posts one event with a hard timeout. All errors are returned for
// tests but callers must ignore them.
// UserAgent identifies the CLI to the ingestion proxy. Cloudflare in front of
// the proxy blocks Go's default "Go-http-client" agent with a 403.
var UserAgent = "oku-cli"

func Send(ctx context.Context, host, key, distinctID, event string, props map[string]any, now time.Time) error {
	if !strings.HasPrefix(event, "cli_") {
		return errors.New("event must be prefixed cli_")
	}
	body, err := json.Marshal(map[string]any{
		"api_key": key, "event": event, "distinct_id": distinctID,
		"properties": Build(props), "timestamp": now.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, FlushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/i/v0/e/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	client := &http.Client{Timeout: FlushTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telemetry: status %d", resp.StatusCode)
	}
	return nil
}

func idPath(stateDir string) string { return filepath.Join(stateDir, "telemetry_id") }

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// InstallID reads the random install ID, or "" if none exists.
func InstallID(stateDir string) string {
	raw, err := os.ReadFile(idPath(stateDir))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(raw))
	if !uuidRE.MatchString(id) {
		return ""
	}
	return id
}

// EnsureInstallID returns the install ID, creating a random UUIDv4 if absent.
func EnsureInstallID(stateDir string) (string, error) {
	if id := InstallID(stateDir); id != "" {
		return id, nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(idPath(stateDir), []byte(id+"\n"), 0o600)
}

// RemoveInstallID deletes the install ID so a later enable starts fresh.
func RemoveInstallID(stateDir string) error {
	err := os.Remove(idPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
