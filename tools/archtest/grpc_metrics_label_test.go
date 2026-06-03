// grpc_metrics_label_test.go — locks the gRPC metrics interceptor's cell-label
// source contract, the gRPC mirror of the HTTP CELLID-CTXSOURCE / RUNTIME-SENTINEL
// invariants in http_metrics_label_test.go.
//
// INVARIANT: GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01
//
// # What this guards
//
// runtime/grpc/interceptor.UnaryMetrics records grpc_server_requests_total /
// grpc_server_request_duration_seconds. Its `cell` label MUST be sourced from
// kernel/ctxkeys.CellIDFrom(ctx), defaulting to
// runtime/observability/metrics.RuntimeCellSentinel ("_runtime") when the ctx
// key is absent — never a hand-written cell literal, a constructor field, or an
// assembly-derived value. This is the reader-side half of cell attribution: it
// is correct TODAY (the interceptor already reads ctx + sentinel), and it must
// stay correct so that when the writer side lands (the FullMethod→cellID
// attribution interceptor, epic PR-7/8) the recorded `cell` label automatically
// reflects the attributed cell instead of regressing to a hardcoded value.
//
// Scope note: gRPC cell ATTRIBUTION (writing ctxkeys.CellID from a FullMethod→
// cellID map) is NOT yet wired — that needs the generated registrar (epic PR-7)
// + an example service (epic PR-8), tracked by gh #1383. So there is no gRPC
// analog of HTTP's ROUTER-ATTRIBUTION-01 (which asserts the attribution
// middleware is installed) — there is no interceptor to assert yet. This
// archtest deliberately covers ONLY the reader contract; the attribution-wiring
// archtest lands with PR-7/8. Until then the recorded label is always
// "_runtime" (see docs/ops/alerting-rules.md).
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: MEDIUM (archtest type-aware AST form + position ordering +
//     object-identity data-flow binding). The CellIDFrom / RuntimeCellSentinel
//     origin checks resolve through go/types (ResolvePackageRef /
//     ResolveMethodCall on the package's TypesInfo), so the kernelctxkeys import
//     alias in metrics.go cannot evade detection and a same-named symbol from
//     another package cannot satisfy it. The RecordRPC cell-label argument is
//     additionally bound to its data-flow origin by go/types *object identity*
//     (cellIDProvenance): the passed identifier MUST be the same var.Object that
//     is both initialized from metrics.RuntimeCellSentinel and (re)assigned from
//     the ctxkeys.CellIDFrom(ctx) branch value — an unrelated identifier of the
//     correct AST shape no longer satisfies the rule (closes B2, see below).
//   - Upstream: HARD is UNREACHABLE in this slice — a Go-language ceiling, not a
//     deferred TODO at the archtest layer. The Hard form is a sealed CellLabel
//     type that makes a hardcoded cell label compile-impossible (only producible
//     from ctx or the sentinel). That is the SAME sealed-collector funnel the
//     HTTP side already deferred and tracks at gh #1398 (HTTP's symmetric
//     RecordRequest(cellID string) is also Medium-guarded). Introducing a
//     gRPC-only sealed type now would break HTTP/gRPC symmetry and front-run
//     #1398, so the Hard upgrade is bound to that unified funnel — HTTP + gRPC
//     convert together. Tracked: gh #1398.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - B1. The rule does not verify that the `ctx` passed to RecordRPC was itself
//     produced from the interceptor's ctx param vs some other context value; it
//     only asserts arg[0] is an identifier (not a fresh context.Background() /
//     context.TODO() call expression). Mirrors the HTTP body_limit arg[0] check.
//   - B2 (CLOSED). Earlier revisions only asserted the RecordRPC cellID arg was
//     an identifier (not a string literal), so a body that read CellIDFrom but
//     passed an *unrelated* identifier would pass. cellIDProvenance now binds the
//     arg by go/types object identity to BOTH the metrics.RuntimeCellSentinel
//     init and the ctxkeys.CellIDFrom(ctx) branch assignment, so the unrelated-
//     ident form is rejected. Proven by the RED fixture
//     (TestGRPCMetricsLabelCellIDCtxSource01_RedFixtureDetected). Residual: only
//     direct (single-hop) assignments are followed — an indirect alias chain
//     (cellID = tmp; tmp = v) is NOT recognized, but that fails CLOSED (the
//     provenance asserts fire), so it cannot smuggle a bad label through.
//   - B3. A RecordRPC call added in a //go:build-gated production file under a
//     non-default tag would be missed by the default-tags load. UnaryMetrics is
//     default-build; documented.
//
// # Reverse self-check
//
// The cellID argument of RecordRPC must be an *ast.Ident (never an *ast.BasicLit)
// whose go/types object is provenance-bound to both the RuntimeCellSentinel init
// and the CellIDFrom(ctx) branch. Two reverse guards prove this is enforced, not
// merely asserted on compliant source:
//
//   - A hardcoded cell-label string passed directly to RecordRPC fails the rule
//     (the !BasicLit assertion).
//   - An unrelated identifier of the correct AST shape (a valid ident that is NOT
//     the ctx-derived cellID) fails the rule — exercised by the RED fixture
//     internal/grpcmetricsfixture (gated `//go:build archtest_fixture`), which
//     reads CellIDFrom + sentinel but feeds a bogus ident to RecordRPC and must
//     yield exactly the two cellIDProvenance diagnostics.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	grpcMetricsRuleCtxSource = "GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01"
	// grpcInterceptorPkgPath is declared in grpc_interceptor_chain_invariants_test.go.
	grpcCtxkeysPkgPath          = PlatformModulePath + "/kernel/ctxkeys"
	grpcMetricsPkgPath          = PlatformModulePath + "/runtime/observability/metrics"
	grpcMetricsCellIDFromName   = "CellIDFrom"
	grpcMetricsSentinelName     = "RuntimeCellSentinel"
	grpcMetricsRecordRPCName    = "RecordRPC"
	grpcMetricsUnaryMetricsName = "UnaryMetrics"
)

