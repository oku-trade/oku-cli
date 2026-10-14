package cli

import (
	"errors"
	"fmt"
	urfave "github.com/urfave/cli/v3"
	"io"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/output"
)

// resolved is the effective profile for a command.
type resolved struct {
	PrivyAppID string `json:"privy_app_id,omitempty"`
	Name       string `json:"profile"`
	Network    string `json:"network"`
	APIURL     string `json:"api_url"`
	Account    string `json:"account"`
}

// profile resolves --profile / OKU_PROFILE / default. Without any profile a
// built-in mainnet default is used for public reads only.
func (e *env) profile() (*resolved, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	name := e.g.profile
	if name == "" {
		name = e.app.Getenv("OKU_PROFILE")
	}
	if name == "" {
		name = f.DefaultProfile
	}
	r := &resolved{Network: config.NetworkMainnet, APIURL: config.DefaultAPIURL}
	if name != "" {
		p, ok := f.Profiles[name]
		if !ok {
			return nil, output.Errorf(output.CodeProfileNotFound, "profile %q not found", name).
				WithDetail("hint", "run 'oku profiles list' or 'oku profiles add'")
		}
		r.Name, r.Network, r.APIURL, r.Account = name, p.Network, config.NormalizeAPIURL(p.APIURL), p.Account
		r.PrivyAppID = p.PrivyAppID
	}
	if e.g.network != "" && e.g.network != r.Network {
		return nil, output.Errorf(output.CodeNetworkMismatch, "--network %s does not match profile network %s", e.g.network, r.Network)
	}
	return r, nil
}

// readAccount returns the account for read commands: --account overrides the
// profile account. It is never silently defaulted.
func (e *env) readAccount(p *resolved) (string, error) {
	acct := p.Account
	if e.g.account != "" {
		acct = e.g.account
	}
	if acct == "" {
		return "", output.Errorf(output.CodeAccountRequired, "no account selected").
			WithDetail("hint", "pass --account 0x... or add a profile with --account")
	}
	if !config.ValidAddress(acct) {
		return "", output.Errorf(output.CodeInvalidArgument, "account %q is not a 0x-prefixed 20-byte address", acct)
	}
	return strings.ToLower(acct), nil
}

func (e *env) client(p *resolved) *api.Client {
	return &api.Client{
		BaseURL:   p.APIURL,
		HTTP:      api.NewHTTPClient(e.g.timeout + 5e9),
		UserAgent: "oku-agent/" + e.app.Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")",
	}
}

func (a *App) cmdVersion(e *env) (*result, error) {
	data := map[string]any{
		"version":        a.Version,
		"commit":         buildCommit(),
		"go":             runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"schema_version": output.SchemaVersion,
	}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "Oku Agent %s (%s, %s/%s)\n", a.Version, buildCommit(), runtime.GOOS, runtime.GOARCH)
	}}, nil
}

func (a *App) cmdConfigPath(e *env) (*result, error) {
	cfg, err := config.Path()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "config path")
	}
	cred, _ := auth.PrivyPath("<profile>")
	state, _ := config.StateDir()
	data := map[string]string{"config": cfg, "credentials": cred, "state": state}
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "config:      %s\ncredentials: %s\nstate:       %s\n", cfg, cred, state)
	}}, nil
}

func (a *App) cmdConfigShow(e *env) (*result, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	return &result{data: f, human: func(w io.Writer) { printProfiles(w, f) }}, nil
}

func (a *App) cmdProfilesList(e *env) (*result, error) {
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	type row struct {
		Name          string `json:"name"`
		Default       bool   `json:"default"`
		Network       string `json:"network"`
		APIURL        string `json:"api_url"`
		Account       string `json:"account,omitempty"`
		Authenticated bool   `json:"authenticated"`
	}
	rows := []row{}
	for _, n := range f.Names() {
		p := f.Profiles[n]
		_, credErr := auth.LoadPrivyMetadata(n)
		rows = append(rows, row{n, n == f.DefaultProfile, p.Network, p.APIURL, p.Account, credErr == nil})
	}
	return &result{data: map[string]any{"profiles": rows}, human: func(w io.Writer) { printProfiles(w, f) }}, nil
}

