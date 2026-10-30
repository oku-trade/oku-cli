package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/output"
)

const tgTestToken = "987654:TOKEN-must-never-print"

func tgApp(env map[string]string) (*App, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &App{
		Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(""),
		ReadSecret: func(string) ([]byte, error) { return nil, errNoTTY },
		Getenv:     func(k string) string { return env[k] },
		Now:        time.Now, Version: "test",
		Sleep: func(context.Context, time.Duration) error { return nil },
	}, &out, &errb
}

func writeTGConfig(t *testing.T, f *config.File) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("OKU_CONFIG_DIR", dir)
	t.Setenv("OKU_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	if f != nil {
		if err := f.Save(); err != nil {
			t.Fatal(err)
		}
	}
}

func tgProfiles() map[string]*config.Profile {
	return map[string]*config.Profile{
		"main":  {Network: config.NetworkMainnet, APIURL: config.StagingAPIURL, Account: "0x1111111111111111111111111111111111111111"},
		"other": {Network: config.NetworkMainnet, APIURL: config.StagingAPIURL, Account: "0x2222222222222222222222222222222222222222"},
		"bare":  {Network: config.NetworkMainnet, APIURL: config.StagingAPIURL},
	}
}

func TestTelegramServeRefusesWithoutAllowlist(t *testing.T) {
	writeTGConfig(t, &config.File{Version: 1, DefaultProfile: "main", Profiles: tgProfiles()})
	a, out, errb := tgApp(map[string]string{"OKU_TELEGRAM_TOKEN": tgTestToken})
	code := a.Run([]string{"telegram", "serve"})
	if code != output.CodeConfigError.ExitCode() || !strings.Contains(errb.String(), "CONFIG_ERROR") || !strings.Contains(errb.String(), "allowed Telegram user IDs") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
	if strings.Contains(out.String()+errb.String(), tgTestToken) {
		t.Fatal("token printed")
	}
}

