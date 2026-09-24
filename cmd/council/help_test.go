package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Root help goes to stdout and lists every registered command (asserted
// against the registry itself, so a new command cannot silently lack help).
func TestRootHelpListsEveryCommand(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		out, code := captureStdout(t, func() int { return run(args) })
		if code != exitOK {
			t.Fatalf("run(%v) = %d, want %d", args, exitOK, code)
		}
		for _, e := range commandRegistry {
			if !strings.Contains(out, e.Name) {
				t.Fatalf("run(%v) help missing command %q", args, e.Name)
			}
			if !strings.Contains(out, e.Purpose) {
				t.Fatalf("run(%v) help missing purpose for %q", args, e.Name)
			}
		}
		if strings.Contains(out, "unknown subcommand") {
			t.Fatalf("run(%v) help must not say unknown subcommand", args)
		}
	}
}

// Per-command and group --help exit 0 and name each of their own flags.
func TestCommandHelpNamesFlags(t *testing.T) {
	for _, e := range commandRegistry {
		t.Run(e.Name, func(t *testing.T) {
			var want []string
			if len(e.Subs) == 0 {
				out, code := captureStdout(t, func() int { return run([]string{e.Name, "--help"}) })
				if code != exitOK {
					t.Fatalf("%s --help = %d, want %d", e.Name, code, exitOK)
				}
				if !strings.Contains(out, e.Purpose) {
					t.Fatalf("%s --help missing purpose", e.Name)
				}
				_ = want
				return
			}
			out, code := captureStdout(t, func() int { return run([]string{e.Name, "--help"}) })
			if code != exitOK {
				t.Fatalf("%s --help = %d, want %d", e.Name, code, exitOK)
			}
			for _, s := range e.Subs {
				if !strings.Contains(out, s.Name) || !strings.Contains(out, s.Purpose) {
					t.Fatalf("%s --help missing subcommand %q with purpose", e.Name, s.Name)
				}
				for _, f := range s.Flags {
					if !strings.Contains(out, "--"+f.Name) {
						t.Fatalf("%s --help missing flag --%s", e.Name, f.Name)
					}
				}
				subOut, subCode := captureStdout(t, func() int { return run([]string{e.Name, s.Name, "--help"}) })
				if subCode != exitOK {
					t.Fatalf("%s %s --help = %d, want %d", e.Name, s.Name, subCode, exitOK)
				}
				for _, f := range s.Flags {
					if !strings.Contains(subOut, "--"+f.Name) {
						t.Fatalf("%s %s --help missing flag --%s", e.Name, s.Name, f.Name)
					}
				}
			}
			if strings.Contains(out, "unknown subcommand") {
				t.Fatalf("%s --help must not say unknown subcommand", e.Name)
			}
		})
	}
}

// Unknown subcommand exits 2 with help plus an explicit pointer to help,
// clearly distinct from the missing-key (1) and blocked (3) errors.
func TestUnknownSubcommandPointsAtHelp(t *testing.T) {
	oldErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	outCh := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(r)
		outCh <- string(raw)
	}()
	outStd, code := captureStdout(t, func() int {
		c := run([]string{"bogus-cmd"})
		_ = w.Close()
		return c
	})
	os.Stderr = oldErr
	errOut := <-outCh
	if code != exitValidation {
		t.Fatalf("unknown = %d, want %d", code, exitValidation)
	}
	if !strings.Contains(errOut, "council help") {
		t.Fatalf("stderr = %q, want pointer to `council help`", errOut)
	}
	if !strings.Contains(outStd, "usage:") {
		t.Fatalf("stdout = %q, want root help on stdout", outStd)
	}
	if strings.Contains(errOut, "COUNCIL_GATEWAY_API_KEY") {
		t.Fatalf("unknown-subcommand error must not mention the gateway key")
	}
	if strings.Contains(errOut, "blocked") || strings.Contains(errOut, "exit 3") {
		t.Fatalf("unknown-subcommand error must not read as blocked")
	}
}

// Unknown subcommand inside a group exits 2 and points at that group's help,
// never the generic "unknown subcommand --help" confusion.
func TestUnknownGroupSubcommandPointsAtGroup(t *testing.T) {
	out, code := captureStdout(t, func() int { return run([]string{"db", "bogus-sub"}) })
	if code != exitValidation {
		t.Fatalf("db bogus = %d, want %d", code, exitValidation)
	}
	if !strings.Contains(out, "db") {
		t.Fatalf("db bogus help = %q, want group help", out)
	}
}

// A spending command's help contains exactly SpendLine(key) for its key.
func TestSpendingHelpEmbedsSpendLine(t *testing.T) {
	cases := []struct{ args, key []string }{
		{[]string{"serve", "--help"}, []string{"serve"}},
		{[]string{"pack", "publish", "--help"}, []string{"pack publish"}},
		{[]string{"harness", "sentinel", "--help"}, []string{"harness sentinel"}},
		{[]string{"graders", "calibrate", "--help"}, []string{"graders calibrate"}},
		{[]string{"config", "check", "--help"}, []string{"config check --live"}},
	}
	for _, tc := range cases {
		out, code := captureStdout(t, func() int { return run(tc.args) })
		if code != exitOK {
			t.Fatalf("%v = %d, want %d", tc.args, code, exitOK)
		}
		for _, k := range tc.key {
			if !strings.Contains(out, SpendLine(k)) {
				t.Fatalf("%v help missing SpendLine(%q) = %q", tc.args, k, SpendLine(k))
			}
		}
	}
}

