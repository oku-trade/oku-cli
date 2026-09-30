// Package cli implements the oku command tree.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/internal/api"
	"github.com/oku-trade/oku-cli/internal/output"
)

const (
	defaultTimeout = 15 * time.Second
	maxTimeout     = 2 * time.Minute
)

// App holds process I/O so commands are testable end to end.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	// StdinIsTTY enables interactive masked prompts. When false, commands
	// never prompt; they fail with a structured error instead.
	StdinIsTTY bool
	// ReadSecret reads a line from the terminal without echo.
	ReadSecret func(prompt string) ([]byte, error)
	Getenv     func(string) string
	Now        func() time.Time
	// Sleep waits between enrollment polls; overridable in tests.
	Sleep   func(ctx context.Context, d time.Duration) error
	Version string
}

// NewApp returns an App wired to the real process.
func NewApp(version string) *App {
	return &App{
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Stdin:      os.Stdin,
		StdinIsTTY: isTerminal(os.Stdin),
		ReadSecret: readSecretTTY,
		Getenv:     os.Getenv,
		Now:        time.Now,
		Sleep:      sleepCtx,
		Version:    version,
	}
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

// globals are accepted by every command.
type globals struct {
	profile string
	network string
	account string
	json    bool
	timeout time.Duration
}

// env is passed to each command handler.
type env struct {
	app  *App
	g    globals
	fs   *flag.FlagSet
	args []string // positional arguments after the command path
	ctx  context.Context
}

// result is what a command returns on success.
type result struct {
	data  any
	human func(w io.Writer)
}

type handler struct {
	summary string
	flags   func(fs *flag.FlagSet)
	run     func(e *env) (*result, error)
}

func (a *App) commands() map[string]*handler {
	return map[string]*handler{
		"version":          {summary: "Print version information", run: a.cmdVersion},
		"doctor":           {summary: "Check configuration, connectivity and credentials", run: a.cmdDoctor},
		"config path":      {summary: "Print config, credential and state paths", run: a.cmdConfigPath},
		"config show":      {summary: "Show configuration (no secrets)", run: a.cmdConfigShow},
		"profiles list":    {summary: "List profiles", run: a.cmdProfilesList},
		"profiles add":     {summary: "Add a profile: profiles add NAME --account 0x.. [--api-url URL] [--default]", flags: profilesAddFlags, run: a.cmdProfilesAdd},
		"profiles use":     {summary: "Set the default profile: profiles use NAME", run: a.cmdProfilesUse},
		"profiles remove":  {summary: "Remove a profile without credentials: profiles remove NAME", run: a.cmdProfilesRemove},
		"auth setup":       {summary: "Enroll this device with an enrollment code from the Oku website", flags: authSetupFlags, run: a.cmdAuthSetup},
		"auth status":      {summary: "Show local enrollment status (--verify checks with Oku)", flags: authStatusFlags, run: a.cmdAuthStatus},
		"auth revoke":      {summary: "Revoke this device's Oku API token and delete local credentials", run: a.cmdAuthRevoke},
		"markets list":     {summary: "List perp markets", flags: marketFilterFlags, run: a.cmdMarketsList},
		"markets quote":    {summary: "Mark, mid and oracle price: markets quote COIN", run: a.cmdMarketsQuote},
		"markets funding":  {summary: "Funding rate history: markets funding COIN [--since 24h]", flags: fundingFlags, run: a.cmdMarketsFunding},
		"account summary":  {summary: "Balances, margin and builder-fee status", run: a.cmdAccountSummary},
		"positions list":   {summary: "Open positions", run: a.cmdPositionsList},
		"orders list":      {summary: "Open orders", flags: fresh, run: a.cmdOrdersList},
		"orders prepare":   {summary: "Prepare a local order intent (sends nothing)", flags: prepareFlags, run: a.cmdOrdersPrepare},
		"orders submit":    {summary: "Sign and submit a prepared intent: orders submit --intent ID --digest SHA", flags: submitFlags, run: a.cmdOrdersSubmit},
		"orders cancel":    {summary: "Cancel an open order: orders cancel --market COIN --order-id OID", flags: cancelFlags, run: a.cmdOrdersCancel},
		"orders status":    {summary: "Status of an intent: orders status --intent ID", flags: statusFlags, run: a.cmdOrdersStatus},
		"orders reconcile": {summary: "Resolve unknown or in-flight intents with Oku", run: a.cmdOrdersReconcile},
		"orders abandon":   {summary: "Cancel an unsigned server action so it can never execute", flags: statusFlags, run: a.cmdOrdersAbandon},
		"fills list":       {summary: "Recent fills", flags: marketFilterFlags, run: a.cmdFillsList},
		"skills list":      {summary: "List bundled agent skills", run: a.cmdSkillsList},
		"skills install":   {summary: "Install skills: --target jcode|claude|codex or --dir PATH [--force]", flags: skillFlags, run: a.cmdSkillsInstall},
		"skills uninstall": {summary: "Remove installed skills that match the bundled version", flags: skillFlags, run: a.cmdSkillsUninstall},
	}
}

// valueFlags are global flags that consume the next argument.
var valueFlags = map[string]bool{"profile": true, "network": true, "account": true, "timeout": true}

// Run executes the CLI and returns the process exit code.
func (a *App) Run(args []string) int {
	cmds := a.commands()
	path, rest := splitCommand(args, cmds)
	jsonOut := hasJSONFlag(args)
	reqID := output.NewRequestID()
	name := strings.ReplaceAll(path, " ", ".")

	if path == "" || path == "help" {
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			return a.fail(jsonOut, reqID, "help", output.Errorf(output.CodeUsage, "unknown command %q", strings.Join(rest, " ")))
		}
		a.printHelp(cmds)
		return 0
	}
	h := cmds[path]
	if h == nil {
		return a.fail(jsonOut, reqID, name, output.Errorf(output.CodeUsage, "unknown command %q; run 'oku help'", path))
	}
	e := &env{app: a}
	fs := flag.NewFlagSet("oku "+path, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&e.g.profile, "profile", "", "profile name (default: $OKU_PROFILE or config default)")
	fs.StringVar(&e.g.network, "network", "", "network (only mainnet is supported)")
	fs.StringVar(&e.g.account, "account", "", "account address for read commands")
	fs.BoolVar(&e.g.json, "json", false, "write a JSON envelope to stdout")
	fs.DurationVar(&e.g.timeout, "timeout", defaultTimeout, "per-request timeout (max 2m)")
	help := fs.Bool("help", false, "show help")
	if h.flags != nil {
		h.flags(fs)
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		return a.fail(jsonOut, reqID, name, output.Errorf(output.CodeUsage, "%v", err))
	}
	if *help {
		a.printCommandHelp(path, h, fs)
		return 0
	}
	if e.g.timeout <= 0 || e.g.timeout > maxTimeout {
		return a.fail(jsonOut, reqID, name, output.Errorf(output.CodeUsage, "--timeout must be between 1ns and %s", maxTimeout))
	}
	if e.g.network != "" && e.g.network != "mainnet" {
		return a.fail(jsonOut, reqID, name, output.Errorf(output.CodeNetworkUnsupported,
			"network %q is not supported: the Oku API currently serves Hyperliquid mainnet only", e.g.network))
	}
	e.fs, e.args = fs, pos
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.ctx = ctx
	res, err := h.run(e)
	if err != nil {
		return a.fail(e.g.json, reqID, name, err)
	}
	if e.g.json {
		if werr := output.Write(a.Stdout, output.Envelope{RequestID: reqID, Command: name, OK: true, Data: res.data}); werr != nil {
			return 1
		}
		return 0
	}
	if res.human != nil {
		res.human(a.Stdout)
	}
	return 0
}

