package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/telegram"
	urfave "github.com/urfave/cli/v3"
)

func telegramFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "allow-users", Usage: "comma-separated numeric Telegram user IDs allowed to use the bot (merged with telegram.allowed_user_ids in config)"},
	}
}

// TelegramReads, TelegramPrepares and TelegramActions are the only command
// paths the Telegram bot can reach. Everything else, including auth,
// profiles, orders prepare and swap orders watch, is refused before Service.
var (
	TelegramReads = []string{
		"wallet address", "wallet balances", "wallet fund", "swap pnl",
		"account summary", "positions list", "orders list", "fills list",
		"swap orders list", "bridge status",
	}
	TelegramPrepares = []string{"swap prepare", "send prepare", "bridge prepare"}
	TelegramActions  = []string{
		"swap submit", "send submit", "bridge submit", "bridge claim",
		"swap orders create", "swap orders cancel", "orders cancel",
	}
)

var telegramPaths = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range [][]string{TelegramReads, TelegramPrepares, TelegramActions} {
		for _, p := range l {
			m[p] = true
		}
	}
	return m
}()

// TelegramBackend adapts Service to telegram.Backend. Every call runs on the
// profile pinned at startup.
type TelegramBackend struct {
	S      *Service
	Launch Globals
}

// Call implements telegram.Backend.
func (b TelegramBackend) Call(ctx context.Context, path string, opts map[string]any, args []string) (any, error) {
	if !telegramPaths[path] {
		return nil, output.Errorf(output.CodeUsage, "%q is not available from the Telegram bot", path)
	}
	g := Globals{Profile: b.Launch.Profile, Network: b.Launch.Network, Timeout: b.Launch.Timeout}
	if g.Timeout == 0 {
		g.Timeout = defaultTimeout
	}
	// The bot gives slow reads (all-chain balances, bridge status) a longer
	// deadline than the launch --timeout; the per-request timeout follows it.
	if d, ok := ctx.Deadline(); ok {
		if left := time.Until(d); left > g.Timeout {
			g.Timeout = min(left, maxTimeout)
		}
	}
	return b.S.Call(ctx, path, g, opts, args)
}

// Has implements telegram.Backend: whether the command exists in this build.
func (b TelegramBackend) Has(path string) bool {
	_, ok := b.S.app.commands()[path]
	return ok
}

// parseTelegramUsers parses a comma-separated list of positive user IDs.
func parseTelegramUsers(s string) ([]int64, error) {
	var out []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, output.Errorf(output.CodeInvalidArgument, "--allow-users must be comma-separated positive numeric Telegram user IDs")
		}
		out = append(out, id)
	}
	return out, nil
}

// mergeTelegramUsers returns the sorted union of a and b.
func mergeTelegramUsers(a, b []int64) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, l := range [][]int64{a, b} {
		for _, id := range l {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// telegramSettings resolves the token, allowlist and pinned profile. It
// never contacts Telegram, and its errors never contain the token.
func (a *App) telegramSettings(e *env) (token string, users []int64, launch Globals, err error) {
	f, lerr := config.Load()
	if lerr != nil {
		return "", nil, Globals{}, output.Wrap(output.CodeConfigError, lerr, "loading config")
	}
	flagUsers, err := parseTelegramUsers(e.cmd.String("allow-users"))
	if err != nil {
		return "", nil, Globals{}, err
	}
	var cfgUsers []int64
	if f.Telegram != nil {
		cfgUsers = f.Telegram.AllowedUserIDs
		token = f.Telegram.Token
	}
	if env := strings.TrimSpace(a.Getenv("OKU_TELEGRAM_TOKEN")); env != "" {
		token = env
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil, Globals{}, output.Errorf(output.CodeConfigError, "no Telegram bot token").
			WithDetail("hint", "set OKU_TELEGRAM_TOKEN or telegram.token in the config file ('oku config path')")
	}
	users = mergeTelegramUsers(flagUsers, cfgUsers)
	if len(users) == 0 {
		return "", nil, Globals{}, output.Errorf(output.CodeConfigError, "no allowed Telegram user IDs; refusing to start a bot anyone could use").
			WithDetail("hint", "pass --allow-users ID[,ID] or set telegram.allowed_user_ids in the config file")
	}
	p, err := e.profile()
	if err != nil {
		return "", nil, Globals{}, err
	}
	if p.Name == "" {
		return "", nil, Globals{}, output.Errorf(output.CodeProfileNotFound, "no profile selected").
			WithDetail("hint", "pass --profile NAME, set OKU_PROFILE or run 'oku profiles use NAME'")
	}
	if !config.ValidAddress(p.Account) {
		return "", nil, Globals{}, output.Errorf(output.CodeAccountRequired, "profile %q has no account", p.Name)
	}
	return token, users, Globals{Profile: p.Name, Network: e.g.network, Timeout: e.g.timeout}, nil
}

// cmdTelegramServe runs the Telegram bot until interrupted.
func (a *App) cmdTelegramServe(e *env) (*result, error) {
	if len(e.args) != 0 {
		return nil, output.Errorf(output.CodeUsage, "unexpected arguments %v", e.args)
	}
	if e.g.json {
		return nil, output.Errorf(output.CodeUsage, "--json is not supported by telegram serve")
	}
	token, users, launch, err := a.telegramSettings(e)
	if err != nil {
		return nil, err
	}
	svc := NewService(a.Version, a.Stderr, a.Getenv)
	// Mutations are on in the Service; TelegramBackend's path allowlist and
	// the bot's confirm step are the gate.
	svc.AllowMutations(true)
	ctx, stop := signal.NotifyContext(e.ctx, os.Interrupt)
	defer stop()
	e.diag("oku telegram: serving (profile %q, %d allowed users)", launch.Profile, len(users))
	err = telegram.Serve(ctx, telegram.Config{
		Token: token, Backend: TelegramBackend{S: svc, Launch: launch},
		AllowedUserIDs: users, Profile: launch.Profile, Log: a.Stderr, Now: a.Now,
	})
	if err != nil {
		if errors.Is(err, telegram.ErrNoAllowlist) || errors.Is(err, telegram.ErrNoToken) {
			return nil, output.Errorf(output.CodeConfigError, "%v", err)
		}
		return nil, output.Errorf(output.CodeUpstream, "%v", err)
	}
	return &result{}, nil
}
