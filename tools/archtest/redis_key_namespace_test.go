package archtest

// INVARIANT: REDIS-KEY-NAMESPACE-01
//
// REDIS-KEY-NAMESPACE-01 — invariant-driven gate.
//
// Invariant: every public constructor in adapters/redis that produces a
// keyspace-using object MUST take a KeyNamespace parameter and validate
// it at construction time. The four constructors are
//
//   - NewIdempotencyClaimer (idempotency.go)
//   - NewCache              (cache.go)
//   - NewNonceStore         (nonce.go)
//   - NewRedisDriver        (distlock.go)
//
// Each constructor must:
//
//  1. Accept a parameter of type `KeyNamespace`.
//  2. Return `(*T, error)` (error-first, so the caller observes namespace
//     validation failure at composition time rather than at runtime).
//  3. Have a top-level `if err := <ns>.Validate(); err != nil { return ... }`
//     guard that rejects invalid namespaces before any field assignment.
//
// The rule is funnel-impossible:
//   - codegen would require a marker schema for each constructor; pure
//     overhead vs. a one-rule AST gate.
//   - the Go type system already enforces the parameter via direct method
//     receiver; this archtest is the "did you remember the Validate()
//     call" guard that the type system cannot express.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B11
// ref: backlog2 §5.3 B2-A-27 REDIS-MULTI-TENANT-KEY-COLLISION

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// redisConstructorSpec describes one expected constructor. file is relative
// to the adapters/redis package; fnName is the exported symbol; takesClient
// records whether the first parameter is the *Client (Cache/Nonce/Idempotency)
// rather than a raw cmdable (RedisDriver).
type redisConstructorSpec struct {
	file        string
	fnName      string
	takesClient bool
}

// redisConstructors enumerates the four constructors required to take a
// KeyNamespace. Adding a fifth Redis primitive is a deliberate event: extend
// this list and the gate ensures the new constructor follows the same shape.
var redisConstructors = []redisConstructorSpec{
	{file: "idempotency.go", fnName: "NewIdempotencyClaimer", takesClient: true},
	{file: "cache.go", fnName: "NewCache", takesClient: true},
	{file: "nonce.go", fnName: "NewNonceStore", takesClient: true},
	{file: "distlock.go", fnName: "NewRedisDriver", takesClient: false},
}

// TestRedisConstructorsRequireKeyNamespace asserts each constructor's
// parameter list, return signature, and Validate() guard.
func TestRedisConstructorsRequireKeyNamespace(t *testing.T) {
	root := findModuleRoot(t)
	for _, spec := range redisConstructors {
		t.Run(spec.fnName, func(t *testing.T) {
			path := filepath.Join(root, "adapters", "redis", spec.file)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			require.NoError(t, err, "parse %s", path)

			fn := findRedisConstructorDecl(file, spec.fnName)
			require.NotNil(t, fn, "%s: %s not found", spec.file, spec.fnName)

			assertConstructorTakesKeyNamespace(t, spec, fn)
			assertConstructorReturnsErrorFirst(t, spec, fn)
			assertConstructorValidatesNamespace(t, spec, fn)
		})
	}
}

func findRedisConstructorDecl(file *ast.File, name string) *ast.FuncDecl {
	var found *ast.FuncDecl
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if found != nil {
			return // first-match wins; EachInSubtree has no break
		}
		if fn.Recv != nil {
			// Only top-level (non-method) functions are constructors.
			return
		}
		if fn.Name.Name == name {
			found = fn
		}
	})
	return found
}

// assertConstructorTakesKeyNamespace verifies the parameter list contains a
// `KeyNamespace`-typed argument named `ns`.
func assertConstructorTakesKeyNamespace(t *testing.T, spec redisConstructorSpec, fn *ast.FuncDecl) {
	t.Helper()
	require.NotNil(t, fn.Type.Params, "%s: missing params list", spec.fnName)

	found := false
	for _, field := range fn.Type.Params.List {
		ident, ok := field.Type.(*ast.Ident)
		if !ok || ident.Name != "KeyNamespace" {
			continue
		}
		// Field must have at least one named binding called `ns`.
		for _, n := range field.Names {
			if n.Name == "ns" {
				found = true
				break
			}
		}
	}
	require.True(t, found,
		"%s.%s: must declare `ns KeyNamespace` parameter (REDIS-KEY-NAMESPACE-01)",
		spec.file, spec.fnName)
}

// assertConstructorReturnsErrorFirst verifies the return list is `(*T, error)`.
func assertConstructorReturnsErrorFirst(t *testing.T, spec redisConstructorSpec, fn *ast.FuncDecl) {
	t.Helper()
	require.NotNil(t, fn.Type.Results, "%s: must return values", spec.fnName)

	results := fn.Type.Results.List
	totalResults := 0
	for _, f := range results {
		if len(f.Names) == 0 {
			totalResults++
		} else {
			totalResults += len(f.Names)
		}
	}
	require.Equalf(t, 2, totalResults,
		"%s.%s: must return (*T, error) tuple (REDIS-KEY-NAMESPACE-01); got %d result(s)",
		spec.file, spec.fnName, totalResults)

	// The last result must be `error`.
	last := results[len(results)-1]
	ident, ok := last.Type.(*ast.Ident)
	require.True(t, ok && ident.Name == "error",
		"%s.%s: last return must be `error` (REDIS-KEY-NAMESPACE-01)",
		spec.file, spec.fnName)
}

// assertConstructorValidatesNamespace requires `if err := ns.Validate(); ...`
// to appear at the TOP of the function body — within the first
// validateNamespaceMaxLeadingStmts statements. A nested or post-assignment
// Validate call would let invalid namespaces touch struct fields before
// the guard fires; the gate explicitly rejects that shape.
func assertConstructorValidatesNamespace(t *testing.T, spec redisConstructorSpec, fn *ast.FuncDecl) {
	t.Helper()
	require.NotNil(t, fn.Body, "%s.%s: must have a body", spec.file, spec.fnName)

	limit := len(fn.Body.List)
	if limit > validateNamespaceMaxLeadingStmts {
		limit = validateNamespaceMaxLeadingStmts
	}
	leading := fn.Body.List[:limit]

	// Synthetic BlockStmt wrapping the leading window: FindFirstChild[ast.IfStmt]
	// visits only direct IfStmt children (depth-1), scoped to the leading slice so
	// Validate calls beyond the limit window are not matched.
	syntheticLeading := &ast.BlockStmt{List: leading}
	_, found := scanner.FindFirstChild[ast.IfStmt](syntheticLeading, func(ifStmt *ast.IfStmt) bool {
		if ifStmt.Init == nil {
			return false
		}
		assign, ok := ifStmt.Init.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return false
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Validate" {
			return false
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "ns" {
			return false
		}
		return true
	})
	require.True(t, found,
		"%s.%s: must call `ns.Validate()` within the first %d body statements "+
			"so invalid namespaces fail-fast before any field assignment "+
			"(REDIS-KEY-NAMESPACE-01)",
		spec.file, spec.fnName, validateNamespaceMaxLeadingStmts)
}

// validateNamespaceMaxLeadingStmts is the position budget for the
// `ns.Validate()` guard. Three allows a constructor to nil-check a
// surrounding parameter (e.g. `client *Client`) before reaching the
// namespace check, which is the existing pattern in NewNonceStore. The
// budget is intentionally tight — if a fourth pre-Validate statement is
// needed, justify it in code review and bump the constant explicitly.
const validateNamespaceMaxLeadingStmts = 3
