package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/docs"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func docsSearchFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.IntFlag{Name: "limit", Value: 50, Usage: "maximum matches (0 = unlimited)"}}
}

func docsCommandsFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.BoolFlag{Name: "markdown", Usage: "print the Markdown command reference generated from this binary"}}
}

func docsSchemaFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.StringFlag{Name: "command", Usage: "command path, e.g. 'orders prepare'"}, &urfave.BoolFlag{Name: "all", Usage: "show schemas for every command with flags"}}
}

func (a *App) cmdDocsSchema(e *env) (*result, error) {
	path := e.cmd.String("command")
	if len(e.args) > 0 {
		path = strings.Join(e.args, " ")
	}
	if e.cmd.Bool("all") {
		if path != "" {
			return nil, output.Errorf(output.CodeUsage, "--all cannot be combined with a command")
		}
		all := map[string]any{}
		for name, h := range a.commands() {
			if h.flags != nil {
				schema, _ := InputSchema(name)
				all[name] = schema
			}
		}
		return &result{data: all, human: func(w io.Writer) { b, _ := json.MarshalIndent(all, "", "  "); fmt.Fprintln(w, string(b)) }}, nil
	}
	if path == "" {
		return nil, output.Errorf(output.CodeUsage, "usage: oku docs schema --command 'orders prepare' [--json]")
	}
	schema, err := InputSchema(path)
	if err != nil {
		return nil, output.Errorf(output.CodeUsage, "%v", err)
	}
	return &result{data: schema, human: func(w io.Writer) { b, _ := json.MarshalIndent(schema, "", "  "); fmt.Fprintln(w, string(b)) }}, nil
}

func (a *App) cmdDocsList(e *env) (*result, error) {
	list, err := docs.Bundled().List()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "listing docs")
	}
	return &result{data: map[string]any{"docs": list}, human: func(w io.Writer) {
		tw := newTable(w)
		for _, d := range list {
			fmt.Fprintf(tw, "%s\t%s\n", d.Name, d.Title)
		}
		tw.Flush()
	}}, nil
}

func (a *App) cmdDocsShow(e *env) (*result, error) {
	args, err := e.positional(1, "docs show NAME")
	if err != nil {
		return nil, err
	}
	b, err := docs.Bundled().Show(args[0])
	if errors.Is(err, fs.ErrNotExist) {
		return nil, output.Errorf(output.CodeNotFound, "no bundled document %q", args[0]).WithDetail("hint", "run 'oku docs list'")
	}
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "docs show")
	}
	return &result{data: map[string]any{"name": args[0], "content": string(b)}, human: func(w io.Writer) { _, _ = w.Write(b) }}, nil
}

func (a *App) cmdDocsSearch(e *env) (*result, error) {
	if len(e.args) == 0 {
		return nil, output.Errorf(output.CodeUsage, "usage: oku docs search TERMS...")
	}
	q := ""
	for i, s := range e.args {
		if i > 0 {
			q += " "
		}
		q += s
	}
	m, err := docs.Bundled().Search(q, e.cmd.Int("limit"))
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "docs search")
	}
	return &result{data: map[string]any{"query": q, "matches": m}, human: func(w io.Writer) {
		for _, x := range m {
			fmt.Fprintf(w, "%s:%d: %s\n", x.Name, x.Line, x.Text)
		}
	}}, nil
}

// cmdDocsCommands lists the live command tree; --markdown prints the
// generated reference (the source of docs/commands.md).
func (a *App) cmdDocsCommands(e *env) (*result, error) {
	var buf bytes.Buffer
	if err := WriteCommandReference(&buf); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "command reference")
	}
	type entry struct {
		Command string       `json:"command"`
		Summary string       `json:"summary"`
		Input   []inputField `json:"input,omitempty"`
	}
	list := []entry{}
	for _, name := range sortedCommandNames(a.commands()) {
		h := a.commands()[name]
		list = append(list, entry{Command: "oku " + name, Summary: h.summary, Input: fields(h)})
	}
	markdown := e.cmd.Bool("markdown")
	data := map[string]any{"commands": list}
	if markdown {
		data["markdown"] = buf.String()
	}
	return &result{data: data, human: func(w io.Writer) {
		if markdown {
			_, _ = w.Write(buf.Bytes())
			return
		}
		tw := newTable(w)
		for _, x := range list {
			fmt.Fprintf(tw, "%s\t%s\n", x.Command, x.Summary)
		}
		tw.Flush()
	}}, nil
}
