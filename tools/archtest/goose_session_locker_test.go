package archtest

// invariants:
//   - INVARIANT: GOOSE-SESSION-LOCKER-01
//   - INVARIANT: POSTGRES-MIGRATOR-LOCK-ORDER-REGRESSION-01
//
// goose_session_locker_test.go enforces GOOSE-SESSION-LOCKER-01: every
// goose.NewProvider call site under adapters/postgres/ MUST configure a
// SessionLocker via goose.WithSessionLocker. Without it, concurrent Up() calls
// race on the schema_migrations table (GitHub #21,
// POSTGRES-MIGRATOR-LOCK-ORDER-REGRESSION-01).
//
// Resolution is type-driven via go/packages + types.Info.ObjectOf: every
// CallExpr is resolved to its *types.Func and gated on
// Pkg().Path() == "github.com/pressly/goose/v3" with Name() == "NewProvider"
// (or "WithSessionLocker"). Import aliases ("import g \"…/goose/v3\""), dot
// imports ("import . \"…/goose/v3\""), and same-named symbols from unrelated
// packages are all handled correctly — an identifier-name AST heuristic
// (sel.X.Name == "goose") could not.
//
// Allowlist: keyed on repo-relative path (filepath.ToSlash) so a future move
// of schema_guard.go under a sub-directory does not silently inherit the
// exemption based on basename collision. schema_guard.go is exempt because
// its goose.NewProvider is used only by VerifyExpectedVersion's read-only
// GetDBVersion path; advisory locks add no value on a read-only readiness
// probe and would create connection contention. The mutating Up/Down path
// lives in migrator.go and MUST hold the lock.
//
// ref: pressly/goose lock/postgres.go pg_try_advisory_lock + retry
// ref: dominikh/go-tools analysis/code/code.go CallName / IsCallToAny

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

const (
	ruleGooseSessionLocker01 = "GOOSE-SESSION-LOCKER-01"
	gooseImportPathProd      = "github.com/pressly/goose/v3"
)

// gooseSessionLockerAllowlist maps a repo-relative path (forward-slash) to
// the rationale for exempting that file from the rule. Only the listed paths
// may invoke goose.NewProvider without WithSessionLocker.
var gooseSessionLockerAllowlist = map[string]string{
	"adapters/postgres/schema_guard.go": "VerifyExpectedVersion uses provider.GetDBVersion only (read-only); " +
		"advisory locks add no value on a readiness probe.",
}

// gooseLockerViolation is one offending NewProvider call site.
type gooseLockerViolation struct {
	rel  string
	line int
}

func (v gooseLockerViolation) String() string {
	return fmt.Sprintf("%s:%d  goose.NewProvider must include goose.WithSessionLocker(lock.NewPostgresSessionLocker())", v.rel, v.line)
}

func TestGooseSessionLocker01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	pkgs, errs, err := typeseval.LoadPackages(root, false, nil, "./adapters/postgres/...")
	require.NoError(t, err, "packages.Load adapters/postgres/...")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	violations, allowlistedHits := scanGooseSessionLocker(
		pkgs, root, gooseImportPathProd, gooseSessionLockerAllowlist,
	)

	for rel, reason := range allowlistedHits {
		t.Logf("%s: allowlist hit %s — %s", ruleGooseSessionLocker01, rel, reason)
	}

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleGooseSessionLocker01, len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}
	assert.Empty(t, violations,
		"%s: every mutating goose.NewProvider in adapters/postgres/ must include "+
			"goose.WithSessionLocker; concurrent Up() races without it "+
			"(GH #21 / POSTGRES-MIGRATOR-LOCK-ORDER-REGRESSION-01)",
		ruleGooseSessionLocker01)
}

// scanGooseSessionLocker walks pkgs and returns (violations, allowlistHits).
//
// The matcher is type-driven: each CallExpr.Fun is resolved via
// info.ObjectOf to a *types.Func; only those whose Pkg().Path() equals
// gooseImportPath are considered. A NewProvider call without a sibling
// WithSessionLocker(...) call argument from the same package is a violation.
//
// allowlist is keyed on repo-relative path (forward-slash, relative to
// modRoot). modRoot is the path used to compute file rel-paths.
func scanGooseSessionLocker(
	pkgs []*packages.Package,
	modRoot string,
	gooseImportPath string,
	allowlist map[string]string,
) ([]gooseLockerViolation, map[string]string) {
	var violations []gooseLockerViolation
	allowlistedHits := map[string]string{}
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

			rel, ok := fileroles.Rel(modRoot, abs)
			if !ok {
				continue
			}

			fileViolations := scanGooseSessionLockerFile(p.Fset, file, rel, p.TypesInfo, gooseImportPath)
			if len(fileViolations) == 0 {
				continue
			}
			if reason, exempt := allowlist[rel]; exempt {
				allowlistedHits[rel] = reason
				continue
			}
			violations = append(violations, fileViolations...)
		}
	})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].rel != violations[j].rel {
			return violations[i].rel < violations[j].rel
		}
		return violations[i].line < violations[j].line
	})
	return violations, allowlistedHits
}

// scanGooseSessionLockerFile inspects a single file and returns violations.
func scanGooseSessionLockerFile(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	gooseImportPath string,
) []gooseLockerViolation {
	var out []gooseLockerViolation

	scanner.EachNode[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isGooseFuncCall(info, call, gooseImportPath, "NewProvider") {
			return
		}
		if hasGooseFuncArg(info, call.Args, gooseImportPath, "WithSessionLocker") {
			return
		}
		out = append(out, gooseLockerViolation{
			rel:  rel,
			line: fset.Position(call.Pos()).Line,
		})
	})
	return out
}

// isGooseFuncCall reports whether call is an invocation of the package-level
// function gooseImportPath.funcName, regardless of import alias / dot-import.
func isGooseFuncCall(info *types.Info, call *ast.CallExpr, gooseImportPath, funcName string) bool {
	ident := callFuncIdent(call.Fun)
	if ident == nil {
		return false
	}
	return resolvesToGooseFunc(info, ident, gooseImportPath, funcName)
}

// hasGooseFuncArg reports whether any direct arg in args is a call to
// gooseImportPath.funcName.
func hasGooseFuncArg(info *types.Info, args []ast.Expr, gooseImportPath, funcName string) bool {
	// Paired index iteration: only direct args (immediate children of args)
	// are checked, not arbitrarily nested CallExprs inside an arg's subtree.
	for i := range args {
		argCall, ok := args[i].(*ast.CallExpr)
		if !ok {
			continue
		}
		if isGooseFuncCall(info, argCall, gooseImportPath, funcName) {
			return true
		}
	}
	return false
}

// callFuncIdent returns the identifier that names the function in a
// CallExpr.Fun: the .Sel of a SelectorExpr (pkg.Func), the bare Ident
// (dot-import case), or nil for anything else (e.g. function literal,
// method-on-value).
func callFuncIdent(fun ast.Expr) *ast.Ident {
	switch e := fun.(type) {
	case *ast.SelectorExpr:
		return e.Sel
	case *ast.Ident:
		return e
	}
	return nil
}

// resolvesToGooseFunc checks ident's resolved object: must be a *types.Func
// declared at package scope (no receiver) in package gooseImportPath with
// the requested name.
func resolvesToGooseFunc(info *types.Info, ident *ast.Ident, gooseImportPath, funcName string) bool {
	if info == nil || ident == nil {
		return false
	}
	fn, ok := info.ObjectOf(ident).(*types.Func)
	if !ok {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != gooseImportPath {
		return false
	}
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return false
	}
	return fn.Name() == funcName
}
