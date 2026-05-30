// Package archtest — grpc_interceptor_chain_order_test.go
//
// INVARIANT: GRPC-INTERCEPTOR-CHAIN-ORDER-01
//
// runtime/grpc/interceptor/chain.go composes the unary interceptor chain via a
// single grpc.ChainUnaryInterceptor(...) call inside NewUnaryChain. That call's
// arguments MUST be, in order:
//
//	UnaryRequestID, UnaryTracing, UnaryMetrics, UnaryAuth, UnaryRecovery
//
// i.e. RequestID outermost and Recovery innermost. The order is load-bearing:
//   - RequestID outermost so every other interceptor (and any errcode the
//     handler emits) carries a stable request/correlation id.
//   - Recovery innermost so a handler panic is collapsed into codes.Internal
//     *before* the outer Metrics and Tracing interceptors observe the result;
//     otherwise a panic would be recorded as a raw failure rather than a clean
//     Internal status (see runtime/grpc/interceptor package doc + chain_test.go
//     behavioral guard).
//
// AI-robust: Medium. Pure-AST callee-order match over the single authoritative
// composition site (chain.go). Interceptor order is statement/argument order in
// a function body, which the Go type system cannot make "wrong order =
// uncompilable"; the HTTP middleware order in runtime/http/router is likewise
// archtest-free. NewUnaryChain is the single sanctioned composition point, and
// chain_test.go behaviorally verifies the recovery-innermost consequence; this
// archtest is the static backstop against future reordering. This is the
// permanent Medium ceiling for an ordering invariant.
//
// Blind spots (reverse self-checked below):
//   - A second grpc.ChainUnaryInterceptor call elsewhere in the package with a
//     different order would bypass this single-site scan
//     (TestArchtest_GRPCInterceptorChainOrder_BlindSpot_SingleCompositionSite).
//   - An argument that is not a direct constructor CallExpr with an *ast.Ident
//     callee (e.g. a variable, a spread, a wrapped call) would make the order
//     extraction meaningless
//     (TestArchtest_GRPCInterceptorChainOrder_BlindSpot_ArgsAreDirectConstructorCalls).
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// grpcChainExpectedOrder is the required argument order of the
// grpc.ChainUnaryInterceptor call in NewUnaryChain.
var grpcChainExpectedOrder = []string{
	"UnaryRequestID",
	"UnaryTracing",
	"UnaryMetrics",
	"UnaryAuth",
	"UnaryRecovery",
}

func grpcInterceptorChainFile(t *testing.T) string {
	t.Helper()
	root := findModuleRoot(t)
	return filepath.Join(root, "runtime", "grpc", "interceptor", "chain.go")
}

// isChainUnaryInterceptorCall reports whether call is `grpc.ChainUnaryInterceptor(...)`.
func isChainUnaryInterceptorCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "ChainUnaryInterceptor" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "grpc"
}

// TestArchtest_GRPCInterceptorChainOrder asserts the interceptor argument order
// of the single grpc.ChainUnaryInterceptor call in chain.go.
func TestArchtest_GRPCInterceptorChainOrder(t *testing.T) {
	path := grpcInterceptorChainFile(t)
	fset := token.NewFileSet()
	// #nosec G304 -- reading repo-resident file under module root
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "GRPC-INTERCEPTOR-CHAIN-ORDER-01: cannot parse %s", path)

	var calls []*ast.CallExpr
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if isChainUnaryInterceptorCall(call) {
			calls = append(calls, call)
		}
	})

	require.Len(t, calls, 1,
		"GRPC-INTERCEPTOR-CHAIN-ORDER-01: expected exactly one grpc.ChainUnaryInterceptor call in chain.go, found %d",
		len(calls))

	got := make([]string, 0, len(calls[0].Args))
	for _, arg := range calls[0].Args {
		got = append(got, grpcChainArgName(arg))
	}

	assert.Equal(t, grpcChainExpectedOrder, got,
		"GRPC-INTERCEPTOR-CHAIN-ORDER-01: interceptor order must be RequestID→Tracing→Metrics→Auth→Recovery "+
			"(RequestID outermost, Recovery innermost). See runtime/grpc/interceptor package doc.")
}

// grpcChainArgName extracts the callee identifier of a constructor-call argument
// (e.g. "UnaryRecovery" from `UnaryRecovery()`), or a "<non-constructor:...>"
// marker the order assertion will reject.
func grpcChainArgName(arg ast.Expr) string {
	call, ok := arg.(*ast.CallExpr)
	if !ok {
		return fmt.Sprintf("<non-call:%T>", arg)
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name
	}
	return fmt.Sprintf("<non-ident-callee:%T>", call.Fun)
}

// TestArchtest_GRPCInterceptorChainOrder_BlindSpot_SingleCompositionSite is the
// reverse self-check: the entire runtime/grpc/interceptor package (non-test
// files) must contain exactly one grpc.ChainUnaryInterceptor call, so the order
// scan above covers the sole composition site. A second composition elsewhere
// could install a differently-ordered chain that the single-file scan misses.
func TestArchtest_GRPCInterceptorChainOrder_BlindSpot_SingleCompositionSite(t *testing.T) {
	dir := filepath.Dir(grpcInterceptorChainFile(t))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "cannot read %s", dir)

	fset := token.NewFileSet()
	total := 0
	var locations []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		p := filepath.Join(dir, name)
		// #nosec G304 -- reading repo-resident file under module root
		f, perr := parser.ParseFile(fset, p, nil, 0)
		require.NoError(t, perr, "cannot parse %s", p)
		scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
			if isChainUnaryInterceptorCall(call) {
				total++
				locations = append(locations, fmt.Sprintf("%s:%d", name, fset.Position(call.Pos()).Line))
			}
		})
	}

	assert.Equal(t, 1, total,
		"GRPC-INTERCEPTOR-CHAIN-ORDER-01 blind-spot: grpc.ChainUnaryInterceptor must be called exactly once "+
			"in the interceptor package (sole composition site); found at %v", locations)
}

// TestArchtest_GRPCInterceptorChainOrder_BlindSpot_ArgsAreDirectConstructorCalls
// is the reverse self-check that every argument to grpc.ChainUnaryInterceptor is
// a direct constructor CallExpr with an *ast.Ident callee. If an argument were a
// variable, a spread (xs...), or a wrapped call, the order extraction would be
// meaningless and the main assertion could vacuously pass.
func TestArchtest_GRPCInterceptorChainOrder_BlindSpot_ArgsAreDirectConstructorCalls(t *testing.T) {
	path := grpcInterceptorChainFile(t)
	fset := token.NewFileSet()
	// #nosec G304 -- reading repo-resident file under module root
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "cannot parse %s", path)

	var violations []string
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if !isChainUnaryInterceptorCall(call) {
			return
		}
		if call.Ellipsis != token.NoPos {
			violations = append(violations, "argument list uses a spread (xs...)")
		}
		for i, arg := range call.Args {
			c, ok := arg.(*ast.CallExpr)
			if !ok {
				violations = append(violations, fmt.Sprintf("arg %d is not a constructor call (%T)", i, arg))
				continue
			}
			if _, ok := c.Fun.(*ast.Ident); !ok {
				violations = append(violations, fmt.Sprintf("arg %d callee is not a bare identifier (%T)", i, c.Fun))
			}
		}
	})

	assert.Empty(t, violations,
		"GRPC-INTERCEPTOR-CHAIN-ORDER-01 blind-spot: every grpc.ChainUnaryInterceptor argument must be a "+
			"direct interceptor-constructor call (e.g. UnaryRecovery()); otherwise order enforcement is bypassable.")
}