// TestGRPCMetricsLabelCellIDCtxSource01 asserts that
// runtime/grpc/interceptor.UnaryMetrics sources its `cell` label from
// kernel/ctxkeys.CellIDFrom with a runtime/observability/metrics.RuntimeCellSentinel
// fallback, feeding a ctx-derived identifier (never a literal, never an unrelated
// var) into GRPCCollector.RecordRPC, with both reads ordered before the
// RecordRPC call.
func TestGRPCMetricsLabelCellIDCtxSource01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var visited bool
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != grpcInterceptorPkgPath {
			return nil
		}
		// The interceptor package loaded; the outer !visited Fatalf below now
		// only fires for a genuine "package not loaded" (production scope gap),
		// never for a renamed UnaryMetrics — that case emits its own diagnostic.
		visited = true
		return scanGRPCMetricsLabelPkg(p)
	})

	if !visited {
		t.Fatalf("%s: package %s with func %s not loaded — rule did not run against real source",
			grpcMetricsRuleCtxSource, grpcInterceptorPkgPath, grpcMetricsUnaryMetricsName)
	}
	Report(t, grpcMetricsRuleCtxSource, diags)
}

// scanGRPCMetricsLabelPkg runs the GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01 checks
// against the already-package-filtered Pass p — the production interceptor
// package (TestGRPCMetricsLabelCellIDCtxSource01) or the RED fixture
// (TestGRPCMetricsLabelCellIDCtxSource01_RedFixtureDetected). The package-path
// filter and the production `visited` tracking stay in the callers so the same
// scan serves both Run(t, Production(...)) and Run(t, Fixture(...)).
func scanGRPCMetricsLabelPkg(p *Pass) []Diagnostic {
	fn := findUnaryMetricsFuncDecl(p.Files)
	if fn == nil {
		return []Diagnostic{{
			Rel: p.Rel(p.Files[0]),
			Message: fmt.Sprintf("%s: func %s not found in %s — renamed or moved? "+
				"the gRPC metrics cell-label reader contract is no longer locked",
				grpcMetricsRuleCtxSource, grpcMetricsUnaryMetricsName, p.Pkg.Path()),
		}}
	}

	rel := relForFunc(p, fn)
	info := p.TypesInfo

	// Narrowest FuncLit whose body issues GRPCCollector.RecordRPC — the
	// SafeObserve closure that builds and records the metric.
	recordLit := narrowestFuncLitWithRecordRPC(info, fn.Body)
	if recordLit == nil {
		return []Diagnostic{{
			Rel:  rel,
			Line: p.Fset.Position(fn.Pos()).Line,
			Message: fmt.Sprintf("%s: %s.UnaryMetrics must record gRPC metrics through "+
				"GRPCCollector.RecordRPC", grpcMetricsRuleCtxSource, p.Pkg.Path()),
		}}
	}

	var (
		readsCtxCellID   bool
		usesSentinel     bool
		ctxCellIDPos     token.Pos
		sentinelPos      token.Pos
		recordPos        token.Pos
		cellIDArgIsIdent bool
		cellIDArgIsLit   bool
		ctxArgIsIdent    bool
		sawRecordRPC     bool
		cellIDArgExpr    ast.Expr // arg[1] of RecordRPC; fed to cellIDProvenance.
	)

	EachInSubtree[ast.CallExpr](recordLit.Body, func(call *ast.CallExpr) {
		// kernel/ctxkeys.CellIDFrom(ctx) — alias-robust via go/types.
		if pkg, name, ok := ResolvePackageRef(info, call.Fun); ok &&
			pkg == grpcCtxkeysPkgPath && name == grpcMetricsCellIDFromName {
			readsCtxCellID = true
			rememberFirstPos(&ctxCellIDPos, call.Pos())
		}
		// GRPCCollector.RecordRPC(ctx, cellID, method, code, duration).
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		rpc, ok := ResolveMethodCall(info, sel)
		if !ok || rpc.Name() != grpcMetricsRecordRPCName ||
			rpc.Pkg() == nil || rpc.Pkg().Path() != grpcMetricsPkgPath {
			return
		}
		sawRecordRPC = true
		rememberFirstPos(&recordPos, call.Pos())
		if len(call.Args) > 0 {
			_, ctxArgIsIdent = call.Args[0].(*ast.Ident)
		}
		if len(call.Args) > 1 {
			cellIDArgExpr = call.Args[1]
			_, cellIDArgIsIdent = call.Args[1].(*ast.Ident)
			_, cellIDArgIsLit = call.Args[1].(*ast.BasicLit)
		}
	})
	// runtime/observability/metrics.RuntimeCellSentinel fallback — resolved
	// via go/types on selector/ident value references (alias-robust).
	EachInSubtree[ast.SelectorExpr](recordLit.Body, func(sel *ast.SelectorExpr) {
		if pkg, name, ok := ResolvePackageRef(info, sel); ok &&
			pkg == grpcMetricsPkgPath && name == grpcMetricsSentinelName {
			usesSentinel = true
			rememberFirstPos(&sentinelPos, sel.Pos())
		}
	})

	// Data-flow provenance: bind the cell-label argument to its origin by
	// go/types object identity (closes blind spot B2). An unrelated identifier
	// — even a valid *ast.Ident that is not a literal — satisfies neither.
	cellIDFromSentinel, cellIDFromCtx := cellIDProvenance(info, recordLit.Body, cellIDArgExpr)

	var d []Diagnostic
	add := func(cond bool, msg string) {
		if !cond {
			d = append(d, Diagnostic{Rel: rel, Line: p.Fset.Position(recordLit.Pos()).Line, Message: grpcMetricsRuleCtxSource + ": " + msg})
		}
	}
	add(readsCtxCellID, "UnaryMetrics must read the cell label from kernel/ctxkeys.CellIDFrom(ctx)")
	add(usesSentinel, "UnaryMetrics must default a missing cell context to metrics.RuntimeCellSentinel before RecordRPC")
	add(sawRecordRPC, "UnaryMetrics must call GRPCCollector.RecordRPC")
	add(cellIDArgIsIdent, "RecordRPC arg[1] (cell label) must be an identifier (a ctx-derived variable), not an inline expression")
	add(!cellIDArgIsLit, "RecordRPC arg[1] (cell label) must NOT be a string literal — "+
		"a hardcoded cell label is forbidden (reverse self-check)")
	add(cellIDFromSentinel, "RecordRPC arg[1] (cell label) must be the same variable initialized from "+
		"metrics.RuntimeCellSentinel — provenance binding by go/types object identity (closes B2)")
	add(cellIDFromCtx, "RecordRPC arg[1] (cell label) must be the same variable (re)assigned from the "+
		"ctxkeys.CellIDFrom(ctx) branch value — provenance binding by go/types object identity (closes B2)")
	add(ctxArgIsIdent, "RecordRPC arg[0] must be the ctx identifier, not a fresh context.Background()/TODO() call expression")
	add(ctxCellIDPos.IsValid() && recordPos.IsValid() && ctxCellIDPos < recordPos,
		"ctxkeys.CellIDFrom must be read before GRPCCollector.RecordRPC")
	add(sentinelPos.IsValid() && recordPos.IsValid() && sentinelPos < recordPos,
		"metrics.RuntimeCellSentinel fallback must appear before GRPCCollector.RecordRPC")
	return d
}

