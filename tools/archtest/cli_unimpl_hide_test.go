// INVARIANT: CLI-UNIMPL-HIDE-01
//
// No `gocell` sub-command may be visible in help while being
// unimplemented (B2-X-05 / cap-14 CLI-UNIMPL-HIDE-01). The four
// help-bearing verb trees — generate / verify / scaffold / check — each
// own a single typed registry (cmd/gocell/app/subcommand.go's
// subcommand[H]); dispatch and help BOTH derive from that one slice, so a
// type cannot appear in one truth and not the other.
//
// This archtest makes that single-source structurally enforced rather
// than convention. It binds three structural facts in cmd/gocell/app
// production code:
//
//   - Upstream Hard: the four dispatch functions (runGenerate, runVerify,
//     runScaffoldWithRoot, runCheck) contain NO `switch` statement and DO
//     call findSub — i.e. dispatch is registry lookup, never a
//     string-literal `case "name":` ladder. Re-introducing a switch (the
//     pre-PR shape that let `generate indexes` exist) fails CI.
//   - Downstream Hard: no `helpEntry{…}` composite literal anywhere in
//     production code carries a string-literal `name`. The only path from
//     a registry to a helpEntry is renderSubHelp, which sets
//     `name: s.name` (a selector, never a literal). A hand-written help
//     list — the other half of the old drift — fails CI.
//   - No placeholder: production code contains no reachable
//     "not implemented" string return. An unimplemented type is absent
//     from its registry and falls through to the unknown-type error,
//     exactly like a typo.
//
// AI-rebust: Hard (closed-loop funnel). Upstream Hard (no switch + must
// call findSub) and downstream Hard (no literal-name helpEntry) together
// make "visible-but-unimplemented sub-command" inexpressible in the four
// trees. Reverse-fixture self-checks below prove each detector actually
// fires (a detector that silently passes everything would itself be the
// bypass).
//
// # Tool / blind-spot ledger (charter §载体决策原则)
//
// Detection is pure-AST (archtest.Run, no go/types) because the rule's
// truth is structural (presence of a SwitchStmt / a literal-name
// helpEntry / a "not implemented" literal), not type-resolution. AST
// forms outside the chosen matchers, each with a reverse self-check:
//
//  1. Declared blind spot — dispatch.go PrintUsage is free-form prose
//     listing the seven top-level commands; it is NOT a subcommand
//     registry, so the four-tree funnel does not cover it. Compensated by
//     assertPrintUsageNoStaleToken (no "indexes" / "not implemented"
//     token) and tracked for single-sourcing by backlog
//     CLI-TOPLEVEL-HELP-REGISTRY-01. Top-level commands map ↔ PrintUsage
//     drift is the explicit follow-up, not silent carryover.
//  2. helpEntry built with a named field (`helpEntry{name: …}`) vs
//     positional (`helpEntry{…}`) — both handled (KeyValueExpr key and
//     positional element 0). Fixture: namedfield.
//  3. A "not implemented" produced via a helper call rather than a
//     literal return — the registry shape has no such path; fixture
//     `placeholder` asserts the literal form is caught, and the absence
//     of any indirection is guaranteed by the no-switch + findSub facts
//     (an unregistered type cannot reach a handler at all).
//  4. runExport's two-value `catalog|metadata` alias switch is
//     intentionally OUT of scope: export has no helpEntry surface, so it
//     cannot drift help vs dispatch. dispatchFuncs lists exactly the four
//     help-bearing trees.
//
// ref: docs/plans/202605121830-038-p0-p1-blocking-implementation-plan.md PR-3
// ref: cmd/gocell/app/subcommand.go (the funnel this test guards)
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// dispatchFuncs are the four help-bearing verb-tree dispatchers. Each must
// route through the subcommand registry (findSub) and contain no switch.
// runExport is deliberately absent (no helpEntry surface — see ledger §4).
var dispatchFuncs = map[string]bool{
	"runGenerate":         true,
	"runVerify":           true,
	"runScaffoldWithRoot": true,
	"runCheck":            true,
}

