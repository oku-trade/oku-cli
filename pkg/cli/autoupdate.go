package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/skills"
	urfave "github.com/urfave/cli/v3"
)

const (
	autoUpdateDefaultInterval = 24 * time.Hour
	autoUpdateMinInterval     = time.Hour
	// autoUpdateChildEnv marks the detached background process (and anything
	// it runs) so it never schedules another check.
	autoUpdateChildEnv = "OKU_AUTO_UPDATE_CHILD"
	// autoUpdateOffEnv disables background checks for one shell or CI job.
	autoUpdateOffEnv = "OKU_NO_AUTO_UPDATE"
)

// autoUpdateState is persisted under the state dir between invocations.
type autoUpdateState struct {
	LastCheck time.Time      `json:"last_check"`
	LastRun   map[string]any `json:"last_result,omitempty"`
}

func autoUpdateEnableFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "interval", Usage: "minimum time between background checks, e.g. 24h or 6h (min 1h; default 24h)"},
		&urfave.BoolFlag{Name: "no-skills", Usage: "update only the binary; leave installed skills alone"},
	}
}

func autoUpdateStatePaths() (state, lock, log string, err error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", "", "", err
	}
	return filepath.Join(dir, "autoupdate.json"), filepath.Join(dir, "autoupdate.lock"), filepath.Join(dir, "autoupdate.log"), nil
}

