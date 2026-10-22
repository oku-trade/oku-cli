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
	if v := str("chain"); isChainID(v) {
		r.props["chain_id"] = v
	}
	if v := str("destination-chain"); isChainID(v) {
		r.props["destination_chain"] = v
	}
	if v := str("route"); v != "" && len(v) <= 40 && !strings.HasPrefix(v, "0x") {
		r.props["route"] = strings.ToLower(v)
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
		// TODO(route-reasons): once funds route errors carry structured
		// details (routes_considered, routes_rejected, rejection_reasons as
		// category counts) these fields fill in without further changes.
		if n, ok := intDetail(r.details["routes_considered"]); ok {
			props["routes_considered"] = n
		}
		if n, ok := intDetail(r.details["routes_rejected"]); ok {
			props["routes_rejected"] = n
		}
		if m, ok := r.details["rejection_reasons"].(map[string]int); ok {
			props["route_rejections"] = telemetry.Categories(m)
		}
	}
	host, key := telemetry.Endpoint(a.getenv)
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
