//go:build archtest

// INVARIANT: IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01
//
// This file owns ONE invariant: the set of string values assigned to the
// exported RequestState consts in runtime/http/idempotency — the value set for
// the `state` label on idempotency_requests_total{cell,state} — is frozen to
// exactly:
//
//	{"acquired", "replayed", "busy", "store_error", "oversize", "key_reused", "body_read_failed"}
//
// These are the seven terminal outcomes of one HTTP idempotency decision. The set
// is design-time bounded: runtime drift (an 8th const, a renamed value) breaks
// dashboards / alerts (replay-storm, busy-rate, key-reused) without a compile
// error. #1460 added the counter; the `cell` label inherits the assembly
// closed-set discipline (#1093 / observability.md §HTTP Metrics cell Label) and
// is NOT this rule's concern.
//
// # Two prongs, mirroring SAGA-METRIC-LABEL-VALUES-FROZEN-01 / RECONCILE-…-01
//
//   - A1 freeze (UPSTREAM): enumerate the string consts of the RequestState TYPE
//     and compare to an independent hardcoded want-set (anti-tautology,
//     order-insensitive). Enumeration is BY TYPE identity (not name prefix), so a
//     const renamed off the "State" mnemonic but still typed RequestState is
//     still counted and an 8th const still fails the count assertion.
//   - A2 callsite + assignment guard (DOWNSTREAM): a `type RequestState string`
//     does NOT stop an inline literal — observeState(ctx, "typo"),
//     RequestState("typo"), and `var s RequestState = "typo"` all compile. The
//     guard bans any compile-time-constant expression of type RequestState that
//     is not a bare reference to a const declared in runtime/http/idempotency, so
//     the only values that can reach the metric label are the frozen consts or a
//     non-constant RequestState (the middleware param relayed into the collector).
//     It scans BOTH the producer package (runtime/http/idempotency — where the
//     consts are emitted via observeState) and the collector package
//     (runtime/observability/metrics — where ObserveRequest(ctx, state) converts
//     string(state)), so an inline literal in either is caught.
//
// # AI-robust rating (charter §"Funnel 双向锁评级" — dual-axis)
//
//   - DOWNSTREAM: HARD. The A2 guard binds the argument's go/types named-type
//     identity to RequestState (ResolvePackageRef-grade resolution via
//     info.Types/info.ObjectOf), so import aliases, RequestState("x")
//     conversions, and foreign-package consts laundered into the type are all
//     form-detected; the form is unique (a constant RequestState expression that
//     is not a declared-in-idempotency const). This is the highest archtest tier
//     for this rule shape — there is no "looks like but isn't" gap.
//   - UPSTREAM: MEDIUM, a GO-LANGUAGE CEILING (not a deferred TODO). Go assigns
//     an untyped literal to a defined string type, so the type system cannot
//     seal "only these 7 RequestState values exist" from inside the package — an
//     in-package author can add an 8th const and the compiler does not object.
//     The A1 archtest is the external frozen-witness. Hard upstream path: enroll
//     the RequestState value set into the metricschema golden so the freeze is
//     byte-locked at codegen time, retiring A1 here — SHARED with saga/reconcile
//     at gh #1416. This is the charter-sanctioned "Medium upstream + Hard
//     downstream" transitional form; gh #1416 already exists, so no new issue.
//
// # Tool blind spots (charter §"工具选定后强制盲区自检")
//
//   - A2 binds on the ARGUMENT's named type being RequestState. A raw-string
//     bypass of the typed enum — e.g. building the label map directly with
//     kernelmetrics.Labels{"state": "rogue"} (a plain string literal, NOT a
//     RequestState) inside the collector — carries type `string`, not
//     RequestState, so the guard does NOT flag it. This is the SAME documented
//     blind spot as saga's `string(reason)` sink; the production collector goes
//     through string(state) of a RequestState param, and that single small file
//     is the reviewed convention. The funnel guarantees: IF a value flows through
//     RequestState it is a frozen const — it does not prevent a parallel raw-map
//     bypass.
//   - A1 does NOT verify the middleware actually emits one of the 7 in every code
//     path (that behavioral coverage is the middleware_test recording-observer
//     assertions), only that the const VALUE set is frozen.
//   - Scope is the two named packages. A RequestState const declared elsewhere is
//     foreign-laundered and rejected by A2's declared-in-idempotency check; it is
//     not separately enumerated by A1 (A1 is the producer package only).
//
// # Reverse self-check (non-vacuous proof)
//
// TestIdempotencyStateLabelValuesFrozen01_NegativeControl proves a synthetic 8th
// value / renamed value is detected; the *_CallsiteGuard_Fixtures RED fixtures
// (inline literal, conversion, foreign const, var relay) prove A2 fires and the
// GREEN fixture proves it does not over-fire.
package archtest

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

