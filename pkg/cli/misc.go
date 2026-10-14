package cli

import (
	"errors"
	"fmt"
	urfave "github.com/urfave/cli/v3"
	"io"

	"github.com/oku-trade/oku-cli/pkg/api"
	"github.com/oku-trade/oku-cli/pkg/auth"
	"github.com/oku-trade/oku-cli/pkg/config"
	"github.com/oku-trade/oku-cli/pkg/journal"
	"github.com/oku-trade/oku-cli/pkg/output"
	"github.com/oku-trade/oku-cli/pkg/skills"
)

func skillFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.StringFlag{Name: "target", Usage: "agent: jcode, claude or codex"},
		&urfave.StringFlag{Name: "dir", Usage: "explicit skills directory (overrides --target)"},
		&urfave.BoolFlag{Name: "force", Usage: "overwrite or remove files that differ from the bundled version"},
	}
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
	res, err := skills.Install(root, e.cmd.Bool("force"))
	return skillsResult(root, res, err, "written")
}

func (a *App) cmdSkillsUninstall(e *env) (*result, error) {
	root, err := e.skillRoot()
	if err != nil {
		return nil, err
	}
	res, err := skills.Uninstall(root, e.cmd.Bool("force"))
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
			md, err := auth.LoadPrivyMetadata(p.Name)
			switch {
			case errors.Is(err, auth.ErrNoCredentials):
				add("credentials", true, "not logged in (public reads only)")
			case err != nil:
				add("credentials", false, err.Error())
			default:
				berr := e.checkBinding(md, p)
				add("credentials", berr == nil && md.Status == "active", fmt.Sprintf("%s, agent %s", md.Status, md.AgentAddress))
			}
			if j, err := journal.Open(p.Name); err == nil {
				un, err := j.Unresolved()
				j.Close()
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
