package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/telemetry"
)

// telemetryRun collects allowlisted facts about one command invocation. It
// never holds addresses, amounts, IDs or free text.
type telemetryRun struct {
	start   time.Time
	command string // space separated path, empty if unknown
	json    bool
	apiEnv  string
	props   map[string]any
	errCode output.Code
	retry   bool
	details map[string]any
	route   string // route the CLI actually selected, if any
}

// setTelemetryRoute records the route a funds command selected. Safe to call
// with telemetry off; the value is only sent if telemetry is enabled.
func (e *env) setTelemetryRoute(name string) {
	if e == nil || e.app == nil || e.app.tel == nil {
		return
	}
	if v := safeRouteName(name); v != "" {
		e.app.tel.route = v
	}
}

func safeRouteName(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || len(v) > 40 || strings.HasPrefix(v, "0x") {
		return ""
	}
	return v
}

// rejectionCategories are the reason prefixes funds route rejection emits
// for swaps and bridges. Anything else is reported as "other".
var rejectionCategories = map[string]bool{
	"backend_error": true, "not_answered": true, "not_fetched": true, "no_quote": true,
	"intent_excluded": true, "chain_mismatch": true, "not_exact_in": true, "unusable_impact": true,
	"net_usd_nan": true, "amount_mismatch": true, "min_out_invalid": true, "intent_without_typed_data": true,
	"no_router_for_chain": true, "simulation_failed": true, "simulation_no_output": true, "token_mismatch": true,
	"typed_data_on_router_route": true, "approvals_inconsistent": true, "spender_not_router": true,
	"net_usd_invalid": true, "out_amount_invalid": true,
}

// rejectionCounts maps route -> reason to category counts, keeping only the
// allowlisted prefix before the first ':' and never the free text.
func rejectionCounts(routes map[string]string) map[string]int {
	out := map[string]int{}
	for _, reason := range routes {
		cat, _, _ := strings.Cut(reason, ":")
		cat = strings.TrimSpace(cat)
		if !rejectionCategories[cat] {
			cat = "other"
		}
		out[cat]++
	}
	return out
}

// telemetryEnabled loads the global preference and applies env precedence.
func (a *App) telemetryEnabled() (bool, telemetry.Reason, *config.File) {
	f, err := config.Load()
	cfgOn := err == nil && f.Telemetry != nil && f.Telemetry.Enabled
	on, why := telemetry.Resolve(a.getenv, cfgOn)
	return on, why, f
}

func (a *App) getenv(k string) string {
	if a.Getenv == nil {
		return ""
	}
	return a.Getenv(k)
}

// apiEnvName classifies an API origin without ever exposing a custom URL.
func apiEnvName(raw string) string {
	switch config.NormalizeAPIURL(raw) {
	case config.DefaultAPIURL:
		return "prod"
	case config.StagingAPIURL:
		return "staging"
	}
	if u, err := url.Parse(raw); err == nil {
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return "local"
		}
	}
	return "custom"
}

// collectCommand records safe per-command facts from the parsed flags. It
// runs after the handler so it can never affect the command itself.
func (r *telemetryRun) collectCommand(path string, e *env, declared map[string]bool) {
	defer func() { _ = recover() }()
	r.command = path
	r.json = e.g.json
	if p, err := e.profile(); err == nil {
		r.apiEnv = apiEnvName(p.APIURL)
	}
	str := func(name string) string {
		if !declared[name] {
			return ""
		}
		return strings.TrimSpace(e.cmd.String(name))
	}
	// Numeric, to match the web app's chain_id property type in PostHog.
	if v := str("chain"); isChainID(v) {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			r.props["chain_id"] = n
		}
	}
	if v := str("destination-chain"); isChainID(v) {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			r.props["destination_chain"] = n
		}
	}
	if v := safeRouteName(str("route")); v != "" {
		r.props["route"] = v
		r.props["route_forced"] = true
	} else if declared["route"] {
		r.props["route_forced"] = false
	}
	if r.route != "" {
		r.props["route"] = r.route
	}
	if declared["no-intent"] {
		r.props["intent"] = !e.cmd.Bool("no-intent")
	}
	if v := strings.ToLower(str("amount")); v != "" {
		if v == "max" || v == "all" {
			r.props["amount_source"] = "max"
		} else {
			r.props["amount_source"] = "exact"
		}
	}
	tokenKind := func(v string) string {
		if v == "" {
			return ""
		}
		if isNativeToken(v) {
			return "native"
		}
		return "erc20"
	}
	if strings.HasPrefix(path, "swap ") || strings.HasPrefix(path, "bridge ") {
		if k := tokenKind(str("from")); k != "" {
			r.props["from_kind"] = k
		}
		if k := tokenKind(str("to")); k != "" {
			r.props["to_kind"] = k
		}
	}
	if path == "send prepare" {
		if k := tokenKind(str("token")); k != "" {
			r.props["from_kind"] = k
		}
	}
}

func isChainID(v string) bool {
	n, err := strconv.ParseUint(v, 10, 64)
	return err == nil && n > 0 && len(v) <= 12
}

