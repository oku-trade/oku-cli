package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"

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

func (a *App) cmdDocsList(e *env) (*result, error) {
	list, err := docs.Bundled().List()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "listing docs")
	}
	return &result{data: map[string]any{"docs": list}, human: func(w io.Writer) {
		for _, d := range list {
			fmt.Fprintf(w, "%-16s %s\n", d.Name, d.Title)
		}
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
		Command string `json:"command"`
		Summary string `json:"summary"`
	}
	list := []entry{}
	for _, name := range sortedCommandNames(a.commands()) {
		list = append(list, entry{"oku " + name, a.commands()[name].summary})
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
		for _, x := range list {
			fmt.Fprintf(w, "%-24s %s\n", x.Command, x.Summary)
		}
	}}, nil
}
