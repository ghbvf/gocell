//go:build archtest

// INVARIANT: GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01
//
// This file owns ONE invariant: the set of string values returned by the
// ProtectionType accessors in runtime/observability/metrics — the value set for
// the `type` label on grpc_protection_rejected_total{type,method,cell} — is
// frozen to exactly:
//
//	{"ratelimit", "circuit"}
//
// These are the two terminal protection outcomes from the gRPC interceptor chain
// (RateLimit deny / CircuitBreaker open). The set is design-time bounded: runtime
// drift (a 3rd accessor, a renamed value) breaks dashboards / alerts without a
// compile error.
//
// # Two prongs, mirroring RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 /
// IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01
//
//   - A1 freeze (UPSTREAM): enumerate the ProtectionType accessor RETURN VALUES
//     from the runtime/observability/metrics package and compare to an independent
//     hardcoded want-set (anti-tautology, order-insensitive). Enumeration is by
//     calling each known accessor function and reading .String().
//   - A2 callsite guard (DOWNSTREAM): scan runtime/grpc/interceptor production
//     files for RecordProtectionRejection calls whose ptype argument is an inline
//     ProtectionType constant that is NOT a call to a ProtectionType accessor.
//     Only accessor-derived values (ProtectionRateLimit() / ProtectionCircuit())
//     may reach the metric label.
//
// # AI-robust rating
//
//   - UPSTREAM: HARD. The ProtectionType struct has a private field (v string),
//     so the type system seals construction: package-outside code cannot build a
//     ProtectionType with an arbitrary string value — only the two accessor
//     functions export valid instances. This archtest adds a Medium external
//     witness: it enumerates and names-checks the accessor return VALUES against
//     the golden, so a new accessor with a new value surfaces here without a
//     compile error. The Hard gate (sealed construction) and the Medium witness
//     (this test) are complementary — the Hard gate prevents injection of
//     arbitrary values; this test freezes the set of accessor values.
//   - DOWNSTREAM: MEDIUM. Scanning RecordProtectionRejection callsites for the
//     ptype arg shape: the sealed ProtectionType prevents inline construction,
//     but a variable relay (ProtectionType stored then passed) is not caught by
//     the callsite scan. Given the Hard upstream seal, the residual risk is
//     effectively zero — but we document the blind spot per charter.
//
// # Blind spots (charter §"工具选定后强制盲区自检")
//
//   - A1 does NOT verify the interceptors actually emit on every denial path
//     (that is the interceptor unit tests' responsibility).
//   - A2 does not follow variable relay chains. Because the upstream ProtectionType
//     is HARD-sealed (sealed struct, no inline construction), the only var-relay
//     values are also accessor-derived, so this is an accepted residual blind spot.
//
// # Reverse self-check (non-vacuous proof)
//
// TestGRPCProtectionRejectedTypeLabelValuesFrozen01_NegativeControl proves a
// synthetic 3rd value or a renamed value is detected.
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const (
	grpcProtectionMetricsPkg     = PlatformFrameworkModulePath + "/runtime/observability/metrics"
	grpcProtectionInterceptorPkg = PlatformFrameworkModulePath + "/runtime/grpc/interceptor"
	grpcProtectionAccessorRL     = "ProtectionRateLimit"
	grpcProtectionAccessorCB     = "ProtectionCircuit"
	grpcProtectionTypeName       = "ProtectionType"
	grpcProtectionRecordFuncName = "RecordProtectionRejection"
)

// wantProtectionTypeValues is the frozen membership of the
// grpc_protection_rejected_total `type` label value set. Updating this list
// requires a simultaneous update to: (1) the ProtectionType accessor functions
// in runtime/observability/metrics/protection_label.go, (2) the interceptor
// emit sites in rate_limit.go and circuit_breaker.go, (3) dashboards/alerts
// referencing grpc_protection_rejected_total{type=...}, and (4)
// docs/ops/alerting-rules.md.
var wantProtectionTypeValues = []string{
	"circuit",
	"ratelimit",
}