func readAutoUpdateState(p string) autoUpdateState {
	var s autoUpdateState
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func writeAutoUpdateState(p string, s autoUpdateState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(p, append(b, '\n'), 0o600)
}

func autoUpdateInterval(c *config.AutoUpdate) time.Duration {
	if c == nil || c.Interval == "" {
		return autoUpdateDefaultInterval
	}
	d, err := time.ParseDuration(c.Interval)
	if err != nil || d < autoUpdateMinInterval {
		return autoUpdateDefaultInterval
	}
	return d
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// spawnAutoUpdate starts a detached `oku autoupdate run`; tests replace it.
var spawnAutoUpdate = func(exe, logPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "autoupdate", "run", "--json")
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Env = append(os.Environ(), autoUpdateChildEnv+"=1")
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// maybeAutoUpdate runs after every command. When the user opted in and the
// interval has elapsed it records the check time and starts a detached
// background update, so the current command is never slowed or changed.
func (a *App) maybeAutoUpdate(command string) {
	defer func() { _ = recover() }()
	if command == "" || command == "self-update" || command == "version" || strings.HasPrefix(command, "autoupdate") {
		return
	}
	if a.getenv(autoUpdateChildEnv) != "" || isTruthy(a.getenv(autoUpdateOffEnv)) {
		return
	}
	f, err := config.Load()
	if err != nil || f.AutoUpdate == nil || !f.AutoUpdate.Enabled {
		return
	}
	statePath, lockPath, logPath, err := autoUpdateStatePaths()
	if err != nil {
		return
	}
	// Cheap unlocked read first: the common case is "not due yet".
	now := a.now()
	interval := autoUpdateInterval(f.AutoUpdate)
	if s := readAutoUpdateState(statePath); now.Sub(s.LastCheck) < interval && !s.LastCheck.After(now) {
		return
	}
	unlock, err := config.LockPath(lockPath)
	if err != nil {
		return
	}
	defer unlock()
	s := readAutoUpdateState(statePath)
	if now.Sub(s.LastCheck) < interval && !s.LastCheck.After(now) {
		return // another process just claimed this window
	}
	s.LastCheck = now
	if writeAutoUpdateState(statePath, s) != nil {
		return
	}
	exe, err := selfExecutable()
	if err != nil {
		return
	}
	_ = spawnAutoUpdate(exe, logPath)
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (a *App) cmdAutoUpdateEnable(e *env) (*result, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	c := &config.AutoUpdate{Enabled: true, Skills: !e.cmd.Bool("no-skills")}
	if f.AutoUpdate != nil {
		c.Interval = f.AutoUpdate.Interval
	}
	if iv := strings.TrimSpace(e.cmd.String("interval")); iv != "" {
		d, err := time.ParseDuration(iv)
		if err != nil {
			return nil, output.Errorf(output.CodeInvalidArgument, "--interval %q is not a duration like 24h", iv)
		}
		if d < autoUpdateMinInterval {
			return nil, output.Errorf(output.CodeInvalidArgument, "--interval must be at least %s", autoUpdateMinInterval)
		}
		c.Interval = d.String()
	}
	f.AutoUpdate = c
	if err := f.Save(); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "saving config")
	}
	return a.cmdAutoUpdateStatus(e)
}

func (a *App) cmdAutoUpdateDisable(e *env) (*result, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	if f.AutoUpdate != nil {
		f.AutoUpdate.Enabled = false
		if err := f.Save(); err != nil {
			return nil, output.Wrap(output.CodeConfigError, err, "saving config")
		}
	}
	return a.cmdAutoUpdateStatus(e)
}

func (a *App) cmdAutoUpdateStatus(*env) (*result, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	c := f.AutoUpdate
	enabled := c != nil && c.Enabled
	interval := autoUpdateInterval(c)
	data := map[string]any{
		"enabled": enabled, "interval": interval.String(), "skills": c != nil && c.Skills,
		"env_disabled": isTruthy(a.getenv(autoUpdateOffEnv)),
	}
	statePath, _, logPath, err := autoUpdateStatePaths()
	var s autoUpdateState
	if err == nil {
		s = readAutoUpdateState(statePath)
		data["log"] = logPath
	}
	if !s.LastCheck.IsZero() {
		data["last_check"] = s.LastCheck.UTC().Format(time.RFC3339)
		data["next_check"] = s.LastCheck.Add(interval).UTC().Format(time.RFC3339)
	}
	if s.LastRun != nil {
		data["last_result"] = s.LastRun
	}
	stale := []string{}
	for t := range skills.Targets {
		if root, err := skills.ResolveDir(t, ""); err == nil {
			if inst, pr, err := skills.Pristine(root); err == nil && inst && !pr {
				stale = append(stale, root)
			}
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		data["skills_unmanaged"] = stale
	}
	return &result{data: data, human: func(w io.Writer) {
		state := "off"
		if enabled {
			state = "on"
		}
		fmt.Fprintf(w, "auto-update: %s (every %s, skills: %v)\n", state, interval, data["skills"])
		if data["env_disabled"] == true {
			fmt.Fprintf(w, "%s is set: background checks are skipped\n", autoUpdateOffEnv)
		}
		if v, ok := data["last_check"]; ok {
			fmt.Fprintf(w, "last check: %s (next after %s)\n", v, data["next_check"])
		}
		if r, ok := data["last_result"].(map[string]any); ok {
			if msg, ok := r["error"]; ok {
				fmt.Fprintf(w, "last result: error: %v\n", msg)
			} else if r["updated"] == true {
				fmt.Fprintf(w, "last result: updated %v -> %v\n", r["previous"], r["version"])
			} else {
				fmt.Fprintf(w, "last result: up to date (%v)\n", r["current"])
			}
		}
		if len(stale) > 0 {
			fmt.Fprintf(w, "skills in %s differ from this build: run `oku skills install --force` once so auto-update can manage them\n", strings.Join(stale, ", "))
		}
		if enabled {
			fmt.Fprintf(w, "log: %v\n", data["log"])
		}
	}}, nil
}

// skillRootsToRefresh returns the standard agent skill roots whose installed
// oku skills exactly match this build, so a refresh cannot clobber edits.
func skillRootsToRefresh() []string {
	roots := []string{}
	for t := range skills.Targets {
		root, err := skills.ResolveDir(t, "")
		if err != nil {
			continue
		}
		if _, pristine, err := skills.Pristine(root); err == nil && pristine {
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	return roots
}

// cmdAutoUpdateRun performs one update: the same steps as `oku self-update`,
// then (when enabled) has the new binary refresh unmodified installed skills.
// The background job runs this; it can also be run by hand.
func (a *App) cmdAutoUpdateRun(e *env) (*result, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	refreshSkills := f.AutoUpdate == nil || f.AutoUpdate.Skills
	// Decide which skill roots are safe to refresh before the binary (and so
	// the bundled content we compare against) changes.
	roots := []string{}
	if refreshSkills {
		roots = skillRootsToRefresh()
	}
	data := map[string]any{"at": a.now().UTC().Format(time.RFC3339)}
	sub := &env{app: a, g: e.g, cmd: mapFlags{}, ctx: e.ctx}
	res, err := a.cmdSelfUpdate(sub)
	if err != nil {
		data["error"] = output.As(err).Message
		a.recordAutoUpdate(data)
		return nil, err
	}
	if m, ok := res.data.(map[string]any); ok {
		for k, v := range m {
			data[k] = v
		}
	}
	refreshed := []string{}
	if data["updated"] == true && len(roots) > 0 {
		exe, err := selfExecutable()
		if err == nil {
			for _, root := range roots {
				cmd := exec.CommandContext(e.ctx, exe, "skills", "install", "--dir", root, "--force", "--json")
				cmd.Env = append(os.Environ(), autoUpdateChildEnv+"=1")
				if cmd.Run() == nil {
					refreshed = append(refreshed, root)
				}
			}
		}
	}
	data["skills_refreshed"] = refreshed
	a.recordAutoUpdate(data)
	return &result{data: data, human: func(w io.Writer) {
		if data["updated"] == true {
			fmt.Fprintf(w, "updated %v -> %v\n", data["previous"], data["version"])
		} else {
			fmt.Fprintf(w, "already up to date (%v)\n", data["current"])
		}
		for _, r := range refreshed {
			fmt.Fprintf(w, "refreshed skills in %s\n", r)
		}
	}}, nil
}

func (a *App) recordAutoUpdate(data map[string]any) {
	statePath, lockPath, _, err := autoUpdateStatePaths()
	if err != nil {
		return
	}
	unlock, err := config.LockPath(lockPath)
	if err != nil {
		return
	}
	defer unlock()
	s := readAutoUpdateState(statePath)
	s.LastRun = data
	if s.LastCheck.IsZero() {
		s.LastCheck = a.now()
	}
	_ = writeAutoUpdateState(statePath, s)
}