const (
	idemStateProducerPkg = PlatformFrameworkModulePath + "/runtime/http/idempotency"
	idemStateMetricsPkg  = PlatformFrameworkModulePath + "/runtime/observability/metrics"
)

// idemStateScanPkgs is the set of packages whose production code the A2 callsite
// guard scans for inline RequestState constants reaching a metric label: the
// producer (middleware emit sites) and the collector (ObserveRequest sink).
var idemStateScanPkgs = map[string]struct{}{
	idemStateProducerPkg: {},
	idemStateMetricsPkg:  {},
}

// wantIdempotencyStateValues is the frozen membership of the
// idempotency_requests_total `state` label value set. Updating this list
// requires a simultaneous update to: (1) the RequestState consts in
// runtime/http/idempotency/metrics.go, (2) the middleware emit sites in
// middleware.go, (3) dashboards/alerts referencing
// idempotency_requests_total{state=...}, and (4) the observability.md
// §"HTTP Idempotency state Label" doc契约.
var wantIdempotencyStateValues = []string{
	"acquired",
	"replayed",
	"busy",
	"store_error",
	"oversize",
	"key_reused",
	"body_read_failed",
}

// idempotencyRequestStateType returns the runtime/http/idempotency RequestState
// named type, or (nil, false) if absent (renamed/removed). p must be that
// package's Pass.
func idempotencyRequestStateType(p *Pass) (types.Type, bool) {
	if p.Pkg == nil {
		return nil, false
	}
	obj := p.Pkg.Scope().Lookup("RequestState")
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return nil, false
	}
	return tn.Type(), true
}

// collectIdempotencyStateConsts enumerates the string constant values of every
// package-scope const in p (which must be runtime/http/idempotency) whose TYPE
// is the RequestState named type.
func collectIdempotencyStateConsts(p *Pass) []string {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil
	}
	stateType, ok := idempotencyRequestStateType(p)
	if !ok {
		return nil
	}
	scope := p.Pkg.Scope()
	var values []string
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		if !types.Identical(c.Type(), stateType) {
			continue
		}
		if c.Val().Kind() != constant.String {
			continue
		}
		values = append(values, constant.StringVal(c.Val()))
	}
	return values
}

// TestIdempotencyStateLabelValuesFrozen01 freezes the RequestState const VALUE
// set against the independent hardcoded want-set (anti-tautology). Both
// directions: nothing missing, nothing extra.
func TestIdempotencyStateLabelValuesFrozen01(t *testing.T) {
	t.Parallel()

	var gotValues []string
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != idemStateProducerPkg {
			return nil
		}
		gotValues = collectIdempotencyStateConsts(p)
		return nil
	})

	if len(gotValues) == 0 {
		t.Fatalf("IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01: found 0 RequestState string consts in %s — "+
			"did the package path change, or were the consts renamed/removed?", idemStateProducerPkg)
	}

	if diff := resultValuesDiff(gotValues, wantIdempotencyStateValues); diff != "" {
		t.Fatalf("IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01: RequestState const value set in "+
			"runtime/http/idempotency drifted from the frozen want-set.\n%s\n"+
			"The state label value set for idempotency_requests_total is frozen to "+
			"{acquired,replayed,busy,store_error,oversize,key_reused,body_read_failed}. If this change is intentional, "+
			"update ALL sync points in the same PR: (1) wantIdempotencyStateValues here, "+
			"(2) runtime/http/idempotency/metrics.go RequestState consts + middleware.go emit sites, "+
			"(3) dashboards/alerts, (4) .claude/rules/gocell/observability.md §HTTP Idempotency state Label.", diff)
	}

	// Reverse self-check: assert exactly 7 RequestState consts exist (same count
	// as the want-set). An 8th const would produce an extra entry AND increment
	// this count.
	if len(gotValues) != len(wantIdempotencyStateValues) {
		t.Errorf("IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01: found %d RequestState string consts, "+
			"want exactly %d — an 8th const was added without updating the golden; see wantIdempotencyStateValues",
			len(gotValues), len(wantIdempotencyStateValues))
	}
}

// idemStateTypeName returns "RequestState" if t is the runtime/http/idempotency
// RequestState named type, else "".
func idemStateTypeName(t types.Type) string {
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != idemStateProducerPkg {
		return ""
	}
	if obj.Name() == "RequestState" {
		return "RequestState"
	}
	return ""
}

