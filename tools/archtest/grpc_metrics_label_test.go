// grpc_metrics_label_test.go — locks the gRPC metrics interceptor's cell-label
// source contract, the gRPC mirror of the HTTP CELLID-CTXSOURCE invariant in
// http_metrics_label_test.go.
//
// INVARIANT: GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01
//
// # What this guards
//
// runtime/grpc/interceptor.UnaryMetrics records grpc_server_requests_total /
// grpc_server_request_duration_seconds. Post-M12b (#1093) its `cell` label MUST
// be produced by the sealed runtime/observability/metrics.ResolveCellLabel
// funnel — never a hand-written cell literal, an unrelated variable, a
// constructor field, or an assembly-derived value. The funnel reads
// ctxkeys.CellIDFrom + validates against the closed set internally; gRPC passes a
// nil closed set today (attribution not yet wired), so the resolved label is
// always RuntimeCellSentinel until #1383. Locking the funnel routing now means
// that when the writer side lands (FullMethod→cellID attribution + a real closed
// set, epic PR-7/8) the recorded `cell` label automatically reflects the
// attributed cell instead of regressing to a hardcoded value.
//
// Scope note: gRPC cell ATTRIBUTION (writing ctxkeys.CellID from a FullMethod→
// cellID map, and threading a real closed set) is NOT yet wired — tracked by
// gh #1383. This archtest covers ONLY the reader/funnel-routing contract; the
// attribution-wiring archtest lands with PR-7/8. Until then the recorded label
// is always "_runtime" (see docs/ops/alerting-rules.md).
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD (type system) — RecordRPC takes a sealed metrics.CellLabel
//     whose sole exported constructor is metrics.ResolveCellLabel, so a raw
//     string cell label is a compile error. This archtest is the MEDIUM upstream
//     net: it binds the RecordRPC cell argument to its data-flow origin by
//     go/types object identity (cellLabelFromResolve) — the passed identifier
//     MUST be the same var.Object assigned from a metrics.ResolveCellLabel call,
//     so an unrelated CellLabel-typed identifier (e.g. a zero value) of the
//     correct AST shape is rejected.
//   - Upstream HARD ceiling: the sealed CellLabel already IS the unified funnel
//     (former gh #1398 resolved in-PR), shared by HTTP + gRPC; no further Hard
//     upgrade is tracked.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - B1. The rule does not verify the `ctx` passed to RecordRPC was produced
//     from the interceptor's ctx param vs another context value; it only asserts
//     arg[0] is an identifier (not a fresh context.Background()/TODO()). Mirrors
//     the HTTP arg[0] check.
//   - B2 (CLOSED). cellLabelFromResolve binds the cell-label arg by go/types
//     object identity to a metrics.ResolveCellLabel assignment, so an unrelated
//     identifier of the correct AST shape is rejected — proven by the RED fixture
//     (TestGRPCMetricsLabelCellIDCtxSource01_RedFixtureDetected). Residual: only
//     direct (single-hop) assignments are followed; an indirect alias chain
//     (cell = tmp; tmp = ResolveCellLabel(...)) is not recognized, but fails
//     CLOSED (the provenance assertion fires), so it cannot smuggle a bad label.
//   - B3. A RecordRPC call in a //go:build-gated production file under a
//     non-default tag would be missed by the default-tags load. UnaryMetrics is
//     default-build; documented.
//
// # Reverse self-check
//
// The cell argument of RecordRPC must be an *ast.Ident whose go/types object is
// provenance-bound to a metrics.ResolveCellLabel assignment. The RED fixture
// internal/grpcmetricsfixture (gated `//go:build archtest_fixture`) routes
// through ResolveCellLabel but feeds an UNRELATED CellLabel ident to RecordRPC
// and must yield exactly the one cellLabelFromResolve diagnostic.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"
)

const (
	grpcMetricsRuleCtxSource = "GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01"
	// grpcInterceptorPkgPath is declared in grpc_interceptor_chain_invariants_test.go.
	grpcCtxkeysPkgPath          = PlatformModulePath + "/kernel/ctxkeys"
	grpcMetricsPkgPath          = PlatformModulePath + "/runtime/observability/metrics"
	grpcMetricsCellIDFromName   = "CellIDFrom"
	grpcMetricsResolveLabelName = "ResolveCellLabel"
	grpcMetricsRecordRPCName    = "RecordRPC"
	grpcMetricsUnaryMetricsName = "UnaryMetrics"
)

