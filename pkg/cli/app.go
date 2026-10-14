// Package cli implements the oku command tree.
package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

const (
	defaultTimeout = 15 * time.Second
	maxTimeout     = 2 * time.Minute
)

//go:embed bash_completion.sh
var bashCompletion string

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
	OpenURL func(string) error
	Version string

	// wrapHandlers, when set by tests, may replace handlers after the
	// command table is built (flags are preserved by the caller).
	wrapHandlers func(map[string]*handler)
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
		OpenURL: func(url string) error {
			command := "xdg-open"
			if runtime.GOOS == "darwin" {
				command = "open"
			}
			cmd := exec.Command(command, url)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait() }()
			return nil
		},
		Version: version,
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
	cmd  flagSource
	args []string // positional arguments after the command path
	ctx  context.Context
	// pass caches an interactively entered passphrase for this one command
	// invocation, so unlock and the signing boundary prompt only once.
	pass []byte
}

// flagSource is the flag view handlers read. *urfave.Command satisfies it
// for the executable; Service supplies a typed map for in-process callers
// such as the MCP server so both share one implementation.
type flagSource interface {
	String(name string) string
	Bool(name string) bool
	Int(name string) int
	Int64(name string) int64
	Duration(name string) time.Duration
}

// result is what a command returns on success.
type result struct {
	data  any
	human func(w io.Writer)
}

type handler struct {
	summary string
	flags   func() []urfave.Flag
	run     func(e *env) (*result, error)
}

func (a *App) commands() map[string]*handler {
	m := a.commandTable()
	if a.wrapHandlers != nil {
		a.wrapHandlers(m)
	}
	return m
}

func (a *App) commandTable() map[string]*handler {
	return map[string]*handler{
		"version":            {summary: "Print version information", run: a.cmdVersion},
		"doctor":             {summary: "Check configuration, connectivity and credentials", run: a.cmdDoctor},
		"config path":        {summary: "Print config, credential and state paths", run: a.cmdConfigPath},
		"config show":        {summary: "Show configuration (no secrets)", run: a.cmdConfigShow},
		"profiles list":      {summary: "List profiles", run: a.cmdProfilesList},
		"profiles add":       {summary: "Add a profile: profiles add NAME --account 0x.. [--api-url URL] [--default]", flags: profilesAddFlags, run: a.cmdProfilesAdd},
		"profiles use":       {summary: "Set the default profile: profiles use NAME", run: a.cmdProfilesUse},
		"profiles remove":    {summary: "Remove a profile without credentials: profiles remove NAME", run: a.cmdProfilesRemove},
		"auth login":         {summary: "Authorize a Privy embedded-wallet agent on the Oku website", flags: authLoginFlags, run: a.cmdAuthLogin},
		"auth status":        {summary: "Show Privy agent credential (--verify checks approval)", flags: authStatusFlags, run: a.cmdAuthStatus},
		"drafts repair":      {summary: "Repair malformed order-draft JSON from --stdin into a reviewable candidate (local only; never prepares or submits)", flags: draftFlags, run: a.cmdDraftsRepair},
		"mcp serve":          {summary: "Run the MCP server on stdio (reads only unless --allow-mutations)", flags: mcpFlags, run: a.cmdMCPServe},
		"auth logout":        {summary: "Log out and delete the local Privy agent credential", run: a.cmdAuthLogout},
		"markets list":       {summary: "List perp markets", flags: marketFilterFlags, run: a.cmdMarketsList},
		"markets quote":      {summary: "Mark, mid and oracle price: markets quote COIN", run: a.cmdMarketsQuote},
		"markets funding":    {summary: "Funding rate history: markets funding COIN [--since 24h]", flags: fundingFlags, run: a.cmdMarketsFunding},
		"account summary":    {summary: "Balances, margin and builder-fee status", run: a.cmdAccountSummary},
		"positions leverage": {summary: "Prepare a leverage/margin-mode change (no submission): --market COIN --leverage N --mode cross|isolated", flags: leverageFlags, run: a.cmdPositionsLeverage},
		"positions margin":   {summary: "Prepare an isolated margin add/remove (no submission): --market COIN --amount USDC", flags: marginFlags, run: a.cmdPositionsMargin},
		"positions list":     {summary: "Open positions", run: a.cmdPositionsList},
		"orders list":        {summary: "Open orders", flags: fresh, run: a.cmdOrdersList},
		"orders prepare":     {summary: "Prepare a local order intent (no exchange submission; may read market data)", flags: prepareFlags, run: a.cmdOrdersPrepare},
		"orders submit":      {summary: "Sign and submit a prepared intent: orders submit --intent ID --digest SHA", flags: submitFlags, run: a.cmdOrdersSubmit},
		"orders cancel":      {summary: "Cancel an open order: orders cancel --market COIN --order-id OID", flags: cancelFlags, run: a.cmdOrdersCancel},
		"orders status":      {summary: "Status of an intent: orders status --intent ID", flags: statusFlags, run: a.cmdOrdersStatus},
		"orders reconcile":   {summary: "Resolve unknown or in-flight intents with Oku", run: a.cmdOrdersReconcile},
		"orders abandon":     {summary: "Cancel an unsigned server action so it can never execute", flags: statusFlags, run: a.cmdOrdersAbandon},
		"fills list":         {summary: "Recent fills", flags: marketFilterFlags, run: a.cmdFillsList},
		"docs list":          {summary: "List documentation bundled in this binary (offline)", run: a.cmdDocsList},
		"docs show":          {summary: "Print a bundled document: docs show NAME", run: a.cmdDocsShow},
		"docs search":        {summary: "Search bundled documentation: docs search TERMS... [--limit N]", flags: docsSearchFlags, run: a.cmdDocsSearch},
		"docs commands":      {summary: "List commands of this binary; --markdown prints the full reference", flags: docsCommandsFlags, run: a.cmdDocsCommands},
		"skills list":        {summary: "List bundled agent skills", run: a.cmdSkillsList},
		"skills install":     {summary: "Install skills: --target jcode|claude|codex or --dir PATH [--force]", flags: skillFlags, run: a.cmdSkillsInstall},
		"skills uninstall":   {summary: "Remove installed skills that match the bundled version", flags: skillFlags, run: a.cmdSkillsUninstall},
	}
}

