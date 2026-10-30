// Package config manages profiles stored under the XDG config directory.
// Credentials are stored separately (see pkg/auth) and never here.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// NetworkMainnet is the only network the Oku backend currently serves
// (account/src/connectapi/perps/signing.go hardcodes Hyperliquid Mainnet).
const NetworkMainnet = "mainnet"

// DefaultAPIURL is the production Oku account API origin. It applies only to
// fresh profiles created without --api-url and to profile-less commands;
// existing profiles keep the api_url they stored. Staging is opt-in with
// StagingAPIURL (--api-url or a staging profile).
const DefaultAPIURL = "https://accounts.icarus.tools"

// StagingAPIURL is the staging account API origin.
const StagingAPIURL = "https://accounts.lat.gfx.town"

var (
	addressRE = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	nameRE    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,31}$`)
)

// Profile selects an account, network, and API origin.
type Profile struct {
	PrivyAppID string `json:"privy_app_id,omitempty"`
	Network    string `json:"network"`
	APIURL     string `json:"api_url"`
	Account    string `json:"account,omitempty"`
	// CredentialStore records where the profile's credential lives on Linux:
	// "file" (default, plain 0600 file) or "keyring" (Secret Service).
	CredentialStore string `json:"credential_store,omitempty"`
}

// File is the persisted config document.
type File struct {
	Version        int                 `json:"version"`
	DefaultProfile string              `json:"default_profile,omitempty"`
	Profiles       map[string]*Profile `json:"profiles"`
	// Telemetry is global (not per profile) and off unless explicitly enabled.
	Telemetry *Telemetry `json:"telemetry,omitempty"`
	// AutoUpdate is global and off unless explicitly enabled.
	AutoUpdate *AutoUpdate `json:"auto_update,omitempty"`
	// Telegram configures 'oku telegram serve'. Token is a secret: config
	// show redacts it.
	Telegram *Telegram `json:"telegram,omitempty"`
}

// Telegram holds the bot token and the user IDs allowed to use the bot.
type Telegram struct {
	Token          string  `json:"token,omitempty"`
	AllowedUserIDs []int64 `json:"allowed_user_ids,omitempty"`
}

// Validate requires positive, unique user IDs.
func (t *Telegram) Validate() error {
	seen := map[int64]bool{}
	for _, id := range t.AllowedUserIDs {
		if id <= 0 {
			return fmt.Errorf("allowed_user_ids: %d is not a positive Telegram user ID", id)
		}
		if seen[id] {
			return fmt.Errorf("allowed_user_ids: %d is listed twice", id)
		}
		seen[id] = true
	}
	return nil
}

// AutoUpdate holds the opt-in background update preference.
type AutoUpdate struct {
	Enabled bool `json:"enabled"`
	// Interval is the minimum time between background checks, as a Go
	// duration string. Empty means the default (24h).
	Interval string `json:"interval,omitempty"`
	// Skills also refreshes installed bundled skills after an update.
	Skills bool `json:"skills"`
}

// Telemetry holds the opt-in usage telemetry preference.
type Telemetry struct {
	Enabled bool `json:"enabled"`
	// HintShown records that the one-time login hint was printed.
	HintShown bool `json:"hint_shown,omitempty"`
}

// Dir returns the oku config directory.
func Dir() (string, error) {
	if d := os.Getenv("OKU_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "oku"), nil
}

// StateDir returns the oku state directory (journal, nonce state).
func StateDir() (string, error) {
	if d := os.Getenv("OKU_STATE_DIR"); d != "" {
		return d, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "oku"), nil
}

// Path returns the config file path.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Load reads the config, returning an empty document if absent.
func Load() (*File, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &File{Version: 1, Profiles: map[string]*Profile{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]*Profile{}
	}
	for name, pr := range f.Profiles {
		if err := pr.Validate(); err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
	}
	if f.Telegram != nil {
		if err := f.Telegram.Validate(); err != nil {
			return nil, fmt.Errorf("telegram: %w", err)
		}
	}
	return &f, nil
}

// Save writes the config atomically with 0600 permissions.
func (f *File) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(p, append(raw, '\n'), 0o600)
}

// Names returns sorted profile names.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ValidName reports whether n is a valid profile name.
func ValidName(n string) bool { return nameRE.MatchString(n) }

// ValidAddress reports whether a is a 20-byte hex address.
func ValidAddress(a string) bool { return addressRE.MatchString(a) }

// Validate checks a profile.
func (p *Profile) Validate() error {
	if p.Network != NetworkMainnet {
		return fmt.Errorf("network %q is not supported (only %q)", p.Network, NetworkMainnet)
	}
	if p.Account != "" && !ValidAddress(p.Account) {
		return fmt.Errorf("account %q is not a 0x-prefixed 20-byte address", p.Account)
	}
	switch p.CredentialStore {
	case "", "file", "keyring":
	default:
		return fmt.Errorf("credential_store %q is not supported (want file or keyring)", p.CredentialStore)
	}
	return ValidateAPIURL(p.APIURL)
}

// ValidateAPIURL requires https (http allowed only for loopback testing).
func ValidateAPIURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid api url %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("api url must be an origin without credentials, path or query")
	}
	host := u.Hostname()
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return fmt.Errorf("api url must use https")
	}
	return nil
}

// NormalizeAPIURL strips a trailing slash.
func NormalizeAPIURL(raw string) string { return strings.TrimRight(raw, "/") }

// WriteFileAtomic writes via a temp file and rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	// Persist the rename itself so a crash cannot roll the file back.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	serr := dir.Sync()
	cerr := dir.Close()
	if serr != nil {
		return serr
	}
	return cerr
}