// finishTelemetry sends at most one event, synchronously with a hard
// timeout, and swallows every error. It never writes to stdout.
func (a *App) finishTelemetry(r *telemetryRun, exit int) {
	defer func() { _ = recover() }()
	if r == nil || r.command == "" || strings.HasPrefix(r.command, "telemetry") {
		return
	}
	on, _, _ := a.telemetryEnabled()
	if !on {
		return
	}
	state, err := config.StateDir()
	if err != nil {
		return
	}
	id, err := telemetry.EnsureInstallID(state)
	if err != nil || id == "" {
		return
	}
	props := map[string]any{}
	for k, v := range r.props {
		props[k] = v
	}
	props["command"] = r.command
	props["success"] = exit == 0
	props["duration_ms"] = time.Since(r.start).Milliseconds()
	props["cli_version"] = a.Version
	props["cli_commit"] = buildCommit()
	props["os"] = runtime.GOOS
	props["arch"] = runtime.GOARCH
	props["json"] = r.json
	props["api_env"] = r.apiEnv
	switch {
	case r.command == "mcp serve":
		props["invoked_by"] = "mcp"
	case a.StdinIsTTY:
		props["invoked_by"] = "tty"
	default:
		props["invoked_by"] = "non_tty"
	}
	if exit != 0 && r.errCode != "" {
		props["error_code"] = string(r.errCode)
		props["retryable"] = r.retry
		// REJECTED route errors carry details.routes (route -> reason) and
		// details.attempts; every listed route was rejected.
		if m, ok := r.details["routes"].(map[string]string); ok && len(m) > 0 {
			props["routes_considered"] = len(m)
			props["routes_rejected"] = len(m)
			props["route_rejections"] = telemetry.Categories(rejectionCounts(m))
		}
		if n, ok := intDetail(r.details["attempts"]); ok {
			props["route_attempts"] = n
		}
	}
	host, key := telemetry.Endpoint(a.getenv)
	telemetry.UserAgent = "oku-cli/" + a.Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	_ = telemetry.Send(context.Background(), host, key, id, telemetry.EventCommand, props, time.Now())
}

func intDetail(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// maybeTelemetryHint prints a single stderr line after a successful
// interactive auth login, at most once per install.
func (a *App) maybeTelemetryHint(r *telemetryRun, exit int) {
	defer func() { _ = recover() }()
	if r == nil || r.command != "auth login" || exit != 0 || r.json || !a.StdinIsTTY {
		return
	}
	on, why, f := a.telemetryEnabled()
	if on || why == telemetry.ReasonDoNotTrack || f == nil {
		return
	}
	if f.Telemetry != nil && f.Telemetry.HintShown {
		return
	}
	if f.Telemetry == nil {
		f.Telemetry = &config.Telemetry{}
	}
	f.Telemetry.HintShown = true
	if f.Save() != nil {
		return
	}
	fmt.Fprintln(a.Stderr, "note: oku has opt-in usage telemetry and it is off; run 'oku telemetry enable' to help (see 'oku telemetry status')")
}

func (a *App) setTelemetry(enabled bool) (*result, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	if f.Telemetry == nil {
		f.Telemetry = &config.Telemetry{}
	}
	f.Telemetry.Enabled = enabled
	state, err := config.StateDir()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "state dir")
	}
	if enabled {
		if _, err := telemetry.EnsureInstallID(state); err != nil {
			return nil, output.Wrap(output.CodeConfigError, err, "creating install id")
		}
	} else if err := telemetry.RemoveInstallID(state); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "removing install id")
	}
	if err := f.Save(); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "saving config")
	}
	return a.cmdTelemetryStatus(nil)
}

func (a *App) cmdTelemetryEnable(*env) (*result, error)  { return a.setTelemetry(true) }
func (a *App) cmdTelemetryDisable(*env) (*result, error) { return a.setTelemetry(false) }

func (a *App) cmdTelemetryStatus(*env) (*result, error) {
	on, why, f := a.telemetryEnabled()
	cfgOn := f != nil && f.Telemetry != nil && f.Telemetry.Enabled
	id := ""
	if state, err := config.StateDir(); err == nil {
		id = telemetry.InstallID(state)
	}
	fields := make([]string, 0, len(telemetry.AllowedProperties))
	for k := range telemetry.AllowedProperties {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	data := map[string]any{
		"enabled": on, "reason": string(why), "config_enabled": cfgOn,
		"install_id": id, "event": telemetry.EventCommand, "fields": fields,
	}
	return &result{data: data, human: func(w io.Writer) {
		state := "off"
		if on {
			state = "on"
		}
		fmt.Fprintf(w, "telemetry: %s (%s)\n", state, why)
		if id == "" {
			id = "(none)"
		}
		fmt.Fprintf(w, "install id: %s\n", id)
		fmt.Fprintf(w, "event %s sends only these fields:\n", telemetry.EventCommand)
		for _, k := range fields {
			fmt.Fprintf(w, "  %-24s %s\n", k, telemetry.AllowedProperties[k])
		}
		fmt.Fprintln(w, "never sent: addresses, amounts, tx hashes, intent/order IDs, error messages, credentials, paths. DO_NOT_TRACK=1 always disables.")
	}}, nil
}