// TestGRPCMetricsLabelCellIDCtxSource01 asserts that
// runtime/grpc/interceptor.UnaryMetrics resolves its `cell` label through the
// sealed metrics.ResolveCellLabel funnel, feeding the resolved CellLabel
// identifier into GRPCCollector.RecordRPC (never a literal, never an unrelated
// var), with the funnel call ordered before the RecordRPC call and no inline
// ctxkeys.CellIDFrom read.
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
// package or the RED fixture. The same scan serves both Run(t, Production(...))
// and Run(t, Fixture(...)).
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
	// SafeObserve closure that resolves and records the metric.
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
		callsResolve   bool
		readsCtxInline bool
		resolvePos     token.Pos
		recordPos      token.Pos
		cellArgIsIdent bool
		cellArgIsLit   bool
		ctxArgIsIdent  bool
		sawRecordRPC   bool
		cellArgExpr    ast.Expr // arg[1] of RecordRPC; fed to cellLabelFromResolve.
	)

	EachInSubtree[ast.CallExpr](recordLit.Body, func(call *ast.CallExpr) {
		if pkg, name, ok := ResolvePackageRef(info, call.Fun); ok && pkg == grpcMetricsPkgPath && name == grpcMetricsResolveLabelName {
			callsResolve = true
			rememberFirstPos(&resolvePos, call.Pos())
		}
		if pkg, name, ok := ResolvePackageRef(info, call.Fun); ok && pkg == grpcCtxkeysPkgPath && name == grpcMetricsCellIDFromName {
			readsCtxInline = true
		}
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
			cellArgExpr = call.Args[1]
			_, cellArgIsIdent = call.Args[1].(*ast.Ident)
			_, cellArgIsLit = call.Args[1].(*ast.BasicLit)
		}
	})

	// Data-flow provenance: bind the cell-label argument to its origin by
	// go/types object identity — it must be assigned from metrics.ResolveCellLabel.
	cellFromResolve := cellLabelFromResolve(info, recordLit.Body, cellArgExpr)

	var d []Diagnostic
	add := func(cond bool, msg string) {
		if !cond {
			d = append(d, Diagnostic{Rel: rel, Line: p.Fset.Position(recordLit.Pos()).Line, Message: grpcMetricsRuleCtxSource + ": " + msg})
		}
	}
	add(callsResolve, "UnaryMetrics must resolve the cell label through metrics.ResolveCellLabel(ctx, nil)")
	add(!readsCtxInline, "cell resolution moved into metrics.ResolveCellLabel; UnaryMetrics must not read ctxkeys.CellIDFrom inline")
	add(sawRecordRPC, "UnaryMetrics must call GRPCCollector.RecordRPC")
	add(cellArgIsIdent, "RecordRPC arg[1] (cell label) must be an identifier (the resolved CellLabel variable), not an inline expression")
	add(!cellArgIsLit, "RecordRPC arg[1] (cell label) must NOT be a literal")
	add(cellFromResolve, "RecordRPC arg[1] (cell label) must be the same variable assigned from "+
		"metrics.ResolveCellLabel — provenance binding by go/types object identity (closes B2)")
	add(ctxArgIsIdent, "RecordRPC arg[0] must be the ctx identifier, not a fresh context.Background()/TODO() call expression")
	add(resolvePos.IsValid() && recordPos.IsValid() && resolvePos < recordPos,
		"metrics.ResolveCellLabel must be called before GRPCCollector.RecordRPC")
	return d
}

// cellLabelFromResolve binds the RecordRPC cell-label argument to its data-flow
// origin by go/types object identity (closes blind spot B2). It reports whether
// the object referenced by argExpr is assigned from a metrics.ResolveCellLabel
// call within body. An unrelated CellLabel identifier — even a valid *ast.Ident
// that is not a literal — is the same var.Object in no such assignment, so the
// result is false and the rule rejects it.
//
// Only direct (single-hop) assignments are followed; an indirect alias chain is
// not recognized but fails CLOSED — see godoc B2.
func cellLabelFromResolve(info *types.Info, body ast.Node, argExpr ast.Expr) bool {
	argIdent, ok := argExpr.(*ast.Ident)
	if !ok {
		return false
	}
	argObj := info.ObjectOf(argIdent)
	if argObj == nil {
		return false
	}
	found := false
	// The funnel assignment is always single-assign (`cell := ResolveCellLabel(...)`),
	// so match `<arg> = <call>` / `<arg> := <call>` directly without iterating an
	// index-correlated Lhs/Rhs slice (SCANNER-FRAMEWORK-USAGE-01).
	EachInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) {
		if len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return
		}
		id, isIdent := as.Lhs[0].(*ast.Ident)
		if !isIdent || info.ObjectOf(id) != argObj {
			return
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return
		}
		if pkg, name, ok := ResolvePackageRef(info, call.Fun); ok &&
			pkg == grpcMetricsPkgPath && name == grpcMetricsResolveLabelName {
			found = true
		}
	})
	return found
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
