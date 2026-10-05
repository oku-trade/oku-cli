package cli

import (
	"fmt"
	"time"

	"github.com/oku-trade/oku-cli/pkg/output"

	urfave "github.com/urfave/cli/v3"
)

// flagKind names the accessor that matches a declared flag type.
type flagKind string

const (
	kindString   flagKind = "String"
	kindBool     flagKind = "Bool"
	kindInt      flagKind = "Int"
	kindInt64    flagKind = "Int64"
	kindDuration flagKind = "Duration"
)

func kindOf(f urfave.Flag) (flagKind, bool) {
	switch f.(type) {
	case *urfave.StringFlag:
		return kindString, true
	case *urfave.BoolFlag:
		return kindBool, true
	case *urfave.IntFlag:
		return kindInt, true
	case *urfave.Int64Flag:
		return kindInt64, true
	case *urfave.DurationFlag:
		return kindDuration, true
	}
	return "", false
}

// flagMisuse is raised when a handler reads a flag through an accessor that
// does not match its declared type. urfave silently returns a zero value in
// that case (e.g. String on a BoolFlag), which once dropped --reduce-only, so
// the read fails closed instead. It is recovered into an INTERNAL error.
type flagMisuse struct{ name, want, got string }

func (m flagMisuse) Error() string {
	return fmt.Sprintf("internal error: flag --%s read as %s, declared as %s", m.name, m.got, m.want)
}

// checkedFlags enforces type-correct reads over any flagSource. declared
// holds handler flags plus global flags.
type checkedFlags struct {
	src      flagSource
	declared map[string]flagKind
}

func newCheckedFlags(src flagSource, flags ...[]urfave.Flag) checkedFlags {
	d := map[string]flagKind{}
	for _, fs := range flags {
		for _, f := range fs {
			if k, ok := kindOf(f); ok {
				for _, n := range f.Names() {
					d[n] = k
				}
			}
		}
	}
	return checkedFlags{src: src, declared: d}
}

func (c checkedFlags) check(name string, k flagKind) {
	got, ok := c.declared[name]
	if !ok {
		panic(flagMisuse{name: name, want: "undeclared", got: string(k)})
	}
	if got != k {
		panic(flagMisuse{name: name, want: string(got), got: string(k)})
	}
}

func (c checkedFlags) String(n string) string { c.check(n, kindString); return c.src.String(n) }
func (c checkedFlags) Bool(n string) bool     { c.check(n, kindBool); return c.src.Bool(n) }
func (c checkedFlags) Int(n string) int       { c.check(n, kindInt); return c.src.Int(n) }
func (c checkedFlags) Int64(n string) int64   { c.check(n, kindInt64); return c.src.Int64(n) }
func (c checkedFlags) Duration(n string) time.Duration {
	c.check(n, kindDuration)
	return c.src.Duration(n)
}

// runChecked runs a handler, converting a flag type misuse into an error.
func runChecked(h *handler, e *env) (res *result, err error) {
	defer func() {
		if r := recover(); r != nil {
			m, ok := r.(flagMisuse)
			if !ok {
				panic(r)
			}
			res, err = nil, output.Wrap(output.CodeInternal, m, "command")
		}
	}()
	return h.run(e)
}
