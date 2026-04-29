package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PROD-DURATION-CONST-01 — production code must not contain literal duration
// expressions. Every `time.Sleep / time.After / time.NewTimer / time.NewTicker`
// argument and `context.WithTimeout / context.WithDeadline / context.AfterFunc`
// duration argument must be a named const or a struct field reference.
//
// Rationale: literal durations like `5*time.Second` lack semantic naming, are
// invisible to grep at adjustment time, and silently drift across copy-pastes.
// Naming centralises the value at the package top so reviewers and operators
// can see all knobs at a glance, and `archtest` permanently keeps the gate
// closed.
//
// Scope: production *.go files only. The following are excluded:
//   - `_test.go` files (covered by PR-CI-4 TEST-NO-SLEEP-01)
//   - `tools/archtest/` (self-referential)
//   - `examples/` (out of scope per PR-CI-6 plan)
//   - `vendor/` `.git/` `generated/` `testdata/` `worktrees/` `node_modules/`
//   - `**/locktest/` `**/outboxtest/` `**/conformance.go` (driver test helpers
//     where physical sleep is the test semantic itself)
const prodDurationConstRule = "PROD-DURATION-CONST-01"

type prodDurationViolation struct {
	File     string
	Line     int
	CallForm string
	Literal  string
}

func (v prodDurationViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s(%s) — extract to package-level const",
		prodDurationConstRule, v.File, v.Line, v.CallForm, v.Literal)
}

func TestProdDurationConst(t *testing.T) {
	root := findModuleRoot(t)

	files, err := collectProdDurationScanFiles(root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "no production .go files found from %s", root)

	var violations []prodDurationViolation
	for _, file := range files {
		fileViolations, err := scanProdDurationFile(root, file)
		require.NoError(t, err)
		violations = append(violations, fileViolations...)
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})
	for _, v := range violations {
		t.Log(v.String())
	}
	assert.Empty(t, violations,
		"PROD-DURATION-CONST-01: extract literal durations to package-level "+
			"const (covers time.Sleep/After/NewTimer/NewTicker, "+
			"context.WithTimeout/WithDeadline/AfterFunc, time.Now().Add). "+
			"See docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6.")
}

// collectProdDurationScanFiles walks root and returns production .go files
// subject to the duration-literal rule.
func collectProdDurationScanFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "generated", "testdata",
				"examples", "worktrees", "node_modules",
				"locktest", "outboxtest":
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr == nil && filepath.ToSlash(rel) == "tools/archtest" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Path-suffix match so any package's conformance.go test-helper is
		// excluded regardless of nesting depth (driver-conformance suites
		// own their literal sleeps as test semantics).
		if strings.HasSuffix(filepath.ToSlash(path), "/conformance.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

// scanProdDurationFile parses path and returns any duration-literal violations.
func scanProdDurationFile(root, path string) ([]prodDurationViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.ToSlash(path), err)
	}
	rel, relErr := filepath.Rel(root, path)
	if relErr != nil {
		rel = path
	}
	relSlash := filepath.ToSlash(rel)

	var violations []prodDurationViolation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callForm, argIdx, ok := classifyDurationCall(call)
		if !ok {
			return true
		}
		if argIdx >= len(call.Args) {
			return true
		}
		arg := call.Args[argIdx]
		// context.WithDeadline takes a time.Time; only flag when it wraps a
		// literal-bearing time.Now().Add(<literal>) pattern.
		if callForm == "context.WithDeadline" {
			inner, ok := unwrapTimeNowAdd(arg)
			if !ok {
				return true
			}
			arg = inner
		}
		if !isLiteralDuration(arg) {
			return true
		}
		pos := fset.Position(arg.Pos())
		violations = append(violations, prodDurationViolation{
			File:     relSlash,
			Line:     pos.Line,
			CallForm: callForm,
			Literal:  literalString(arg),
		})
		return true
	})
	return violations, nil
}

// classifyDurationCall returns (callName, argIndex, true) if call matches a
// known duration-bearing function. Otherwise returns ("", 0, false).
func classifyDurationCall(call *ast.CallExpr) (string, int, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", 0, false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", 0, false
	}
	switch pkgIdent.Name {
	case "time":
		switch sel.Sel.Name {
		case "Sleep", "After", "NewTimer", "NewTicker":
			return "time." + sel.Sel.Name, 0, true
		}
	case "context":
		switch sel.Sel.Name {
		case "WithTimeout", "WithDeadline":
			return "context." + sel.Sel.Name, 1, true
		case "AfterFunc":
			return "context." + sel.Sel.Name, 0, true
		}
	}
	return "", 0, false
}

// unwrapTimeNowAdd recognises `time.Now().Add(<expr>)` and returns <expr>, true.
func unwrapTimeNowAdd(expr ast.Expr) (ast.Expr, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Add" {
		return nil, false
	}
	inner, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	innerSel, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok || innerSel.Sel.Name != "Now" {
		return nil, false
	}
	pkg, ok := innerSel.X.(*ast.Ident)
	if !ok || pkg.Name != "time" {
		return nil, false
	}
	return call.Args[0], true
}

// isLiteralDuration reports whether expr contains a literal-bearing duration
// (BasicLit, or a BinaryExpr/ParenExpr/UnaryExpr whose leaves include one).
// Identifiers (named consts) and plain SelectorExprs (e.g. `pkg.Const`,
// `c.field`) are compliant. Most CallExprs returning a duration are compliant
// because their value is computed at runtime — except `time.Duration(<lit>)`
// type-conversion form, which is a literal in disguise and would otherwise
// allow `time.Duration(30) * time.Second` to bypass the gate.
func isLiteralDuration(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return true
	case *ast.BinaryExpr:
		return isLiteralDuration(e.X) || isLiteralDuration(e.Y)
	case *ast.ParenExpr:
		return isLiteralDuration(e.X)
	case *ast.UnaryExpr:
		return isLiteralDuration(e.X)
	case *ast.CallExpr:
		// Only `time.Duration(<expr>)` type conversion participates in the
		// literal check — any other CallExpr (e.g. min/max, helper funcs)
		// returns a runtime value and is compliant by design.
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok &&
				pkg.Name == "time" && sel.Sel.Name == "Duration" &&
				len(e.Args) == 1 {
				return isLiteralDuration(e.Args[0])
			}
		}
	}
	return false
}

// literalString renders expr back to a compact human-readable form for the
// violation report.
func literalString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Value
	case *ast.BinaryExpr:
		return literalString(e.X) + e.Op.String() + literalString(e.Y)
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name + "." + e.Sel.Name
		}
		return e.Sel.Name
	case *ast.Ident:
		return e.Name
	case *ast.ParenExpr:
		return "(" + literalString(e.X) + ")"
	case *ast.UnaryExpr:
		return e.Op.String() + literalString(e.X)
	case *ast.CallExpr:
		// Render `time.Duration(<arg>)` literally so reports stay readable;
		// other CallExprs aren't classified as literal so they shouldn't
		// reach this branch.
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok {
				if len(e.Args) == 1 {
					return pkg.Name + "." + sel.Sel.Name + "(" + literalString(e.Args[0]) + ")"
				}
			}
		}
	}
	return "<expr>"
}