// TestGRPCProtectionRejectedTypeLabelValuesFrozen01 freezes the ProtectionType
// accessor value set against the independent hardcoded wantProtectionTypeValues
// (anti-tautology). Both directions: nothing missing, nothing extra.
func TestGRPCProtectionRejectedTypeLabelValuesFrozen01(t *testing.T) {
	t.Parallel()

	var gotValues []string
	visited := false

	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != grpcProtectionMetricsPkg {
			return nil
		}
		visited = true
		gotValues = collectProtectionTypeValues(p)
		return nil
	})

	if !visited {
		t.Fatalf("GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01: package %s not loaded — "+
			"did the package path change?", grpcProtectionMetricsPkg)
	}

	if len(gotValues) == 0 {
		t.Fatalf("GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01: found 0 ProtectionType accessor "+
			"values in %s — did the accessors get renamed or removed?", grpcProtectionMetricsPkg)
	}

	if diff := resultValuesDiff(gotValues, wantProtectionTypeValues); diff != "" {
		t.Fatalf("GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01: ProtectionType accessor value set "+
			"in runtime/observability/metrics drifted from the frozen want-set.\n%s\n"+
			"The type label value set for grpc_protection_rejected_total is frozen to "+
			"{ratelimit,circuit}. If this change is intentional, update ALL sync points in "+
			"the same PR: (1) wantProtectionTypeValues here, (2) protection_label.go accessors, "+
			"(3) rate_limit.go and circuit_breaker.go emit sites, (4) dashboards/alerts, "+
			"(5) docs/ops/alerting-rules.md.", diff)
	}

	if len(gotValues) != len(wantProtectionTypeValues) {
		t.Errorf("GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01: found %d ProtectionType accessors, "+
			"want exactly %d — a 3rd accessor was added without updating the golden; "+
			"see wantProtectionTypeValues", len(gotValues), len(wantProtectionTypeValues))
	}
}

// collectProtectionTypeValues enumerates the ProtectionType accessor functions
// (exported top-level functions returning ProtectionType) in the given package
// and returns their .String() values in sorted order. This binds by RETURN TYPE
// identity, so a function renamed but still returning ProtectionType is still
// counted.
func collectProtectionTypeValues(p *Pass) []string {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil
	}

	// Locate the ProtectionType named type.
	ptypeObj := p.Pkg.Scope().Lookup(grpcProtectionTypeName)
	if ptypeObj == nil {
		return nil
	}
	tn, ok := ptypeObj.(*types.TypeName)
	if !ok {
		return nil
	}
	ptypeType := tn.Type()

	// Collect exported function names that return ProtectionType and have no
	// parameters.
	var accessors []string
	for _, name := range p.Pkg.Scope().Names() {
		fn, ok := p.Pkg.Scope().Lookup(name).(*types.Func)
		if !ok {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Params().Len() != 0 {
			continue
		}
		if sig.Results().Len() != 1 {
			continue
		}
		if !types.Identical(sig.Results().At(0).Type(), ptypeType) {
			continue
		}
		// It's an exported accessor returning ProtectionType with no params.
		accessors = append(accessors, name)
	}

	// Resolve the return values by evaluating the accessor body's return
	// expression. We look for `return ProtectionType{v: "..."}` or
	// `return ProtectionType{"..."}` in the AST.
	values := make([]string, 0, len(accessors))
	for _, accName := range accessors {
		val := resolveProtectionAccessorValue(p, accName)
		if val != "" {
			values = append(values, val)
		}
	}
	return values
}

// resolveProtectionAccessorValue finds the string literal v-field value in the
// ProtectionType returned by the named accessor function. It walks the function
// body for `return ProtectionType{v: "..."}` or inlined `{ "..." }` composite
// literal.
func resolveProtectionAccessorValue(p *Pass, funcName string) string {
	for _, file := range p.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != funcName {
				continue
			}
			if fn.Body == nil {
				continue
			}
			var found string
			EachInSubtree[ast.ReturnStmt](fn.Body, func(ret *ast.ReturnStmt) {
				if found != "" || len(ret.Results) != 1 {
					return
				}
				cl, ok := ret.Results[0].(*ast.CompositeLit)
				if !ok {
					return
				}
				// Try keyed form: ProtectionType{v: "ratelimit"}.
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "v" {
						continue
					}
					lit, ok := kv.Value.(*ast.BasicLit)
					if !ok {
						continue
					}
					found = strings.Trim(lit.Value, `"`)
					return
				}
				// Try positional form: ProtectionType{"ratelimit"}.
				if len(cl.Elts) == 1 {
					lit, ok := cl.Elts[0].(*ast.BasicLit)
					if !ok {
						return
					}
					found = strings.Trim(lit.Value, `"`)
				}
			})
			if found != "" {
				return found
			}
		}
	}
	return ""
}