func printProfiles(w io.Writer, f *config.File) {
	if len(f.Profiles) == 0 {
		fmt.Fprintln(w, "no profiles; add one with: oku profiles add NAME --account 0x...")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "\tNAME\tNETWORK\tACCOUNT\tAPI")
	for _, n := range f.Names() {
		p := f.Profiles[n]
		mark := ""
		if n == f.DefaultProfile {
			mark = "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", mark, n, p.Network, p.Account, p.APIURL)
	}
	tw.Flush()
}

func profilesAddFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "api-url", Value: config.DefaultAPIURL, Usage: "Oku account API origin"},
		&urfave.StringFlag{Name: "privy-app-id", Usage: "Privy application ID (defaults to Oku Trade Agents)"},
		&urfave.BoolFlag{Name: "default", Usage: "make this the default profile"},
	}
}

func (a *App) cmdProfilesAdd(e *env) (*result, error) {
	args, err := e.positional(1, "profiles add NAME --account 0x... [--api-url URL] [--default]")
	if err != nil {
		return nil, err
	}
	name := args[0]
	if !config.ValidName(name) {
		return nil, output.Errorf(output.CodeInvalidArgument, "invalid profile name %q", name)
	}
	network := e.g.network
	if network == "" {
		network = config.NetworkMainnet
	}
	p := &config.Profile{
		PrivyAppID: e.cmd.String("privy-app-id"),
		Network:    network,
		APIURL:     config.NormalizeAPIURL(e.cmd.String("api-url")),
		Account:    e.g.account,
	}
	if p.Account == "" {
		return nil, output.Errorf(output.CodeAccountRequired, "--account is required")
	}
	if err := p.Validate(); err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "invalid profile")
	}
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	if _, exists := f.Profiles[name]; exists {
		// Changing account/API of an enrolled profile would silently reuse
		// the credential elsewhere, so profiles are immutable.
		return nil, output.Errorf(output.CodeConflict, "profile %q already exists; remove it first", name)
	}
	f.Profiles[name] = p
	if e.cmd.Bool("default") || f.DefaultProfile == "" {
		f.DefaultProfile = name
	}
	if err := f.Save(); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "saving config")
	}
	return &result{data: map[string]any{"profile": name, "config": p, "default": f.DefaultProfile == name},
		human: func(w io.Writer) { fmt.Fprintf(w, "added profile %s\n", name) }}, nil
}

func (a *App) cmdProfilesUse(e *env) (*result, error) {
	args, err := e.positional(1, "profiles use NAME")
	if err != nil {
		return nil, err
	}
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	if _, ok := f.Profiles[args[0]]; !ok {
		return nil, output.Errorf(output.CodeProfileNotFound, "profile %q not found", args[0])
	}
	f.DefaultProfile = args[0]
	if err := f.Save(); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "saving config")
	}
	return &result{data: map[string]string{"default_profile": args[0]},
		human: func(w io.Writer) { fmt.Fprintf(w, "default profile: %s\n", args[0]) }}, nil
}

func (a *App) cmdProfilesRemove(e *env) (*result, error) {
	args, err := e.positional(1, "profiles remove NAME")
	if err != nil {
		return nil, err
	}
	name := args[0]
	if _, err := auth.LoadMetadata(name); !errors.Is(err, auth.ErrNoCredentials) {
		return nil, output.Errorf(output.CodeConflict, "profile %q has stored legacy credentials; run 'oku auth logout --profile %s' first", name, name)
	}
	if _, err := auth.LoadPrivyMetadata(name); !errors.Is(err, auth.ErrNoCredentials) {
		return nil, output.Errorf(output.CodeConflict, "profile %q has stored Privy credentials; run 'oku auth logout --profile %s' first", name, name)
	}
	f, err := config.Load()
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "loading config")
	}
	if _, ok := f.Profiles[name]; !ok {
		return nil, output.Errorf(output.CodeProfileNotFound, "profile %q not found", name)
	}
	delete(f.Profiles, name)
	if f.DefaultProfile == name {
		f.DefaultProfile = ""
	}
	if err := f.Save(); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "saving config")
	}
	return &result{data: map[string]string{"removed": name},
		human: func(w io.Writer) { fmt.Fprintf(w, "removed profile %s\n", name) }}, nil
}
