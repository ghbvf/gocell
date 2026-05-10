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
// Why type-aware (Medium): the constraint is about call form, not import or
// signature. typed Go signature alone cannot express "pool.Exec is forbidden
// here but allowed there"; archtest with AST walk + receiver-field
// resolution is the lowest-cost enforceable carrier (.claude/rules/gocell/
// ai-collab.md §载体决策原则 #3).
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// pgRepoFiles enumerates the PG-backed repository files this rule covers.
// Adding a new PG repo means adding it here AND keeping the write-method
// allowlist of helpers in sync. Both adapters/postgres and cell-private
// adapters are included since the tx-extraction key
// (kernel/persistence.TxCtxKey) is shared across the layer boundary.
var pgRepoFiles = []string{
	"adapters/postgres/session_store.go",
	"adapters/postgres/refresh_store.go",
	"adapters/postgres/outbox_store.go",
	"cells/accesscore/internal/adapters/postgres/user_repo.go",
	"cells/accesscore/internal/adapters/postgres/role_repo.go",
}

// pgWriteMethodPrefixes flags a method as write-path. List intentionally
// short — the rule is "if you write, route via ambient-tx helper". The
// suite's read-path exclusion list (Get / List / Find / Detect / Health)
// is implicit — methods not matching these prefixes are read-only and
// allowed to touch pool directly.
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
// when invoked directly on s.pool. The ambient-tx-aware helpers
// (s.execCtx / s.queryRowCtx) wrap these and consult ctx for an existing
// pgx.Tx before falling through to pool.
var pgPoolBypassCalls = map[string]struct{}{
	"Exec":     {},
	"Query":    {},
	"QueryRow": {},
	"Begin":    {},
}

// repoRoot returns the absolute path to the GoCell repo root, derived from
// the running test binary location. archtest packages execute from
// tools/archtest at module-root depth 2.
func pgRepoRepoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs cwd: %v", err)
	}
	// tools/archtest is two levels below repo root.
	return filepath.Clean(filepath.Join(cwd, "..", ".."))
}

// TestPGRepoAmbientTx guards PG-REPO-AMBIENT-TX-01: write-path methods on
// PostgreSQL-backed repositories must respect ambient transactions.
func TestPGRepoAmbientTx(t *testing.T) {
	t.Parallel()
	root := pgRepoRepoRoot(t)
	fset := token.NewFileSet()

	for _, rel := range pgRepoFiles {
		path := filepath.Join(root, rel)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Errorf("%s: parse: %v", rel, err)
			continue
		}
		auditPGRepoFile(t, fset, rel, file)
	}
}

// auditPGRepoFile inspects each method on the file's repo type. Write-path
// method bodies are checked against the bypass-call rule.
func auditPGRepoFile(t *testing.T, fset *token.FileSet, rel string, file *ast.File) {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		if !isPGWriteMethod(fn.Name.Name) {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if !isDirectPoolBypass(sel) {
				return true
			}
			pos := fset.Position(sel.Sel.Pos())
			t.Errorf(
				"PG-REPO-AMBIENT-TX-01: %s:%d: write-method %s.%s calls pool.%s directly; "+
					"route via execCtx/queryRowCtx (ambient-tx aware) or txRunner.RunInTx",
				rel, pos.Line, receiverTypeName(fn), fn.Name.Name, sel.Sel.Name)
			return true
		})
	}
}

// isPGWriteMethod reports whether the method name starts with a known
// write-prefix. Prefix-match instead of full-name to keep the rule
// extension-friendly without an explicit allowlist per-method.
func isPGWriteMethod(name string) bool {
	for _, prefix := range pgWriteMethodPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// isDirectPoolBypass reports whether sel is `<recv>.pool.<Bypass>` where
// Bypass is one of Exec / Query / QueryRow / Begin. The receiver name
// (typically "s") is not pinned — any nested-selector to a "pool" field
// followed by a bypass call counts.
func isDirectPoolBypass(sel *ast.SelectorExpr) bool {
	if _, ok := pgPoolBypassCalls[sel.Sel.Name]; !ok {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return inner.Sel.Name == "pool"
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
