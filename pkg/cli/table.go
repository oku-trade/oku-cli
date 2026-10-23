package cli

import (
	"io"
	"text/tabwriter"
)

// newTable returns a tabwriter with the spacing every human table uses.
// Callers write tab-separated rows and must call Flush.
func newTable(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0) }