// isIdemStateConversion reports whether call is a type conversion to RequestState
// (e.g. idempotency.RequestState("x")). The conversion's operand is itself
// recorded with the RequestState type, so scanning it would double-flag; we skip
// the operand here and let the parent call flag the conversion expression once.
func isIdemStateConversion(info *types.Info, call *ast.CallExpr) bool {
	var sel *ast.Ident
	switch f := call.Fun.(type) {
	case *ast.Ident:
		sel = f
	case *ast.SelectorExpr:
		sel = f.Sel
	default:
		return false
	}
	tn, ok := info.ObjectOf(sel).(*types.TypeName)
	if !ok {
		return false
	}
	return idemStateTypeName(tn.Type()) != ""
}

// isIdemDeclaredConstRef reports whether arg is a bare reference to a const
// declared in runtime/http/idempotency AND typed RequestState. A RequestState
// const declared in any other package (foreign-laundered) is NOT valid.
func isIdemDeclaredConstRef(info *types.Info, arg ast.Expr) bool {
	var obj types.Object
	switch e := arg.(type) {
	case *ast.Ident:
		obj = info.ObjectOf(e)
	case *ast.SelectorExpr:
		obj = info.ObjectOf(e.Sel)
	default:
		return false
	}
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	if c.Pkg() == nil || c.Pkg().Path() != idemStateProducerPkg {
		return false
	}
	return idemStateTypeName(c.Type()) != ""
}

// idemStateConstViolation returns "RequestState" if expr is an inline
// RequestState-typed constant that is NOT a valid declared-in-idempotency const
// reference, "" otherwise. Shared by the callsite and assignment guards.
func idemStateConstViolation(info *types.Info, expr ast.Expr) string {
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil {
		return "" // non-constant (var, func call) — allowed
	}
	if idemStateTypeName(tv.Type) == "" {
		return "" // not the RequestState type
	}
	if isIdemDeclaredConstRef(info, expr) {
		return "" // valid frozen idempotency const reference
	}
	return "RequestState"
}

// scanIdemStateCallsites flags any CallExpr argument whose go/types type is
// RequestState AND is a compile-time constant that is not a valid declared
// idempotency const reference (an inline string literal, a RequestState("x")
// conversion, or a RequestState const declared outside runtime/http/idempotency).
func scanIdemStateCallsites(p *Pass) []Diagnostic {
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
			if isIdemStateConversion(info, call) {
				return // operand handled by the parent call that receives the conversion
			}
			for _, arg := range call.Args {
				if idemStateConstViolation(info, arg) == "" {
					continue // allowed: non-constant, not RequestState, or valid frozen const
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(arg.Pos()).Line,
					Message: "inline constant of RequestState reaches a metric label — pass a declared " +
						"idempotency.State* const (StateAcquired/StateReplayed/StateBusy/StateStoreError/" +
						"StateOversize/StateKeyReused) or a non-constant RequestState, not a string literal " +
						"or RequestState(...) conversion (IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 callsite guard)",
				})
			}
		})
	}
	return diags
}

// scanIdemStateAssignments flags any var declaration or assignment whose LHS has
// type RequestState and whose RHS is an inline constant that is not a valid
// declared idempotency const reference. This catches the var-relay bypass
// (`var s RequestState = "typo"`) the callsite guard cannot see (tv.Value==nil at
// the callsite). const GenDecls are EXCLUDED: the RequestState const block in
// metrics.go IS the frozen-set source of truth (enumerated + value-frozen by A1).
func scanIdemStateAssignments(p *Pass) []Diagnostic {
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
		EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
			if gd.Tok != token.VAR {
				return
			}
			EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
				for i, val := range vs.Values {
					if i >= len(vs.Names) {
						continue
					}
					if idemStateTypeName(idemLHSType(info, vs.Names[i])) == "" {
						continue // LHS is not RequestState (covers explicit + inferred type)
					}
					if idemStateConstViolation(info, val) == "" {
						continue
					}
					diags = append(diags, idemStateAssignDiag(p, rel, val))
				}
			})
		})
		EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
			for i, rhs := range as.Rhs {
				if i >= len(as.Lhs) {
					continue
				}
				if idemStateTypeName(idemLHSType(info, as.Lhs[i])) == "" {
					continue // LHS is not RequestState (covers `=` use and `:=` define)
				}
				if idemStateConstViolation(info, rhs) == "" {
					continue
				}
				diags = append(diags, idemStateAssignDiag(p, rel, rhs))
			}
		})
	}
	return diags
}