// Run executes the CLI and returns the process exit code.
func (a *App) Run(args []string) int {
	reqID := output.NewRequestID()
	jsonOut := hasJSONFlag(args)
	root := a.commandTree(reqID, jsonOut)
	var unknown string
	unknownCommand := func(_ context.Context, cmd *urfave.Command, name string) {
		unknown = strings.TrimPrefix(cmd.FullName()+" "+name, "oku ")
	}
	root.CommandNotFound = unknownCommand
	for _, group := range root.Commands {
		group.CommandNotFound = unknownCommand
	}
	err := root.Run(context.Background(), append([]string{"oku"}, args...))
	if unknown != "" {
		return a.fail(jsonOut, reqID, strings.ReplaceAll(unknown, " ", "."), output.Errorf(output.CodeUsage, "unknown command %q; run 'oku help'", unknown))
	}
	if err == nil {
		return 0
	}
	var failure *commandFailure
	if errors.As(err, &failure) {
		if failure.name == "oku" {
			failure.name = a.commandName(args)
		}
		return a.fail(failure.json, reqID, failure.name, failure.err)
	}
	return a.fail(jsonOut, reqID, a.commandName(args), output.Errorf(output.CodeUsage, "%v", err))
}

type commandFailure struct {
	name string
	json bool
	err  error
}

func (f *commandFailure) Error() string { return f.err.Error() }

