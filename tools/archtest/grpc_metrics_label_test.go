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
//   - Downstream: MEDIUM (archtest type-aware AST form + position ordering).
//     The CellIDFrom / RuntimeCellSentinel origin checks resolve through
//     go/types (ResolvePackageRef / ResolveMethodCall on the package's
//     TypesInfo), so the kernelctxkeys import alias in metrics.go cannot evade
//     detection and a same-named symbol from another package cannot satisfy it.
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
//   - B2. The rule does not verify the cellID variable was assigned from
//     CellIDFrom's return; it asserts (a) CellIDFrom is called and (b) the
//     RecordRPC cellID arg is an identifier (not a string literal). A pathological
//     body that reads CellIDFrom but passes an unrelated ident would pass — but
//     such a body cannot pass a HARDCODED literal, which is the actual threat.
//   - B3. A RecordRPC call added in a //go:build-gated production file under a
//     non-default tag would be missed by the default-tags load. UnaryMetrics is
//     default-build; documented.
//
// # Reverse self-check
//
// The cellID argument of RecordRPC must be an *ast.Ident, never an *ast.BasicLit
// — i.e. a hardcoded cell label string passed directly to RecordRPC fails the
// rule. This is the durable guard that survives independent of the ctx-read
// assertions above.
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
	grpcMetricsSentinelName     = "RuntimeCellSentinel"
	grpcMetricsRecordRPCName    = "RecordRPC"
	grpcMetricsUnaryMetricsName = "UnaryMetrics"
)

// TestGRPCMetricsLabelCellIDCtxSource01 asserts that
// runtime/grpc/interceptor.UnaryMetrics sources its `cell` label from
// kernel/ctxkeys.CellIDFrom with a runtime/observability/metrics.RuntimeCellSentinel
// fallback, feeding a ctx-derived identifier (never a literal) into
// GRPCCollector.RecordRPC, with both reads ordered before the RecordRPC call.
func TestGRPCMetricsLabelCellIDCtxSource01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var visited bool
	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != grpcInterceptorPkgPath {
			return nil
		}
		// The interceptor package loaded; the outer !visited Fatalf below now
		// only fires for a genuine "package not loaded" (production scope gap),
		// never for a renamed UnaryMetrics — that case emits its own diagnostic.
		visited = true

		fn := findUnaryMetricsFuncDecl(p.Files)
		if fn == nil {
			return []Diagnostic{{
				Rel: p.Rel(p.Files[0]),
				Message: fmt.Sprintf("%s: func %s not found in %s — renamed or moved? "+
					"the gRPC metrics cell-label reader contract is no longer locked",
					grpcMetricsRuleCtxSource, grpcMetricsUnaryMetricsName, grpcInterceptorPkgPath),
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
					"GRPCCollector.RecordRPC", grpcMetricsRuleCtxSource, grpcInterceptorPkgPath),
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

		var d []Diagnostic
		add := func(cond bool, msg string) {
			if !cond {
				d = append(d, Diagnostic{Rel: rel, Line: p.Fset.Position(recordLit.Pos()).Line, Message: grpcMetricsRuleCtxSource + ": " + msg})
			}
		}
		add(readsCtxCellID, "UnaryMetrics must read the cell label from kernel/ctxkeys.CellIDFrom(ctx)")
		add(usesSentinel, "UnaryMetrics must default a missing cell context to metrics.RuntimeCellSentinel before RecordRPC")
		add(sawRecordRPC, "UnaryMetrics must call GRPCCollector.RecordRPC")
		add(cellIDArgIsIdent, "RecordRPC arg[1] (cell label) must be the ctx-derived cellID identifier, not a constructor/config value")
		add(!cellIDArgIsLit, "RecordRPC arg[1] (cell label) must NOT be a string literal — "+
			"a hardcoded cell label is forbidden (reverse self-check)")
		add(ctxArgIsIdent, "RecordRPC arg[0] must be the ctx identifier, not a fresh context.Background()/TODO() call expression")
		add(ctxCellIDPos.IsValid() && recordPos.IsValid() && ctxCellIDPos < recordPos,
			"ctxkeys.CellIDFrom must be read before GRPCCollector.RecordRPC")
		add(sentinelPos.IsValid() && recordPos.IsValid() && sentinelPos < recordPos,
			"metrics.RuntimeCellSentinel fallback must appear before GRPCCollector.RecordRPC")
		return d
	})

	if !visited {
		t.Fatalf("%s: package %s with func %s not loaded — rule did not run against real source",
			grpcMetricsRuleCtxSource, grpcInterceptorPkgPath, grpcMetricsUnaryMetricsName)
	}
	Report(t, grpcMetricsRuleCtxSource, diags)
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