func (a *App) fail(jsonOut bool, reqID, name string, err error) int {
	oe := output.As(err)
	if jsonOut {
		_ = output.Write(a.Stdout, output.Envelope{RequestID: reqID, Command: name, OK: false, Error: oe})
	} else {
		fmt.Fprintf(a.Stderr, "error: %s: %s\n", oe.Code, oe.Message)
		if hint, ok := oe.Details["hint"].(string); ok {
			fmt.Fprintf(a.Stderr, "hint: %s\n", hint)
		}
	}
	return oe.Code.ExitCode()
}

// splitCommand extracts up to two command words, skipping flags.
func splitCommand(args []string, cmds map[string]*handler) (string, []string) {
	words := []int{}
	for i := 0; i < len(args) && len(words) < 2; i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(a, "-")
			if valueFlags[name] && !strings.Contains(a, "=") {
				i++
			}
			continue
		}
		if len(words) == 0 {
			words = append(words, i)
			continue
		}
		if _, ok := cmds[args[words[0]]+" "+a]; ok {
			words = append(words, i)
		}
		break
	}
	if len(words) == 0 {
		return "", args
	}
	parts := []string{}
	rest := []string{}
	skip := map[int]bool{}
	for _, w := range words {
		parts = append(parts, args[w])
		skip[w] = true
	}
	for i, a := range args {
		if !skip[i] {
			rest = append(rest, a)
		}
	}
	return strings.Join(parts, " "), rest
}

func hasJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "-json" || a == "--json=true" {
			return true
		}
	}
	return false
}

// parseInterspersed allows flags after positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	pos := []string{}
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func (e *env) requestCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(e.ctx, e.g.timeout)
}

func (e *env) positional(n int, usage string) ([]string, error) {
	if len(e.args) != n {
		return nil, output.Errorf(output.CodeUsage, "usage: oku %s", usage)
	}
	return e.args, nil
}

func (e *env) diag(format string, a ...any) {
	fmt.Fprintf(e.app.Stderr, format+"\n", a...)
}

// apiError maps transport/Connect errors to CLI errors for read calls.
func apiError(err error, what string) error {
	ae, ok := api.AsError(err)
	if !ok {
		return output.Wrap(output.CodeInternal, err, what)
	}
	e := &output.Error{Message: what + ": " + ae.Error()}
	switch ae.Code {
	case "not_found":
		e.Code = output.CodeNotFound
	case "invalid_argument", "failed_precondition", "out_of_range":
		e.Code = output.CodeInvalidArgument
	case "unauthenticated":
		e.Code = output.CodeAuthRequired
	case "permission_denied":
		e.Code = output.CodeAuthRequired
		e.Message += " (the device credential lacks permission for this operation)"
	case "resource_exhausted":
		e.Code, e.Retryable = output.CodeRateLimited, true
	case "deadline_exceeded":
		e.Code, e.Retryable = output.CodeUpstreamTimeout, true
	case "unimplemented":
		e.Code = output.CodeAuthUnavailable
		e.Message += " (endpoint not available on this Oku API deployment)"
	case "redirect_refused":
		e.Code = output.CodeUpstream
	default:
		e.Code, e.Retryable = output.CodeUpstream, ae.Ambiguous
	}
	return e
}

var errNoTTY = errors.New("no interactive terminal")
