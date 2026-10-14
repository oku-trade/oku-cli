package cli

import (
	"context"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	urfave "github.com/urfave/cli/v3"
)

// Regression for M-01: --reduce-only must reach the persisted intent and
// the signed action through the executable.
func TestReduceOnlyPreservedThroughExecutable(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	id, dg := prepare(t, h, "--reduce-only")
	r := h.run("orders", "status", "--intent", id, "--json")
	if r.data()["intent"].(map[string]any)["reduce_only"] != true {
		t.Fatalf("--reduce-only dropped from persisted intent: %s", r.stdout)
	}
	r = h.run("orders", "submit", "--intent", id, "--digest", dg, "--json")
	if r.code != 0 {
		t.Fatalf("submit: %s", r.stdout)
	}
	raw := h.fake.lastSigned["signedAction"].(map[string]any)["actionJson"].(string)
	if !strings.Contains(raw, `"r":true`) {
		t.Fatalf("signed action is not reduce-only: %s", raw)
	}
}

// probeValue is a non-default value for each flag kind and its CLI text.
func probeValue(k flagKind) (any, string) {
	switch k {
	case kindBool:
		return true, ""
	case kindInt:
		return int64(7), "7"
	case kindInt64:
		return int64(9007199254740991), "9007199254740991"
	case kindDuration:
		return 3 * time.Minute, "3m"
	}
	return "probe-value", "probe-value"
}

// readAll reads every declared flag with its type-correct accessor.
func readAll(e *env, flags []urfave.Flag) map[string]any {
	got := map[string]any{}
	for _, f := range flags {
		n := f.Names()[0]
		k, _ := kindOf(f)
		switch k {
		case kindBool:
			got[n] = e.cmd.Bool(n)
		case kindInt:
			got[n] = int64(e.cmd.Int(n))
		case kindInt64:
			got[n] = e.cmd.Int64(n)
		case kindDuration:
			got[n] = e.cmd.Duration(n)
		case kindString:
			got[n] = e.cmd.String(n)
		}
	}
	return got
}

func probeHandlers(seen map[string]map[string]any) func(map[string]*handler) {
	return func(m map[string]*handler) {
		for path, h := range m {
			path, flags := path, h.flags
			h.run = func(e *env) (*result, error) {
				var fs []urfave.Flag
				if flags != nil {
					fs = flags()
				}
				seen[path] = readAll(e, fs)
				return &result{data: map[string]any{}}, nil
			}
		}
	}
}

// Every boolean, integer and other flag of every command is parsed by the
// real urfave executable path and observed with its set value, and the
// Service/MCP adapter yields identical values for the same inputs.
func TestEveryFlagExecutableAndServiceParity(t *testing.T) {
	exe := map[string]map[string]any{}
	svcSeen := map[string]map[string]any{}
	a := &App{Stdout: &strings.Builder{}, Stderr: &strings.Builder{}, Stdin: strings.NewReader(""),
		Getenv: func(string) string { return "" }, Now: time.Now, Version: "test"}
	a.wrapHandlers = probeHandlers(exe)
	svc := NewService("test", os.Stderr, func(string) string { return "" })
	svc.AllowMutations(true)
	svc.app.wrapHandlers = probeHandlers(svcSeen)

	nonString := 0
	for path, h := range a.commandTable() {
		if h.flags == nil {
			continue
		}
		args := strings.Split(path, " ")
		given := map[string]any{}
		want := map[string]any{}
		for _, f := range h.flags() {
			n := f.Names()[0]
			k, ok := kindOf(f)
			if !ok {
				t.Fatalf("%s --%s: unsupported flag type %T", path, n, f)
			}
			v, text := probeValue(k)
			if choices := h.enums[n]; len(choices) > 0 {
				v, text = choices[0], choices[0]
			}
			if k == kindBool {
				args = append(args, "--"+n)
			} else {
				args = append(args, "--"+n, text)
			}
			if k != kindString {
				nonString++
			}
			given[n], want[n] = v, v
		}
		if code := a.Run(args); code != 0 {
			t.Fatalf("%s: exit %d", path, code)
		}
		if !reflect.DeepEqual(exe[path], want) {
			t.Errorf("executable %s: got %v want %v", path, exe[path], want)
		}
		if !svc.allowed(path) {
			continue
		}
		if _, err := svc.Call(context.Background(), path, Globals{}, given, nil); err != nil {
			t.Fatalf("service %s: %v", path, err)
		}
		if !reflect.DeepEqual(svcSeen[path], exe[path]) {
			t.Errorf("parity %s: service %v executable %v", path, svcSeen[path], exe[path])
		}
	}
	if nonString < 10 {
		t.Fatalf("only %d non-string flags probed", nonString)
	}
	for _, p := range []string{"orders prepare", "orders cancel", "skills install", "positions leverage"} {
		if _, ok := exe[p]; !ok {
			t.Errorf("%s not probed", p)
		}
	}
}