// cellIDProvenance binds the RecordRPC cell-label argument to its data-flow
// origin by go/types object identity (closes blind spot B2). It reports whether
// the object referenced by argExpr is (a) initialized from
// metrics.RuntimeCellSentinel and (b) (re)assigned from the value bound by a
// kernel/ctxkeys.CellIDFrom(ctx) call within body. An unrelated identifier — even
// a valid *ast.Ident that is not a string literal — is the same var.Object in
// neither assignment, so both results are false and the rule rejects it.
//
// Only direct (single-hop) assignments are followed; an indirect alias chain
// (cellID = tmp; tmp = v) is not recognized but fails CLOSED — see godoc B2.
func cellIDProvenance(info *types.Info, body ast.Node, argExpr ast.Expr) (fromSentinel, fromCtx bool) {
	argIdent, ok := argExpr.(*ast.Ident)
	if !ok {
		return false, false
	}
	argObj := info.ObjectOf(argIdent)
	if argObj == nil {
		return false, false
	}

	// Pass 1: collect the objects bound by `<v>, <ok> := ctxkeys.CellIDFrom(ctx)`
	// — the ctx-derived value candidates the cell label may be assigned from.
	ctxValueObjs := map[types.Object]bool{}
	EachInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) {
		if len(as.Lhs) == 0 || len(as.Rhs) != 1 {
			return
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return
		}
		if pkg, name, ok := ResolvePackageRef(info, call.Fun); !ok ||
			pkg != grpcCtxkeysPkgPath || name != grpcMetricsCellIDFromName {
			return
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			if obj := info.ObjectOf(id); obj != nil {
				ctxValueObjs[obj] = true
			}
		}
	})

	// Pass 2: inspect every assignment whose LHS is the cell-label object, and
	// classify its RHS as the sentinel init or a ctx-derived value assignment.
	EachInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) {
		// Use EachInChildren[ast.Ident] to avoid for-range over []ast.Expr + type assertion.
		// Filter to Lhs idents only (Lhs items appear before Rhs[0] in source).
		scanner.EachInChildren[ast.Ident](as, func(id *ast.Ident) {
			if info.ObjectOf(id) != argObj {
				return
			}
			// Only process Lhs idents (those that appear before the first Rhs expr).
			if len(as.Rhs) > 0 && id.Pos() >= as.Rhs[0].Pos() {
				return
			}
			// Find the matching Rhs by index (using position to identify the Lhs slot).
			for i, lhsExpr := range as.Lhs {
				if lhsExpr.Pos() != id.Pos() || i >= len(as.Rhs) {
					continue
				}
				rhs := as.Rhs[i]
				if pkg, name, ok := ResolvePackageRef(info, rhs); ok &&
					pkg == grpcMetricsPkgPath && name == grpcMetricsSentinelName {
					fromSentinel = true
				}
				if rid, ok := rhs.(*ast.Ident); ok {
					if obj := info.ObjectOf(rid); obj != nil && ctxValueObjs[obj] {
						fromCtx = true
					}
				}
			}
		})
	})
	return fromSentinel, fromCtx
}