// TestCLIUnimplHide01 binds the structural single-source facts in
// cmd/gocell/app production code.
func TestCLIUnimplHide01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	diags := Run(t, DirsScope(root, []string{"cmd/gocell/app"}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue // production-only invariant
				}
				d = append(d, scanDispatchSwitchFree(p, f, rel)...)
				d = append(d, scanHelpEntryNoLiteralName(p, f, rel)...)
				d = append(d, scanNoNotImplementedLiteral(p, f, rel)...)
			}
			return d
		})
	Report(t, "CLI-UNIMPL-HIDE-01", diags)
}

// scanDispatchSwitchFree enforces the upstream-Hard fact: each dispatch
// function has zero SwitchStmt and at least one findSub call.
func scanDispatchSwitchFree(p *Pass, f *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Name == nil || !dispatchFuncs[fn.Name.Name] || fn.Body == nil {
			return
		}
		var hasSwitch, callsFindSub bool
		EachInSubtree[ast.SwitchStmt](fn.Body, func(*ast.SwitchStmt) { hasSwitch = true })
		EachInSubtree[ast.CallExpr](fn.Body, func(c *ast.CallExpr) {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "findSub" {
				callsFindSub = true
			}
		})
		if hasSwitch {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(fn.Pos()).Line,
				Message: fn.Name.Name + " dispatches via switch; the four verb " +
					"trees must dispatch through the subcommand registry (findSub) " +
					"so help and dispatch cannot drift",
			})
		}
		if !callsFindSub {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(fn.Pos()).Line,
				Message: fn.Name.Name + " does not call findSub; dispatch must " +
					"resolve the handler through its subcommand registry",
			})
		}
	})
	return d
}

// scanHelpEntryNoLiteralName enforces the downstream-Hard fact: no
// helpEntry composite literal carries a string-literal name. renderSubHelp
// builds helpEntry{name: s.name, …} from the registry — a literal name is
// a hand-written help list (the deleted printXxxHelp shape).
func scanHelpEntryNoLiteralName(p *Pass, f *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	flag := func(cl *ast.CompositeLit) {
		v := helpEntryNameValue(cl)
		lit, ok := v.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		d = append(d, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(cl.Pos()).Line,
			Message: "hand-written helpEntry with string-literal name " +
				lit.Value + "; help must derive from a subcommand registry " +
				"via renderSubHelp (name: s.name), never a literal list",
		})
	}
	EachInSubtree[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
		switch {
		case isHelpEntryIdent(cl.Type):
			// Explicit `helpEntry{…}` (e.g. renderSubHelp's per-element
			// construction — legitimate when name is a selector).
			flag(cl)
		case isHelpEntrySliceOrArray(cl.Type):
			// `[]helpEntry{ {…}, {…} }` — inner elements carry an elided
			// type (cl.Type == nil), so inspect each direct child.
			EachInChildren[ast.CompositeLit](cl, flag)
		}
	})
	return d
}

// isHelpEntrySliceOrArray reports whether e is `[]helpEntry` or
// `[N]helpEntry` — the container whose elements elide their type.
func isHelpEntrySliceOrArray(e ast.Expr) bool {
	at, ok := e.(*ast.ArrayType)
	return ok && isHelpEntryIdent(at.Elt)
}

// scanNoNotImplementedLiteral enforces: no production string literal
// announces an unimplemented sub-command. The registry shape expresses
// "unimplemented" as absence, not a placeholder branch.
func scanNoNotImplementedLiteral(p *Pass, f *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING {
			return
		}
		if strings.Contains(strings.ToLower(lit.Value), "not implemented") {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(lit.Pos()).Line,
				Message: "string literal " + lit.Value + " announces an " +
					"unimplemented sub-command; an unimplemented type must be " +
					"absent from its registry, not a placeholder branch",
			})
		}
	})
	return d
}

func isHelpEntryIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "helpEntry"
}

// helpEntryNameValue returns the AST value assigned to the helpEntry "name"
// field, handling both `helpEntry{name: X, …}` (KeyValueExpr) and
// `helpEntry{X, …}` (positional, name is field 0).
func helpEntryNameValue(cl *ast.CompositeLit) ast.Expr {
	if len(cl.Elts) == 0 {
		return nil
	}
	if _, keyed := cl.Elts[0].(*ast.KeyValueExpr); !keyed {
		return cl.Elts[0] // positional: name is the first field
	}
	var nameVal ast.Expr
	EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "name" {
			nameVal = kv.Value
		}
	})
	return nameVal
}

// --- Reverse self-checks: prove each detector actually fires ----------------
//
// A detector that silently passes everything would itself be the bypass.
// Each fixture under testdata/cli_unimpl_hide/ contains exactly one
// deliberate violation; the matching detector must flag it.

func parseFixture(t *testing.T, name string) (*Pass, *ast.File, string) {
	t.Helper()
	rel := filepath.Join("testdata", "cli_unimpl_hide", name)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return &Pass{Fset: fset, Files: []*ast.File{f}}, f, rel
}

func TestCLIUnimplHide01_DetectsSwitchDispatch(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "switch_dispatch.go")
	if got := scanDispatchSwitchFree(p, f, rel); len(got) == 0 {
		t.Fatal("expected switch-dispatch fixture to be flagged, got 0 diagnostics")
	}
}

func TestCLIUnimplHide01_DetectsLiteralHelpEntry(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "literal_helpentry.go")
	got := scanHelpEntryNoLiteralName(p, f, rel)
	if len(got) == 0 {
		t.Fatal("expected literal-name helpEntry fixture to be flagged, got 0")
	}
	// Named-field form (blind-spot ledger §2) must also be caught.
	if !containsMsg(got, "namedKey") || !containsMsg(got, "positional0") {
		t.Errorf("both named-field and positional helpEntry forms must be flagged; got %v", got)
	}
}

func TestCLIUnimplHide01_DetectsNotImplementedLiteral(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "placeholder.go")
	if got := scanNoNotImplementedLiteral(p, f, rel); len(got) == 0 {
		t.Fatal("expected 'not implemented' literal fixture to be flagged, got 0")
	}
}

func containsMsg(diags []Diagnostic, sub string) bool {
	for _, d := range diags {
		if strings.Contains(d.Message, sub) {
			return true
		}
	}
	return false
}

// TestCLIUnimplHide01_PrintUsageNoStaleToken is the declared-blind-spot
// compensation (ledger §1): dispatch.go's free-form PrintUsage is not a
// registry, so the four-tree funnel does not bind it. This guards the one
// concrete drift that mattered — a stale "indexes" / "not implemented"
// token surviving in the top-level usage prose — until the top-level
// commands map ↔ PrintUsage single-sourcing follow-up
// (backlog CLI-TOPLEVEL-HELP-REGISTRY-01) lands.
func TestCLIUnimplHide01_PrintUsageNoStaleToken(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fset := token.NewFileSet()
	abs := filepath.Join(root, "cmd", "gocell", "app", "dispatch.go")
	f, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse dispatch.go: %v", err)
	}
	EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Name.Name != "PrintUsage" || fn.Body == nil {
			return
		}
		EachInSubtree[ast.BasicLit](fn.Body, func(lit *ast.BasicLit) {
			if lit.Kind != token.STRING {
				return
			}
			low := strings.ToLower(lit.Value)
			if strings.Contains(low, "indexes") || strings.Contains(low, "not implemented") {
				t.Errorf("PrintUsage carries stale token %s; the removed "+
					"`generate indexes` type must not resurface in top-level "+
					"usage prose (CLI-TOPLEVEL-HELP-REGISTRY-01)", lit.Value)
			}
		})
	})
}
