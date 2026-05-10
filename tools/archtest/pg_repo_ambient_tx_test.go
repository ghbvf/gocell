// invariants:
//   - INVARIANT: PG-REPO-AMBIENT-TX-01
//
// Package archtest — PG-REPO-AMBIENT-TX-01.
//
// Write-path methods on PostgreSQL-backed repositories must route through an
// ambient-tx-aware helper (typically `s.execCtx` / `s.queryRowCtx` from
// `adapters/postgres/refresh_store.go`-style implementations, OR through
// `txRunner.RunInTx`). Direct `s.pool.Exec` / `s.pool.QueryRow` /
// `s.pool.Query` / `s.pool.Begin` calls inside a write method bypass the
// caller's ambient tx and break ADR-credential D5 same-tx revoke + L2
// outbox atomicity.
//
// Read-only methods (Get / List / Probe / Health / Count / Detect) MAY use
// the pool directly — they don't participate in ambient-tx semantics.
//
// AI-rebust tier: **Medium** (.claude/rules/gocell/ai-collab.md §载体决策原则
// #3). Type-aware: the bypass is identified by resolving the call receiver's
// type to `*pgxpool.Pool` via `go/types`, not by string-matching a field name.
// A future repo using `pgPool *pgxpool.Pool` (different field name) is still
// caught.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// pgRepoFiles enumerates the PG-backed repository files this rule covers.
// Adding a new PG repo means adding it here AND keeping the helper allowlist
// (s.execCtx / s.queryRowCtx) in sync. Both adapters/postgres and cell-private
// adapters are included since the tx-extraction key
// (kernel/persistence.TxCtxKey) is shared across the layer boundary.
var pgRepoFiles = map[string]struct{}{
	"adapters/postgres/session_store.go":                       {},
	"adapters/postgres/refresh_store.go":                       {},
	"adapters/postgres/outbox_store.go":                        {},
	"cells/accesscore/internal/adapters/postgres/user_repo.go": {},
	"cells/accesscore/internal/adapters/postgres/role_repo.go": {},
}

// pgWriteMethodPrefixes flags a method as write-path. Prefix-match keeps the
// rule extension-friendly without an explicit allowlist per-method; methods
// not matching these prefixes are read-only and may touch pool directly.
var pgWriteMethodPrefixes = []string{
	"Create",
	"Insert",
	"Update",
	"Delete",
	"Revoke",
	"Assign",
	"Remove",
	"Mark",
	"Claim",
	"Reclaim",
}

// pgPoolBypassCalls is the set of pool-method names that bypass ambient tx
// when invoked directly on a *pgxpool.Pool. The ambient-tx-aware helpers
// (s.execCtx / s.queryRowCtx) wrap these and consult ctx for an existing
// pgx.Tx before falling through to pool.
var pgPoolBypassCalls = map[string]struct{}{
	"Exec":     {},
	"Query":    {},
	"QueryRow": {},
	"Begin":    {},
}

const (
	pgxpoolImportPath = "github.com/jackc/pgx/v5/pgxpool"
	pgxpoolTypeName   = "Pool"
)

// pgRepoPackagePatterns lists the import patterns whose production .go files
// the archtest must parse with full TypesInfo. Limiting the load to two
// directories keeps the test fast (vs. module-wide scan).
var pgRepoPackagePatterns = []string{
	"github.com/ghbvf/gocell/adapters/postgres",
	"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres",
}

// TestPGRepoAmbientTx guards PG-REPO-AMBIENT-TX-01: write-path methods on
// PostgreSQL-backed repositories must respect ambient transactions.
//
// Type-aware: the bypass detection resolves the receiver of the bypass call
// to *pgxpool.Pool via go/types, not by string-matching a field name.
func TestPGRepoAmbientTx(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads PG repo packages with TypesInfo, ~2-3s)")
	}

	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, false, nil, pgRepoPackagePatterns...)
	require.NoError(t, err)
	require.NotNil(t, resolver, "SharedResolver must return a non-nil resolver")

	var violations []string
	visited := map[string]bool{}

	packages.Visit(resolver.Packages(), nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true
			rel, ok := relPath(root, abs)
			if !ok {
				continue
			}
			if _, watched := pgRepoFiles[rel]; !watched {
				continue
			}
			violations = append(violations, scanPGRepoFileTyped(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"PG-REPO-AMBIENT-TX-01: write-method bodies must route via ambient-tx "+
			"aware helpers (execCtx / queryRowCtx) or txRunner.RunInTx; direct "+
			"*pgxpool.Pool method calls bypass the caller's ambient transaction.")
}

// relPath converts an absolute path under root into a forward-slash relative
// path. Returns ("", false) when path does not lie under root.
func relPath(root, abs string) (string, bool) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// scanPGRepoFileTyped inspects each method on file's repo type. Write-path
// method bodies are checked against the bypass-call rule using TypesInfo to
// resolve the receiver of every CallExpr selector.
func scanPGRepoFileTyped(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []string {
	var out []string
	scanner.EachNode[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Recv == nil || fn.Body == nil {
			return
		}
		if !isPGWriteMethod(fn.Name.Name) {
			return
		}
		scanner.EachNode[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			if _, isBypass := pgPoolBypassCalls[sel.Sel.Name]; !isBypass {
				return
			}
			if !isPgxPoolReceiver(sel.X, info) {
				return
			}
			pos := fset.Position(sel.Sel.Pos())
			out = append(out, fmt.Sprintf(
				"%s:%d: write-method %s.%s calls *pgxpool.Pool.%s directly; "+
					"route via execCtx/queryRowCtx (ambient-tx aware) or txRunner.RunInTx",
				rel, pos.Line, receiverTypeName(fn), fn.Name.Name, sel.Sel.Name))
		})
	})
	return out
}

// isPGWriteMethod reports whether the method name starts with a known
// write-prefix.
func isPGWriteMethod(name string) bool {
	for _, prefix := range pgWriteMethodPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// isPgxPoolReceiver reports whether expr's resolved type is *pgxpool.Pool
// (or pgxpool.Pool — the helper handles both pointer and value receivers
// even though pgxpool.Pool is conventionally a pointer). Returns false when
// TypesInfo is missing or expr's type is not a named type from the
// pgxpool import path.
func isPgxPoolReceiver(expr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	t := tv.Type
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == pgxpoolImportPath && obj.Name() == pgxpoolTypeName
}

// receiverTypeName returns the bare type name of fn's receiver, stripping
// any leading * pointer marker. Used solely for error messages.
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}
