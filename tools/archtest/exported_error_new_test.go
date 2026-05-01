// EXPORTED-ERROR-NEW-01 — invariant-driven gate.
//
// Invariant: In all production-shippable .go files outside pkg/errcode/,
// no top-level (package-scope) `var` declaration may bind an exported
// identifier matching the sentinel naming convention `^Err[A-Z]\w*$` to
// an `errors.New(...)` call expression. Use pkg/errcode.New(code, message)
// so the sentinel participates in the wire-protocol error code taxonomy
// and HTTP status mapping (CLAUDE.md: 禁止 errors.New 对外暴露).
//
// Allowed:
//   - var ErrFoo = errcode.New(errcode.CodeFoo, "foo")    // wraps errcode
//   - var errFoo = errors.New("foo")                       // unexported
//   - func Bar() error { return errors.New("local") }      // function-local
//   - var Errno = errors.New(...)                          // 4th rune is
//     lowercase — not the sentinel naming convention
//
// Forbidden:
//   - var ErrFoo = errors.New("foo")                       // must use errcode
//
// Allow-list: pkg/errcode/ may use errors.New (it is the migration target
// and constructs internal sentinels).
//
// File-role classification is delegated to tools/internal/fileroles.
// Aliased imports of "errors" are still caught: the gate resolves the
// SelectorExpr.Sel via TypesInfo.Uses to its declaring *types.Package
// and matches by package path "errors", not by source identifier.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G2
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const ruleExportedErrorNew01 = "EXPORTED-ERROR-NEW-01"

// errcodeAllowlistPath is the canonical home of low-level sentinel errors;
// the gate exempts it because pkg/errcode is the migration destination and
// itself wraps errors.New internally.
const errcodeAllowlistPath = "pkg/errcode/"

// TestExportedErrorNew enforces EXPORTED-ERROR-NEW-01 by walking every
// production-code file (per fileroles.IsProductionCode) outside the
// pkg/errcode/ allow-list and flagging package-scope exported sentinel
// vars whose initializer is `errors.New(...)`.
func TestExportedErrorNew(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production packages module-wide, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	pkgs, errs, err := typeseval.LoadPackages(root, false, []string{"e2e", "integration", "pg"}, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			rel, ok := fileroles.Rel(root, abs)
			if !ok || !fileroles.IsProductionCode(rel) {
				continue
			}
			if strings.HasPrefix(rel, errcodeAllowlistPath) {
				continue
			}
			violations = append(violations, scanExportedErrorNewAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"%s: package-scope exported `var Err* = errors.New(...)` violates "+
			"CLAUDE.md '禁止 errors.New 对外暴露'. Migrate to errcode.New(code, message). "+
			"ref: docs/plans/202605011500-029-master-roadmap.md G2",
		ruleExportedErrorNew01)
}

// scanExportedErrorNewAST returns "<rel>:<line>: <ident>" violation strings
// for a single parsed file.
func scanExportedErrorNewAST(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []string {
	var out []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// A ValueSpec with N names and 1 value is a multi-assign from a
			// single function call; errors.New only returns one value, so
			// such a form would not type-check. We still iterate Values
			// indexed by position to be safe.
			for i, name := range vs.Names {
				if !isExportedErrSentinelName(name.Name) {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				if !isErrorsNewCall(vs.Values[i], info) {
					continue
				}
				pos := fset.Position(name.Pos())
				out = append(out, fmt.Sprintf(
					"%s:%d: %s = errors.New(...) — migrate to errcode.New(code, message)",
					rel, pos.Line, name.Name))
			}
		}
	}
	return out
}

// isExportedErrSentinelName reports whether name follows the exported
// sentinel convention `Err` + uppercase ASCII + zero-or-more word chars.
// Names like Errno / Errors (4th rune lowercase) and Err alone are not
// sentinel-pattern matches and are accepted.
func isExportedErrSentinelName(name string) bool {
	if !strings.HasPrefix(name, "Err") {
		return false
	}
	if len(name) <= 3 {
		return false
	}
	c := name[3]
	return c >= 'A' && c <= 'Z'
}

// isErrorsNewCall reports whether expr is a call to stdlib `errors.New`,
// resolving aliased imports via TypesInfo.Uses.
func isErrorsNewCall(expr ast.Expr, info *types.Info) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if info == nil {
		return false
	}
	obj := info.Uses[sel.Sel]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	if fn.Name() != "New" {
		return false
	}
	pkg := fn.Pkg()
	if pkg == nil {
		return false
	}
	return pkg.Path() == "errors"
}