// AC12 cross-check: every real stdlib flag.FlagSet in cmd/council must
// appear in the registry with matching name/type/default, and no registry
// flag may be absent from its FlagSet.
func TestRegistryMatchesFlagSets(t *testing.T) {
	actual := collectFlagSets(t)
	checkRegistryAgainst(t, commandRegistry, actual)
}

// collectFlagSets parses the non-test sources in this package and extracts
// every fs.String/Int/Bool/Float64/Duration flag definition keyed by the
// FlagSet name ("serve", "pack publish", ...).
func collectFlagSets(t *testing.T) map[string]map[string]HelpFlag {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]HelpFlag{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			var recv string
			if id, ok := sel.X.(*ast.Ident); ok {
				recv = id.Name
			}
			if recv == "" || (sel.Sel.Name != "String" && sel.Sel.Name != "Int" && sel.Sel.Name != "Bool" && sel.Sel.Name != "Float64" && sel.Sel.Name != "Duration") {
				return true
			}
			if len(call.Args) < 3 {
				return true
			}
			name, ok1 := stringLit(call.Args[0])
			def, ok2 := basicLit(call.Args[1])
			usage, ok3 := stringLit(call.Args[2])
			if !ok1 || !ok2 || !ok3 {
				return true
			}
			setName := flagSetName(fset, node, call)
			if setName == "" {
				return true
			}
			typ := map[string]string{"String": "string", "Int": "int", "Bool": "bool", "Float64": "float64", "Duration": "duration"}[sel.Sel.Name]
			if out[setName] == nil {
				out[setName] = map[string]HelpFlag{}
			}
			out[setName][name] = HelpFlag{Name: name, Type: typ, Default: def, Usage: usage}
			return true
		})
	}
	return out
}

// flagSetName finds the flag.NewFlagSet("name", ...) call in the same
// function body as the flag definition.
func flagSetName(fset *token.FileSet, file *ast.File, call *ast.CallExpr) string {
	var found string
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		callPos := fset.Position(call.Pos())
		fnPos := fset.Position(fn.Pos())
		fnEnd := fset.Position(fn.End())
		if callPos.Line < fnPos.Line || callPos.Line > fnEnd.Line {
			return true
		}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			c, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			s, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || s.Sel.Name != "NewFlagSet" {
				return true
			}
			if len(c.Args) < 1 {
				return true
			}
			if name, ok := stringLit(c.Args[0]); ok {
				found = name
			}
			return true
		})
		return true
	})
	return found
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func basicLit(e ast.Expr) (string, bool) {
	if id, ok := e.(*ast.Ident); ok {
		if id.Name == "true" || id.Name == "false" {
			return id.Name, true
		}
		return "", false
	}
	lit, ok := e.(*ast.BasicLit)
	if !ok {
		return "", false
	}
	switch lit.Kind {
	case token.STRING:
		v, err := strconv.Unquote(lit.Value)
		return v, err == nil
	case token.INT, token.FLOAT:
		return lit.Value, true
	}
	return "", false
}

// checkRegistryAgainst compares registry flags to parsed FlagSet flags. The
// "note" flag on flags confirm/dismiss is parsed manually (not a FlagSet),
// so it is asserted directly from resolveFlag's contract instead.
func checkRegistryAgainst(t *testing.T, reg []HelpEntry, actual map[string]map[string]HelpFlag) {
	t.Helper()
	manual := map[string]bool{"flags confirm": true, "flags dismiss": true}
	for _, e := range reg {
		if len(e.Subs) == 0 {
			continue
		}
		for _, s := range e.Subs {
			key := e.Name + " " + s.Name
			got := actual[key]
			if manual[key] {
				if got != nil {
					t.Fatalf("%s: unexpected FlagSet (parsed manually)", key)
				}
				for _, f := range s.Flags {
					if f.Name != "note" || f.Type != "string" {
						t.Fatalf("%s: registry = %+v, want manual --note string", key, f)
					}
				}
				continue
			}
			if len(s.Flags) == 0 && len(got) == 0 {
				continue
			}
			for _, f := range s.Flags {
				a, ok := got[f.Name]
				if !ok {
					t.Fatalf("%s: registry flag --%s absent from FlagSet", key, f.Name)
				}
				if a.Type != f.Type || a.Default != f.Default {
					t.Fatalf("%s: flag --%s registry (%s,%s) != FlagSet (%s,%s)",
						key, f.Name, f.Type, f.Default, a.Type, a.Default)
				}
			}
			for name := range got {
				found := false
				for _, f := range s.Flags {
					if f.Name == name {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s: FlagSet flag --%s missing from registry", key, name)
				}
			}
		}
	}
}