func TestTelegramServeRefusesWithoutToken(t *testing.T) {
	writeTGConfig(t, &config.File{Version: 1, DefaultProfile: "main", Profiles: tgProfiles(), Telegram: &config.Telegram{AllowedUserIDs: []int64{42}}})
	a, _, errb := tgApp(map[string]string{})
	code := a.Run([]string{"telegram", "serve", "--allow-users", "7"})
	if code != output.CodeConfigError.ExitCode() || !strings.Contains(errb.String(), "CONFIG_ERROR") || !strings.Contains(errb.String(), "token") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestTelegramServeRejectsArgsAndJSON(t *testing.T) {
	writeTGConfig(t, nil)
	a, _, _ := tgApp(map[string]string{"OKU_TELEGRAM_TOKEN": tgTestToken})
	if code := a.Run([]string{"telegram", "serve", "extra"}); code != output.CodeUsage.ExitCode() {
		t.Fatalf("args: %d", code)
	}
	a, out, _ := tgApp(map[string]string{"OKU_TELEGRAM_TOKEN": tgTestToken})
	if code := a.Run([]string{"telegram", "serve", "--json"}); code != output.CodeUsage.ExitCode() || strings.Contains(out.String(), tgTestToken) {
		t.Fatalf("json: %d %s", code, out.String())
	}
	a, _, errb := tgApp(map[string]string{"OKU_TELEGRAM_TOKEN": tgTestToken})
	if code := a.Run([]string{"telegram", "serve", "--allow-users", "12,abc"}); code != output.CodeInvalidArgument.ExitCode() || strings.Contains(errb.String(), tgTestToken) {
		t.Fatalf("bad users: %d %s", code, errb.String())
	}
}

func tgEnv(a *App, users string, profile string) *env {
	return &env{app: a, cmd: mapFlags{"allow-users": users}, ctx: context.Background(), g: globals{profile: profile, timeout: defaultTimeout}}
}

func TestTelegramSettingsMergeAndPrecedence(t *testing.T) {
	writeTGConfig(t, &config.File{Version: 1, DefaultProfile: "main", Profiles: tgProfiles(),
		Telegram: &config.Telegram{Token: "config-token", AllowedUserIDs: []int64{30, 10}}})
	a, _, _ := tgApp(map[string]string{})
	token, users, launch, err := a.telegramSettings(tgEnv(a, " 20 ,10,", ""))
	if err != nil || token != "config-token" || launch.Profile != "main" {
		t.Fatalf("config token: %q %v %v", token, launch, err)
	}
	if len(users) != 3 || users[0] != 10 || users[1] != 20 || users[2] != 30 {
		t.Fatalf("merged users: %v", users)
	}
	a, _, _ = tgApp(map[string]string{"OKU_TELEGRAM_TOKEN": "env-token", "OKU_PROFILE": "other"})
	token, _, launch, err = a.telegramSettings(tgEnv(a, "", ""))
	if err != nil || token != "env-token" || launch.Profile != "other" {
		t.Fatalf("env precedence: %q %v %v", token, launch, err)
	}
	_, _, launch, err = a.telegramSettings(tgEnv(a, "", "main"))
	if err != nil || launch.Profile != "main" {
		t.Fatalf("--profile precedence: %v %v", launch, err)
	}
	if _, _, _, err = a.telegramSettings(tgEnv(a, "", "bare")); output.As(err).Code != output.CodeAccountRequired {
		t.Fatalf("profile without account: %v", err)
	}
	if _, _, _, err = a.telegramSettings(tgEnv(a, "", "missing")); output.As(err).Code != output.CodeProfileNotFound {
		t.Fatalf("missing profile: %v", err)
	}
}

func TestTelegramSettingsRequiresProfile(t *testing.T) {
	writeTGConfig(t, &config.File{Version: 1, Profiles: tgProfiles()})
	a, _, _ := tgApp(map[string]string{"OKU_TELEGRAM_TOKEN": tgTestToken})
	_, _, _, err := a.telegramSettings(tgEnv(a, "5", ""))
	if output.As(err).Code != output.CodeProfileNotFound || strings.Contains(err.Error(), tgTestToken) {
		t.Fatalf("no profile: %v", err)
	}
}

func TestTelegramConfigValidation(t *testing.T) {
	for _, ids := range [][]int64{{0}, {-3}, {5, 5}} {
		writeTGConfig(t, nil)
		p, _ := config.Path()
		raw, _ := json.Marshal(map[string]any{"version": 1, "profiles": map[string]any{}, "telegram": map[string]any{"allowed_user_ids": ids}})
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Load(); err == nil {
			t.Fatalf("accepted allowed_user_ids %v", ids)
		}
	}
}

// The bot is pinned to the profile resolved at startup: a later change of
// the default profile does not move it.
func TestTelegramBackendPinsProfile(t *testing.T) {
	writeTGConfig(t, &config.File{Version: 1, DefaultProfile: "main", Profiles: tgProfiles()})
	a, _, _ := tgApp(map[string]string{"OKU_TELEGRAM_TOKEN": tgTestToken})
	_, _, launch, err := a.telegramSettings(tgEnv(a, "1", ""))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService("test", &bytes.Buffer{}, func(string) string { return "" })
	svc.AllowMutations(true)
	var seen []string
	svc.app.wrapHandlers = func(m map[string]*handler) {
		for _, h := range m {
			h.run = func(e *env) (*result, error) {
				p, err := e.profile()
				if err != nil {
					return nil, err
				}
				seen = append(seen, p.Name)
				return &result{data: map[string]any{}}, nil
			}
		}
	}
	be := TelegramBackend{S: svc, Launch: launch}
	if _, err := be.Call(context.Background(), "wallet address", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := config.Load()
	f.DefaultProfile = "other"
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Call(context.Background(), "swap submit", map[string]any{"intent": "oku-x", "digest": "d"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "main" || seen[1] != "main" {
		t.Fatalf("profiles used: %v", seen)
	}
}

func TestTelegramBackendPathAllowlist(t *testing.T) {
	writeTGConfig(t, nil)
	svc := NewService("test", &bytes.Buffer{}, func(string) string { return "" })
	svc.AllowMutations(true)
	reached := map[string]bool{}
	svc.app.wrapHandlers = func(m map[string]*handler) {
		for path, h := range m {
			h.run = func(*env) (*result, error) { reached[path] = true; return &result{data: map[string]any{}}, nil }
		}
	}
	be := TelegramBackend{S: svc, Launch: Globals{Profile: "main"}}
	for _, path := range []string{"swap orders watch", "orders prepare", "orders submit", "auth login", "auth logout", "auth status", "profiles use", "profiles add", "positions leverage", "send reconcile", "deposit submit", "telegram serve", "mcp serve", "self-update"} {
		_, err := be.Call(context.Background(), path, map[string]any{}, nil)
		if output.As(err).Code != output.CodeUsage || reached[path] {
			t.Errorf("%s reachable from the bot: %v", path, err)
		}
	}
	cmds := (&App{}).commands()
	for p := range telegramPaths {
		if _, ok := cmds[p]; !ok {
			t.Errorf("%s is not a command", p)
		}
		if !ServiceReads[p] && !ServiceMutations[p] {
			t.Errorf("%s is in neither ServiceReads nor ServiceMutations", p)
		}
	}
	for _, p := range TelegramReads {
		if !ServiceReads[p] {
			t.Errorf("telegram read %s is not a Service read", p)
		}
	}
	if !be.Has("swap pnl") || be.Has("pnl") {
		t.Fatal("Has does not reflect the command table")
	}
}

func TestConfigShowRedactsTelegramToken(t *testing.T) {
	writeTGConfig(t, &config.File{Version: 1, Profiles: tgProfiles(), Telegram: &config.Telegram{Token: tgTestToken, AllowedUserIDs: []int64{9}}})
	a, out, errb := tgApp(map[string]string{})
	if code := a.Run([]string{"config", "show", "--json"}); code != 0 {
		t.Fatalf("config show: %d %s", code, errb.String())
	}
	if strings.Contains(out.String(), tgTestToken) || !strings.Contains(out.String(), `"token": "\u003cset\u003e"`) && !strings.Contains(out.String(), `"token": "<set>"`) {
		t.Fatalf("config show: %s", out.String())
	}
	a, out, _ = tgApp(map[string]string{})
	a.Run([]string{"config", "show"})
	if strings.Contains(out.String(), tgTestToken) {
		t.Fatal("human config show printed the token")
	}
	f, _ := config.Load()
	if f.Telegram.Token != tgTestToken {
		t.Fatal("redaction modified the stored config")
	}
}
