package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/drafts"
	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

func draftFlags() []urfave.Flag {
	return []urfave.Flag{&urfave.BoolFlag{Name: "stdin", Usage: "read the draft JSON from stdin (required)"}}
}

// cmdDraftsRepair is local only: no network, credentials, journal or
// submission. The candidate must still be prepared explicitly.
func (a *App) cmdDraftsRepair(e *env) (*result, error) {
	if !e.cmd.Bool("stdin") {
		return nil, output.Errorf(output.CodeUsage, "usage: oku drafts repair --stdin < draft.json")
	}
	c, err := drafts.Repair(e.app.Stdin)
	if err != nil {
		return nil, output.Wrap(output.CodeInvalidArgument, err, "draft refused")
	}
	return &result{data: map[string]any{
		"repaired": c.Repaired, "candidate": c.Order, "json": c.JSON, "prepare_args": c.PrepareArgs, "input": c.Input,
		"notes": []string{"nothing was prepared, signed or sent", "review every field; then run the prepare command, which returns the digest to approve"},
	}, human: func(w io.Writer) {
		fmt.Fprintf(w, "repaired: %v\ncandidate: %s\nreview, then run: oku %s\n", c.Repaired, c.JSON, strings.Join(c.PrepareArgs, " "))
	}}, nil
}
