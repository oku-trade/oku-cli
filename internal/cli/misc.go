package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/oku-trade/oku-cli/internal/api"
	"github.com/oku-trade/oku-cli/internal/auth"
	"github.com/oku-trade/oku-cli/internal/config"
	"github.com/oku-trade/oku-cli/internal/journal"
	"github.com/oku-trade/oku-cli/internal/output"
	"github.com/oku-trade/oku-cli/internal/skills"
)

func skillFlags(fs *flag.FlagSet) {
	fs.String("target", "", "agent: jcode, claude or codex")
	fs.String("dir", "", "explicit skills directory (overrides --target)")
	fs.Bool("force", false, "overwrite or remove files that differ from the bundled version")
}

func (a *App) cmdSkillsList(e *env) (*result, error) {
	list, err := skills.List()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "listing skills")
	}
	targets := map[string]string{}
	for t := range skills.Targets {
		targets[t], _ = skills.ResolveDir(t, "")
	}
	return &result{data: map[string]any{"skills": list, "targets": targets}, human: func(w io.Writer) {
		for _, s := range list {
			fmt.Fprintf(w, "%s (%d files)\n", s.Name, len(s.Files))
		}
		if len(list) == 0 {
			fmt.Fprintln(w, "no skills bundled in this build")
		}
	}}, nil
}

func (e *env) skillRoot() (string, error) {
	t, d := str(e, "target"), str(e, "dir")
	if t == "" && d == "" {
		return "", output.Errorf(output.CodeUsage, "pass --target jcode|claude|codex or --dir PATH")
	}
	root, err := skills.ResolveDir(t, d)
	if err != nil {
		return "", output.Wrap(output.CodeInvalidArgument, err, "skills target")
	}
	return root, nil
}

func skillsResult(root string, res []skills.FileResult, err error, verb string) (*result, error) {
	if errors.Is(err, skills.ErrExists) {
		return nil, output.Errorf(output.CodeConflict, "files under %s differ from the bundled skill; nothing was %s. Re-run with --force to replace them", root, verb).
			WithDetail("files", res)
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "skills")
	}
	return &result{data: map[string]any{"root": root, "files": res}, human: func(w io.Writer) {
		for _, r := range res {
			fmt.Fprintf(w, "%-11s %s\n", r.Action, r.Path)
		}
	}}, nil
}

func (a *App) cmdSkillsInstall(e *env) (*result, error) {
	root, err := e.skillRoot()
	if err != nil {
		return nil, err
	}
	res, err := skills.Install(root, str(e, "force") == "true")
	return skillsResult(root, res, err, "written")
}

func (a *App) cmdSkillsUninstall(e *env) (*result, error) {
	root, err := e.skillRoot()
	if err != nil {
		return nil, err
	}
	res, err := skills.Uninstall(root, str(e, "force") == "true")
	return skillsResult(root, res, err, "removed")
}

func (a *App) cmdDoctor(e *env) (*result, error) {
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	checks := []check{}
	add := func(name string, ok bool, detail string) { checks = append(checks, check{name, ok, detail}) }
	p, err := e.profile()
	if err != nil {
		add("profile", false, output.As(err).Message)
	} else {
		add("profile", true, fmt.Sprintf("%s (%s, %s)", orDash(p.Name), p.Network, p.APIURL))
		var mk api.GetMarketsResponse
		if err := e.perpsRead(p, "GetMarkets", map[string]string{"coin": "BTC"}, &mk); err != nil {
			add("oku_api", false, output.As(err).Message)
		} else {
			add("oku_api", len(mk.Markets) > 0, "PerpsService reachable")
		}
		if p.Name != "" {
			md, err := auth.LoadMetadata(p.Name)
			switch {
			case errors.Is(err, auth.ErrNoCredentials):
				add("credentials", true, "not enrolled (read-only public data only)")
			case err != nil:
				add("credentials", false, err.Error())
			default:
				berr := checkBinding(md, p)
				add("credentials", berr == nil, fmt.Sprintf("%s, scopes %s", md.Status, describeScopes(md.Scopes)))
			}
			if j, err := journal.Open(p.Name); err == nil {
				un, err := j.Unresolved()
				switch {
				case err != nil:
					add("journal", false, err.Error())
				default:
					add("journal", len(un) == 0, fmt.Sprintf("%d unresolved intent(s)", len(un)))
				}
			}
		}
	}
	cfg, _ := config.Path()
	add("config", true, cfg)
	ok := true
	for _, c := range checks {
		ok = ok && c.OK
	}
	return &result{data: map[string]any{"ok": ok, "checks": checks}, human: func(w io.Writer) {
		for _, c := range checks {
			mark := "ok  "
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(w, "[%s] %-12s %s\n", mark, c.Name, c.Detail)
		}
	}}, nil
}

func (a *App) printHelp(cmds map[string]*handler) {
	w := a.Stdout
	fmt.Fprintln(w, "oku: Oku perps from your terminal and your agent (Hyperliquid mainnet)")
	fmt.Fprintln(w, "\nUsage: oku <command> [flags]\n\nCommands:")
	names := make([]string, 0, len(cmds))
	for n := range cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-18s %s\n", n, cmds[n].summary)
	}
	fmt.Fprintln(w, `
Global flags:
  --profile NAME   profile (default $OKU_PROFILE or config default)
  --network NAME   must be mainnet; checked against the profile
  --account 0x..   account for read commands
  --json           one JSON envelope on stdout; diagnostics on stderr
  --timeout DUR    per-request timeout (default 15s, max 2m)

Trading uses a local delegated agent key. It is trusted-host signing: anything
that can unlock it on this machine can trade the account. Oku revocation does
not remove the Hyperliquid agent, orders or positions.`)
}

func (a *App) printCommandHelp(path string, h *handler, fs *flag.FlagSet) {
	fmt.Fprintf(a.Stdout, "oku %s: %s\n\nFlags:\n", path, h.summary)
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "help" {
			return
		}
		fmt.Fprintf(a.Stdout, "  --%-14s %s", f.Name, f.Usage)
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
			fmt.Fprintf(a.Stdout, " (default %s)", f.DefValue)
		}
		fmt.Fprintln(a.Stdout)
	})
}