// idemLHSType resolves the go/types type of an assignment/declaration LHS
// expression. For a bare identifier it uses info.ObjectOf — which covers BOTH a
// defining ident (`var y = …` / `y := …`, whose type is recorded in info.Defs,
// NOT info.Types) and a re-assigned use (`y = …`, in info.Uses). This is the
// difference that closes the inferred-type var-relay bypass
// (`var y = RequestState("x")`): info.Types[name] is absent for definitions, so
// the earlier lookup silently skipped them. Non-ident LHS (selector/index) fall
// back to info.Types. Returns nil if unresolved.
func idemLHSType(info *types.Info, lhs ast.Expr) types.Type {
	if id, ok := lhs.(*ast.Ident); ok {
		if obj := info.ObjectOf(id); obj != nil {
			return obj.Type()
		}
	}
	if tv, ok := info.Types[lhs]; ok {
		return tv.Type
	}
	return nil
}

// idemStateAssignDiag builds the shared assignment-guard diagnostic.
func idemStateAssignDiag(p *Pass, rel string, expr ast.Expr) Diagnostic {
	return Diagnostic{
		Rel:  rel,
		Line: p.Fset.Position(expr.Pos()).Line,
		Message: "variable of RequestState initialized/assigned from an inline constant — use a declared " +
			"idempotency.State* const or a non-constant RequestState to avoid laundering an arbitrary " +
			"value into the frozen set (IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 assignment guard)",
	}
}

// scanIdemStateAll runs both guards on a single Pass and merges diagnostics.
func scanIdemStateAll(p *Pass) []Diagnostic {
	return append(scanIdemStateCallsites(p), scanIdemStateAssignments(p)...)
}

// TestIdempotencyStateLabelValuesFrozen01_CallsiteGuard is the production GREEN
// baseline for the downstream funnel: every RequestState value reaching a metric
// label in the producer (middleware) and collector packages is a declared const
// or a non-constant relay — never an inline literal.
func TestIdempotencyStateLabelValuesFrozen01_CallsiteGuard(t *testing.T) {
	t.Parallel()

	var allDiags []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		if _, scan := idemStateScanPkgs[p.Pkg.Path()]; !scan {
			return nil
		}
		allDiags = append(allDiags, scanIdemStateAll(p)...)
		return nil
	})

	Report(t, "IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01", allDiags)
}

// TestIdempotencyStateLabelValuesFrozen01_NegativeControl proves the A1
// comparison is non-vacuous: a synthetically drifted set (an 8th value, or a
// renamed value) MUST produce a non-empty diff.
func TestIdempotencyStateLabelValuesFrozen01_NegativeControl(t *testing.T) {
	t.Parallel()

	// Extra value "fingerprint_pass" — a forbidden 8th label.
	withExtra := append(append([]string(nil), wantIdempotencyStateValues...), "fingerprint_pass")
	if diff := resultValuesDiff(withExtra, wantIdempotencyStateValues); diff == "" {
		t.Fatal("IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 negative control: a set with an extra 8th " +
			"value produced an empty diff — the comparison is vacuous and would not catch a real drift")
	}

	// Missing value — "key_reused" renamed to "reused".
	withMissing := []string{"acquired", "replayed", "busy", "store_error", "oversize", "reused", "body_read_failed"}
	if diff := resultValuesDiff(withMissing, wantIdempotencyStateValues); diff == "" {
		t.Fatal("IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 negative control: a set with 'key_reused' " +
			"renamed to 'reused' produced an empty diff — the comparison is vacuous")
	}

	// Correct set must produce empty diff (over-fire guard).
	if diff := resultValuesDiff(wantIdempotencyStateValues, wantIdempotencyStateValues); diff != "" {
		t.Fatalf("IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 negative control: the frozen want-set "+
			"compared against itself produced a non-empty diff — the comparison has a bug: %s", diff)
	}
}

// TestIdempotencyStateLabelValuesFrozen01_CallsiteGuard_Fixtures proves the A2
// guard is non-vacuous: each RED fixture (inline literal + conversion, foreign
// const, var relay) is flagged with the expected count, and the GREEN fixture
// (declared const + non-constant relay + string(state) sink) is not.
func TestIdempotencyStateLabelValuesFrozen01_CallsiteGuard_Fixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir  string
		want int
	}{
		{"red_literal", 2},       // inline literal plus a RequestState conversion
		{"red_foreign_const", 1}, // RequestState const declared outside the producer pkg
		{"red_var_relay", 3},     // explicit-var, inferred-var, and short-var-decl forms
		{"green", 0},
	}
	for _, c := range cases {
		c := c
		t.Run(c.dir, func(t *testing.T) {
			t.Parallel()
			pattern := "./tools/archtest/testdata/idempotency_state_callsite_fixtures/" + c.dir
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), scanIdemStateAll)
			if len(diags) != c.want {
				t.Fatalf("A2 fixture %s: want %d diagnostic(s), got %d: %v",
					c.dir, c.want, len(diags), diags)
			}
		})
	}
}