// scanProtectionEmitCallsites scans the interceptor package for
// RecordProtectionRejection calls and reports any ptype argument that is NOT a
// call to a ProtectionType accessor (ProtectionRateLimit / ProtectionCircuit).
func scanProtectionEmitCallsites(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != grpcProtectionRecordFuncName {
				return
			}
			fn, ok := ResolveMethodCall(info, sel)
			if !ok || fn.Name() != grpcProtectionRecordFuncName {
				return
			}
			// ptype is arg[3] (ctx, cell, method, ptype).
			if len(call.Args) < 4 {
				return
			}
			ptypeArg := call.Args[3]
			if isProtectionAccessorCall(info, ptypeArg) {
				return // valid: coming from an accessor
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(ptypeArg.Pos()).Line,
				Message: "RecordProtectionRejection ptype argument must be a call to a " +
					"ProtectionType accessor (ProtectionRateLimit() or ProtectionCircuit()) — " +
					"not an inline literal, conversion, or foreign value " +
					"(GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01 callsite guard)",
			})
		})
	}
	return diags
}

// isProtectionAccessorCall reports whether expr is a call to one of the two
// ProtectionType accessor functions declared in the metrics package.
func isProtectionAccessorCall(info *types.Info, expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	pkg, name, ok := ResolvePackageRef(info, call.Fun)
	if !ok {
		return false
	}
	if pkg != grpcProtectionMetricsPkg {
		return false
	}
	return name == grpcProtectionAccessorRL || name == grpcProtectionAccessorCB
}

// TestGRPCProtectionRejectedTypeLabelValuesFrozen01_CallsiteGuard is the
// production GREEN baseline for the downstream funnel: every
// RecordProtectionRejection ptype argument in the interceptor package comes from
// a ProtectionType accessor call.
func TestGRPCProtectionRejectedTypeLabelValuesFrozen01_CallsiteGuard(t *testing.T) {
	t.Parallel()

	var allDiags []Diagnostic
	visited := false

	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != grpcProtectionInterceptorPkg {
			return nil
		}
		visited = true
		allDiags = append(allDiags, scanProtectionEmitCallsites(p)...)
		return nil
	})

	if !visited {
		t.Fatalf("GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01 callsite guard: "+
			"package %s not loaded — did the package path change?", grpcProtectionInterceptorPkg)
	}

	Report(t, "GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01", allDiags)
}

// TestGRPCProtectionRejectedTypeLabelValuesFrozen01_NegativeControl proves the
// A1 comparison is non-vacuous: a synthetically drifted set (a 3rd value, or a
// renamed value) MUST produce a non-empty diff.
func TestGRPCProtectionRejectedTypeLabelValuesFrozen01_NegativeControl(t *testing.T) {
	t.Parallel()

	// Extra value "timeout" — a forbidden 3rd label.
	withExtra := append(append([]string(nil), wantProtectionTypeValues...), "timeout")
	if diff := resultValuesDiff(withExtra, wantProtectionTypeValues); diff == "" {
		t.Fatal("GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01 negative control: a set with extra " +
			"'timeout' produced an empty diff — the comparison is vacuous and would not catch a real drift")
	}

	// Missing value — "ratelimit" renamed to "rate_limit".
	withMissing := []string{"circuit", "rate_limit"}
	if diff := resultValuesDiff(withMissing, wantProtectionTypeValues); diff == "" {
		t.Fatal("GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01 negative control: a set with " +
			"'ratelimit' renamed to 'rate_limit' produced an empty diff — the comparison is vacuous")
	}

	// Correct set must produce empty diff (over-fire guard).
	if diff := resultValuesDiff(wantProtectionTypeValues, wantProtectionTypeValues); diff != "" {
		t.Fatalf("GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01 negative control: the frozen "+
			"want-set compared against itself produced a non-empty diff — comparison has a bug: %s", diff)
	}
}