// A wrong-typed read fails closed instead of returning a zero value.
func TestWrongTypedFlagReadFailsClosed(t *testing.T) {
	a := &App{Stdout: &strings.Builder{}, Stderr: &strings.Builder{}, Stdin: strings.NewReader(""),
		Getenv: func(string) string { return "" }, Now: time.Now, Version: "test"}
	a.wrapHandlers = func(m map[string]*handler) {
		m["orders prepare"].run = func(e *env) (*result, error) {
			_ = e.cmd.String("reduce-only")
			return &result{}, nil
		}
	}
	out := &strings.Builder{}
	a.Stdout = out
	if code := a.Run([]string{"orders", "prepare", "--reduce-only", "--json"}); code == 0 || !strings.Contains(out.String(), "INTERNAL") {
		t.Fatalf("wrong-typed read not refused: %d %s", code, out.String())
	}
}

// Static audit: every flag read in package sources uses the accessor that
// matches the flag's declared type.
func TestStaticFlagReadsMatchDeclaredTypes(t *testing.T) {
	a := &App{}
	kinds := map[string]map[flagKind]bool{}
	add := func(fs []urfave.Flag) {
		for _, f := range fs {
			k, _ := kindOf(f)
			for _, n := range f.Names() {
				if kinds[n] == nil {
					kinds[n] = map[flagKind]bool{}
				}
				kinds[n][k] = true
			}
		}
	}
	for _, h := range a.commandTable() {
		if h.flags != nil {
			add(h.flags())
		}
	}
	add(a.commandTree("x", false).Flags)
	for n, ks := range kinds {
		if len(ks) != 1 {
			t.Errorf("flag --%s declared with several types %v", n, ks)
		}
	}
	files, _ := filepath.Glob("*.go")
	reads := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := gotoken.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			var method string
			argIdx := 0
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				recv, ok := fn.X.(*ast.SelectorExpr)
				id, ok2 := fn.X.(*ast.Ident)
				if !(ok && recv.Sel.Name == "cmd") && !(ok2 && id.Name == "cmd") {
					return true
				}
				method = fn.Sel.Name
			case *ast.Ident:
				if fn.Name != "str" || len(call.Args) != 2 {
					return true
				}
				method, argIdx = "String", 1
			default:
				return true
			}
			lit, ok := call.Args[argIdx].(*ast.BasicLit)
			if !ok || lit.Kind != gotoken.STRING {
				return true
			}
			name, _ := strconv.Unquote(lit.Value)
			switch flagKind(method) {
			case kindString, kindBool, kindInt, kindInt64, kindDuration:
			default:
				return true
			}
			reads++
			if !kinds[name][flagKind(method)] {
				var have []string
				for k := range kinds[name] {
					have = append(have, string(k))
				}
				sort.Strings(have)
				t.Errorf("%s: --%s read via %s, declared %v", fset.Position(call.Pos()), name, method, have)
			}
			return true
		})
	}
	if reads < 30 {
		t.Fatalf("audit found only %d reads; parser pattern broken", reads)
	}
}
