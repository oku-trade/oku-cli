package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
)

func autoUpdateApp(t *testing.T, now *time.Time, env map[string]string) (*App, *int) {
	t.Helper()
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("OKU_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	spawns := 0
	oldSpawn, oldExe := spawnAutoUpdate, selfExecutable
	spawnAutoUpdate = func(string, string) error { spawns++; return nil }
	selfExecutable = func() (string, error) { return "/fake/oku", nil }
	t.Cleanup(func() { spawnAutoUpdate, selfExecutable = oldSpawn, oldExe })
	a := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader(""), Version: "v0.0.1",
		Getenv: func(k string) string { return env[k] }, Now: func() time.Time { return *now }}
	return a, &spawns
}

func TestAutoUpdateOffByDefault(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, spawns := autoUpdateApp(t, &now, nil)
	a.Run([]string{"config", "path"})
	if *spawns != 0 {
		t.Fatal("spawned without opt-in")
	}
}

func TestAutoUpdateThrottledByInterval(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, spawns := autoUpdateApp(t, &now, map[string]string{})
	if code := a.Run([]string{"autoupdate", "enable", "--interval", "6h", "--json"}); code != 0 {
		t.Fatalf("enable: %d", code)
	}
	if *spawns != 0 {
		t.Fatal("autoupdate commands must not trigger a check")
	}
	a.Run([]string{"config", "path"})
	if *spawns != 1 {
		t.Fatalf("first run should spawn, got %d", *spawns)
	}
	now = now.Add(5 * time.Hour)
	a.Run([]string{"config", "path"})
	a.Run([]string{"config", "path"})
	if *spawns != 1 {
		t.Fatalf("spawned before interval elapsed: %d", *spawns)
	}
	now = now.Add(2 * time.Hour)
	a.Run([]string{"config", "path"})
	if *spawns != 2 {
		t.Fatalf("should spawn after interval: %d", *spawns)
	}
	f, _ := config.Load()
	if f.AutoUpdate == nil || !f.AutoUpdate.Enabled || !f.AutoUpdate.Skills || f.AutoUpdate.Interval != "6h0m0s" {
		t.Fatalf("config %+v", f.AutoUpdate)
	}
}

func TestAutoUpdateEnvAndDisable(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	env := map[string]string{autoUpdateOffEnv: "1"}
	a, spawns := autoUpdateApp(t, &now, env)
	a.Run([]string{"autoupdate", "enable"})
	a.Run([]string{"config", "path"})
	env[autoUpdateOffEnv] = ""
	env[autoUpdateChildEnv] = "1"
	a.Run([]string{"config", "path"})
	if *spawns != 0 {
		t.Fatal("env overrides ignored")
	}
	delete(env, autoUpdateChildEnv)
	a.Run([]string{"autoupdate", "disable"})
	a.Run([]string{"config", "path"})
	if *spawns != 0 {
		t.Fatal("spawned after disable")
	}
}

func TestAutoUpdateRejectsShortInterval(t *testing.T) {
	now := time.Now()
	a, _ := autoUpdateApp(t, &now, nil)
	if code := a.Run([]string{"autoupdate", "enable", "--interval", "5m", "--json"}); code == 0 {
		t.Fatal("accepted 5m interval")
	}
}

func TestAutoUpdateRunUpdatesAndRecords(t *testing.T) {
	exe := installedExe(t)
	f := &fakeRelease{tag: "v0.0.2", archive: makeTarGz(t, []tarEntry{{stem("v0.0.2") + "/oku", fakeBinary("v0.0.2"), 0o755}})}
	f.sums = goodSums(f.tag)
	f.token = true
	srv := f.server(t)
	t.Setenv("OKU_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("OKU_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("HOME", t.TempDir()) // no installed skills to refresh
	old := selfExecutable
	selfExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExecutable = old })
	var out bytes.Buffer
	a := &App{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader(""), Version: "v0.0.1",
		Getenv: func(k string) string {
			return map[string]string{"OKU_GITHUB_API_URL": srv.URL, "GITHUB_TOKEN": "test-token"}[k]
		}}
	if code := a.Run([]string{"autoupdate", "run", "--json"}); code != 0 {
		t.Fatalf("run: %s", out.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != fakeBinary("v0.0.2") {
		t.Fatal("binary not replaced")
	}
	statePath, _, _, _ := autoUpdateStatePaths()
	s := readAutoUpdateState(statePath)
	if s.LastRun["updated"] != true || s.LastRun["version"] != "v0.0.2" {
		b, _ := json.Marshal(s)
		t.Fatalf("state %s", b)
	}
}