// findUnaryMetricsFuncDecl returns the UnaryMetrics top-level FuncDecl across the
// Pass's files, or nil if absent.
func findUnaryMetricsFuncDecl(files []*ast.File) *ast.FuncDecl {
	var result *ast.FuncDecl
	for _, f := range files {
		EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
			if result == nil && fn.Recv == nil && fn.Name.Name == grpcMetricsUnaryMetricsName {
				result = fn
			}
		})
	}
	return result
}

// relForFunc returns the module-relative path of the file owning fn.
func relForFunc(p *Pass, fn *ast.FuncDecl) string {
	for _, f := range p.Files {
		if fn.Pos() >= f.Pos() && fn.Pos() <= f.End() {
			return p.Rel(f)
		}
	}
	return grpcInterceptorPkgPath
}

// narrowestFuncLitWithRecordRPC returns the smallest *ast.FuncLit under root
// whose body issues a call resolving to GRPCCollector.RecordRPC.
func narrowestFuncLitWithRecordRPC(info *types.Info, root ast.Node) *ast.FuncLit {
	var best *ast.FuncLit
	EachInSubtree[ast.FuncLit](root, func(fl *ast.FuncLit) {
		if !bodyCallsRecordRPC(info, fl.Body) {
			return
		}
		if best == nil || fl.End()-fl.Pos() < best.End()-best.Pos() {
			best = fl
		}
	})
	return best
}

// bodyCallsRecordRPC reports whether root contains a call resolving to
// runtime/observability/metrics.GRPCCollector.RecordRPC. Uses the typed
// find-first funnel (SCANNER-FRAMEWORK-USAGE-02) rather than a closure sentinel.
func bodyCallsRecordRPC(info *types.Info, root ast.Node) bool {
	_, ok := FindFirstInSubtree[ast.CallExpr](root, func(call *ast.CallExpr) bool {
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel {
			return false
		}
		rpc, resolved := ResolveMethodCall(info, sel)
		return resolved && rpc.Name() == grpcMetricsRecordRPCName &&
			rpc.Pkg() != nil && rpc.Pkg().Path() == grpcMetricsPkgPath
	})
	return ok
}