func (a *App) commandTree(reqID string, jsonOut bool) *urfave.Command {
	cmds := a.commands()
	root := &urfave.Command{
		Name: "oku", Usage: "Oku Agent for Hyperliquid mainnet perps",
		Writer: a.Stdout, ErrWriter: io.Discard, HideVersion: true,
		OnUsageError: func(_ context.Context, cmd *urfave.Command, err error, _ bool) error {
			return &commandFailure{name: strings.ReplaceAll(strings.TrimPrefix(cmd.FullName(), "oku "), " ", "."), json: jsonOut, err: output.Errorf(output.CodeUsage, "%v", err)}
		},
		EnableShellCompletion: true,
		ConfigureShellCompletionCommand: func(c *urfave.Command) {
			c.Hidden = false
			c.Usage = "Print a shell completion script (bash, zsh or fish)"
			c.Description = "Print a shell completion script. Source it in bash or zsh; fish can pipe it to source. Completion never contacts Oku or unlocks credentials."
			for _, child := range c.Commands {
				if child.Name == "bash" {
					child.Action = func(_ context.Context, cmd *urfave.Command) error {
						_, err := io.WriteString(cmd.Root().Writer, bashCompletion)
						return err
					}
				}
				if child.Name == "pwsh" {
					child.Hidden = true
				}
			}
		},
		Flags: []urfave.Flag{
			&urfave.StringFlag{Name: "profile", Usage: "profile name (default: $OKU_PROFILE or config default)"},
			&urfave.StringFlag{Name: "network", Usage: "network (only mainnet is supported)"},
			&urfave.StringFlag{Name: "account", Usage: "account address for read commands"},
			&urfave.BoolFlag{Name: "json", Usage: "write a JSON envelope to stdout"},
			&urfave.DurationFlag{Name: "timeout", Value: defaultTimeout, Usage: "per-request timeout (max 2m)"},
		},
	}
	names := sortedCommandNames(cmds)
	groups := map[string]*urfave.Command{}
	for _, path := range names {
		h := cmds[path]
		parts := strings.Split(path, " ")
		leaf := &urfave.Command{Name: parts[len(parts)-1], Usage: h.summary, HideHelpCommand: true, OnUsageError: root.OnUsageError}
		if h.flags != nil {
			leaf.Flags = h.flags()
		}
		leaf.Action = func(ctx context.Context, cmd *urfave.Command) error {
			name := strings.ReplaceAll(path, " ", ".")
			e := &env{app: a, cmd: newCheckedFlags(cmd, leaf.Flags), args: cmd.Args().Slice(), ctx: ctx}
			e.g = globals{profile: cmd.String("profile"), network: cmd.String("network"), account: cmd.String("account"), json: cmd.Bool("json") || jsonOut, timeout: cmd.Duration("timeout")}
			limit := maxTimeout
			if name == "auth.login" {
				limit = 15 * time.Minute
			}
			if e.g.timeout <= 0 || e.g.timeout > limit {
				return &commandFailure{name, e.g.json, output.Errorf(output.CodeUsage, "--timeout must be between 1ns and %s", limit)}
			}
			if e.g.network != "" && e.g.network != "mainnet" {
				return &commandFailure{name, e.g.json, output.Errorf(output.CodeNetworkUnsupported, "network %q is not supported: the Oku API currently serves Hyperliquid mainnet only", e.g.network)}
			}
			res, err := runChecked(h, e)
			if err != nil {
				return &commandFailure{name, e.g.json, err}
			}
			if e.g.json {
				return output.Write(a.Stdout, output.Envelope{RequestID: reqID, Command: name, OK: true, Data: res.data})
			}
			if res.human != nil {
				res.human(a.Stdout)
			}
			return nil
		}
		if len(parts) == 1 {
			root.Commands = append(root.Commands, leaf)
			continue
		}
		group := groups[parts[0]]
		if group == nil {
			group = &urfave.Command{Name: parts[0], Usage: parts[0] + " commands", OnUsageError: root.OnUsageError}
			group.Action = func(_ context.Context, cmd *urfave.Command) error {
				name := strings.TrimPrefix(cmd.FullName(), "oku ")
				if cmd.Args().Len() > 0 {
					unknown := name + " " + cmd.Args().First()
					return &commandFailure{name: strings.ReplaceAll(unknown, " ", "."), json: jsonOut, err: output.Errorf(output.CodeUsage, "unknown command %q; run 'oku help'", unknown)}
				}
				return &commandFailure{name: name, json: jsonOut, err: output.Errorf(output.CodeUsage, "missing subcommand for %s", cmd.FullName())}
			}
			groups[parts[0]] = group
			root.Commands = append(root.Commands, group)
		}
		group.Commands = append(group.Commands, leaf)
	}
	return root
}

func sortedCommandNames(cmds map[string]*handler) []string {
	names := make([]string, 0, len(cmds))
	for name := range cmds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a *App) commandName(args []string) string {
	commands := a.commands()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--profile" || arg == "--network" || arg == "--account" || arg == "--timeout" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if i+1 < len(args) {
			if _, ok := commands[arg+" "+args[i+1]]; ok {
				return arg + "." + args[i+1]
			}
		}
		if _, ok := commands[arg]; ok {
			return arg
		}
	}
	return "oku"
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

func hasJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "-json" || a == "--json=true" {
			return true
		}
	}
	return false
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
		e.Message += " (the Oku session lacks permission for this operation)"
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
