// bootstrap_autowire_collector_funnel_test.go — closes the DOWNSTREAM side of
// the bootstrap metric-collector auto-wire single-source funnel.
//
//   - INVARIANT: BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01
//
// # What this guards
//
// runtime/bootstrap auto-wires four metric collector families at startup
// (HTTP requests, event-router subscriptions, outbox rejects, projection
// metrics). Each share the SAME discipline: skip on nil/Nop provider →
// construct ONCE → cache → and — critically — return a startup-fatal error on a
// registration conflict, NEVER a slog.Warn-then-degrade that silently drops the
// metric family. That discipline used to live as four hand-copied control-flow
// blocks. #1399 (projection PR-04a regression) proved the copy-paste is a drift
// surface: an AI co-author transcribed a wrong description of the HTTP collector
// (believing it warn-degraded) into buildOneProjection, so the second projection
// silently lost its metrics — and NOTHING mechanical caught it.
//
// The fix collapses the four blocks into one generic single source,
// runtime/bootstrap.autoWireCachedCollector[T], which all four callers route
// through. This archtest is the machine backstop that keeps it single-source.
// It enforces TWO bound checks, not a loose "ancestor exists" check:
//
//  1. CONSTRUCT-ARG POSITION BIND. Each of the four metric-collector
//     CONSTRUCTORS may only be referenced (called, or passed as a function
//     value) from WITHIN the construct ARGUMENT (the 3rd positional arg) of a
//     call to autoWireCachedCollector — either AS that argument (the direct
//     function value form: event/outbox/projection) or nested inside it (the
//     FuncLit form: HTTP, which needs an extra config arg). A reference in any
//     OTHER argument slot (e.g. buried in the conflictMsg string expression) is
//     NOT routed through the helper's skip/cache/fail-fast and is reported. The
//     earlier "any autoWireCachedCollector ancestor" check let such other-arg
//     references slip; this binds to the construct slot specifically.
//  2. CONSTRUCT FUNCLIT PASSTHROUGH LOCK. When the construct argument is a
//     FuncLit, its body must be exactly `return <constructor>(...)` — a single
//     return of a constructor call, nothing else. This forbids re-introducing
//     the #1399 warn-then-degrade INSIDE the construct closure (construct its
//     own collector, register it, swallow the conflict error, return a nil
//     error so the helper caches a degraded family). The helper owns the
//     fail-fast; the construct closure may ONLY construct.
//
// A future auto-wire that calls a constructor directly and re-implements the
// skip/cache/fail-fast control flow inline (the exact #1399 failure mode) has
// no construct-arg routing → CI red. The detector is exercised against a
// synthetic fixture package (planted-bypass reverse test below) so the FIRE
// path is proven, not merely the PASS path.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: MEDIUM by archtest. The constructor symbol is resolved via
//     go/types (ResolvePackageRef), so import aliases are resolved to the same
//     symbol — no "looks like but isn't" gap. The ancestor check binds each
//     reference to an autoWireCachedCollector CallExpr resolved through
//     info.Uses (not a name-string anchor). It is archtest-bound, not a type
//     system gate, hence Medium not Hard.
//   - Upstream: MEDIUM, and this is a GO-LANGUAGE CEILING, not a deferred TODO.
//     Hard upstream would require the four constructors to be uncallable except
//     from autoWireCachedCollector. They cannot be: the constructors are
//     EXPORTED across package boundaries (runtime/observability/metrics and
//     kernel/projection are distinct packages from runtime/bootstrap), so Go
//     visibility cannot express "only this one function may call this exported
//     constructor". This is the same permanent ceiling documented for
//     SPAN-SETATTR-HOLDER-SEAL (#851), HEALTHZ-HOLDER-SEAL (#893), and
//     CTXKEYS-PRINCIPAL-WRITE (#1282). Tracked as a won't-do at gh #1474.
//     The downstream archtest is the enforcement; the ceiling is documented.
//
// # Detection is REFERENCE-based, not call-based
//
// The scanner matches every SelectorExpr that go/types resolves to a
// constructor symbol — whether callee of a call OR passed as a function value.
// This closes the "store the constructor in a variable then call it" gap: the
// reference at the `metricsmiddleware.NewEventRouterCollector` SelectorExpr is
// caught regardless of where the value is later invoked.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Multi-statement construct FuncLit that legitimately needs to build config
//     before the constructor call is NOT expressible: the passthrough lock
//     requires a single `return <ctor>(...)`. This is intentional — config must
//     be a sub-expression of the return (as the HTTP closure does with an inline
//     ProviderCollectorConfig{}) or a separate top-level helper, never a place to
//     smuggle degrade logic. If a future caller genuinely needs a multi-statement
//     construct body, the lock (constructFuncLitPassthroughViolation) must be
//     revisited deliberately, not loosened ad hoc.
//   - Dot-import bare-identifier form (import . "…/observability/metrics";
//     NewEventRouterCollector(p)) references the symbol as a bare *ast.Ident, not
//     a SelectorExpr, so the SelectorExpr scan would not match it. This blind spot
//     is CLOSED by a reverse self-check below: bootstrap is forbidden from
//     dot-importing either funneled package (autoWireFunneledPkgs), keeping the
//     bare-ident form unrepresentable.
//   - Raw ad-hoc collector construction that bypasses the four named constructors
//     entirely (e.g. calling b.metricsProvider.GaugeVec/CounterVec/HistogramVec
//     directly in bootstrap to assemble a private collector) is OUT of this
//     funnel's scope — it is the four constructors' own concern. Documented as a
//     known gap; bootstrap does not do this today.
//   - Scope is the runtime/bootstrap package only (where the single-source funnel
//     lives). The same constructors called from other production packages are not
//     this rule's concern (today there are none outside bootstrap).
//   - The anti-vacuity guard below (every constructor symbol must be referenced
//     ≥1× inside bootstrap) is the reverse self-check: it proves the scanner
//     actually resolves the real references (not a vacuous pass) AND that the four
//     callers still route a live reference — a constructor that stops being
//     referenced means a caller was deleted or stopped using the funnel.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const autoWireBootstrapPkgPath = PlatformModulePath + "/runtime/bootstrap"

// autoWireHelperName is the single-source generic helper every bootstrap metric
// auto-wire must route through. It is an unexported package-local func in
// runtime/bootstrap, so a name match on a *types.Func resolved via info.Uses is
// unambiguous (no cross-package homonym is reachable as a bare ident here).
const autoWireHelperName = "autoWireCachedCollector"

const (
	autoWireRuntimeMetricsPkgPath = PlatformModulePath + "/runtime/observability/metrics"
	autoWireProjectionPkgPath     = PlatformModulePath + "/kernel/projection"
)

// autoWireCtorSymbols maps each {pkgPath, funcName} metric-collector constructor
// to a stable display symbol. These are the only collector constructors the
// bootstrap auto-wire funnel may invoke, and only from inside autoWireCachedCollector.
var autoWireCtorSymbols = map[[2]string]string{
	{autoWireRuntimeMetricsPkgPath, "NewProviderCollector"}:     "metricsmiddleware.NewProviderCollector",
	{autoWireRuntimeMetricsPkgPath, "NewEventRouterCollector"}:  "metricsmiddleware.NewEventRouterCollector",
	{autoWireRuntimeMetricsPkgPath, "NewOutboxRejectCollector"}: "metricsmiddleware.NewOutboxRejectCollector",
	{autoWireProjectionPkgPath, "RegisterMetrics"}:              "projection.RegisterMetrics",
}

// autoWireFunneledPkgs is the set of packages whose collector constructors the
// funnel guards. A bootstrap dot-import of any of these would expose those
// constructors as bare identifiers and bypass the SelectorExpr scan, so the
// reverse self-check forbids it.
var autoWireFunneledPkgs = map[string]struct{}{
	autoWireRuntimeMetricsPkgPath: {},
	autoWireProjectionPkgPath:     {},
}

// autoWireConstructArgIndex is the positional index of the construct argument in
// autoWireCachedCollector(b, cache, construct, conflictMsg). Constructor
// references are bound to THIS argument specifically — a reference in any other
// slot (cache / conflictMsg) is NOT routed through the helper's fail-fast.
const autoWireConstructArgIndex = 2

// TestBootstrapAutoWireCollectorFunnel01 asserts that every reference to a
// metric-collector constructor inside runtime/bootstrap production code is bound
// to the construct argument of an autoWireCachedCollector call (directly, or
// inside a single-return passthrough FuncLit), that no construct FuncLit hides
// degrade logic, and that no constructor symbol is unreferenced (anti-vacuity).
func TestBootstrapAutoWireCollectorFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]bool{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != autoWireBootstrapPkgPath {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			d = append(d, scanAutoWireDotImports(p, file)...)
		}
		d = append(d, scanAutoWireCollectorFunnel(p, autoWireCtorSymbols, autoWireHelperName, observed)...)
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: every constructor symbol must
	// be referenced from inside bootstrap. A symbol that disappears means a caller
	// was deleted or stopped routing through the funnel — surface it loudly so the
	// rule never silently passes vacuously.
	for _, symbol := range sortedAutoWireCtorSymbols() {
		if !observed[symbol] {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01: constructor %s is NEVER referenced in "+
						"runtime/bootstrap — the scanner found no live reference. Either the auto-wire caller "+
						"was removed, or the scanner stopped resolving it (regression). The rule must observe a "+
						"live reference for every constructor it claims to funnel.",
					symbol),
			})
		}
	}

	Report(t, "BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01", diags)
}

// TestBootstrapAutoWireCollectorFunnel01_PlantedBypass exercises the FIRE path of
// scanAutoWireCollectorFunnel against a synthetic fixture package, so the rule's
// rejection behavior is proven — not just its PASS on real bootstrap. The fixture
// declares two allowed forms (direct funcval / single-return FuncLit) and three
// bypasses (constructor in a non-construct argument; naked constructor call with
// no helper; multi-statement degrade-swallowing construct FuncLit). The detector
// must flag exactly the three bypass functions and neither allowed one.
func TestBootstrapAutoWireCollectorFunnel01_PlantedBypass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkgPath := modPath + "/tools/archtest/internal/autowirefunnelfixture"
	collectorsPkgPath := fixturePkgPath + "/collectors"
	fixturePattern := "./tools/archtest/internal/autowirefunnelfixture/..."

	// Fixture-local constructor symbol set, keyed exactly as the production map.
	fixtureCtors := map[[2]string]string{
		{collectorsPkgPath, "NewAlpha"}: "collectors.NewAlpha",
		{collectorsPkgPath, "NewBeta"}:  "collectors.NewBeta",
	}

	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{fixturePattern}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != fixturePkgPath {
			return nil
		}
		return scanAutoWireCollectorFunnel(p, fixtureCtors, autoWireHelperName, nil)
	})

	joined := ""
	for _, dg := range diags {
		t.Logf("planted: %s", dg.Message)
		joined += dg.Message + "\n"
	}

	// Exactly three bypasses must fire: the other-arg reference, the naked call,
	// and the swallowing construct FuncLit.
	assert.Contains(t, joined, "bypassOtherArg",
		"constructor referenced in a non-construct argument must be reported")
	assert.Contains(t, joined, "bypassNaked",
		"naked constructor call with no helper must be reported")
	assert.Contains(t, joined, "bypassSwallowClosure",
		"multi-statement degrade-swallowing construct FuncLit must be reported")
	assert.NotContains(t, joined, "allowedDirect",
		"direct constructor function value as the construct arg must NOT be reported")
	assert.NotContains(t, joined, "allowedClosure",
		"single-return passthrough construct FuncLit must NOT be reported")
	assert.Lenf(t, diags, 3,
		"exactly three planted bypasses must fire (other-arg + naked + swallow); got %d:\n%s",
		len(diags), joined)
}

// scanAutoWireDotImports is the blind-spot reverse self-check: a dot-import of a
// funneled package would let its constructor be referenced as a bare *ast.Ident
// (not a SelectorExpr), bypassing the SelectorExpr scan. Assert bootstrap never
// dot-imports either funneled package so that blind spot stays vacuous.
func scanAutoWireDotImports(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	var d []Diagnostic
	for _, imp := range file.Imports {
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if _, funneled := autoWireFunneledPkgs[path]; funneled {
			pos := p.Fset.Position(imp.Pos())
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01: dot-import of funneled package %q in bootstrap "+
						"would expose its collector constructors as bare identifiers, bypassing the "+
						"SelectorExpr-based funnel scan. Import it qualified.", path),
			})
		}
	}
	return d
}

// scanAutoWireCollectorFunnel is the shared detector used by both the real
// bootstrap scan and the planted-bypass fixture. For every constructor reference
// (ctorSymbols) it requires the reference to be bound to the construct argument
// of a helperName call (position bind); for every helper call whose construct
// argument is a FuncLit it requires a single `return <ctor>(...)` body
// (passthrough lock). observed, when non-nil, records each referenced symbol for
// the caller's anti-vacuity check.
func scanAutoWireCollectorFunnel(
	p *Pass,
	ctorSymbols map[[2]string]string,
	helperName string,
	observed map[string]bool,
) []Diagnostic {
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		// ancestors holds the AST path from the file root down to (but not
		// including) the node currently being visited. Push after the check, pop
		// on the post-visit nil callback — every non-nil node returns true so each
		// push has a matching pop.
		var ancestors []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				ancestors = ancestors[:len(ancestors)-1]
				return true
			}
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if symbol, matched := autoWireCtorSymbolIn(p.TypesInfo, node, ctorSymbols); matched {
					if observed != nil {
						observed[symbol] = true
					}
					if !ctorRoutesThroughConstructArg(p.TypesInfo, node, ancestors, helperName) {
						pos := p.Fset.Position(node.Pos())
						d = append(d, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01: %s in %s is referenced outside the construct "+
									"argument of %s. Every bootstrap metric-collector construction MUST route through the "+
									"single-source helper so the skip/cache/fail-fast discipline cannot be re-implemented "+
									"inline (the #1399 warn-then-degrade regression). Pass the constructor as the construct "+
									"argument (or invoke it inside that single-return construct FuncLit); do not reference it "+
									"elsewhere. Correct form: `autoWireCachedCollector(b, &b.myCollector, %s, \"...conflict msg...\")`.",
								symbol, enclosingFuncOrScope(file, node.Pos()), helperName, symbol),
						})
					}
				}
			case *ast.CallExpr:
				if isAutoWireHelperCall(p.TypesInfo, node, helperName) {
					if msg, bad := constructFuncLitPassthroughViolation(p.TypesInfo, node, ctorSymbols); bad {
						pos := p.Fset.Position(node.Args[autoWireConstructArgIndex].Pos())
						d = append(d, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01: construct FuncLit in %s %s The helper owns the "+
									"skip/cache/fail-fast; the construct closure may ONLY construct — body must be exactly "+
									"`return <constructor>(...)`. A multi-statement body can register + swallow the conflict "+
									"error + return nil, silently degrading the metric family (the #1399 regression "+
									"re-introduced through the funnel).",
								enclosingFuncOrScope(file, node.Pos()), msg),
						})
					}
				}
			}
			ancestors = append(ancestors, n)
			return true
		})
	}
	return d
}

// autoWireCtorSymbolIn resolves a SelectorExpr REFERENCE (call or function value)
// to one of the given constructor symbols, alias-proof via go/types.
func autoWireCtorSymbolIn(info *types.Info, sel *ast.SelectorExpr, syms map[[2]string]string) (string, bool) {
	pkgPath, name, ok := ResolvePackageRef(info, sel)
	if !ok {
		return "", false
	}
	sym, found := syms[[2]string{pkgPath, name}]
	return sym, found
}

// ctorRoutesThroughConstructArg reports whether sel lies within the construct
// argument (autoWireConstructArgIndex) of some ancestor helperName call —
// resolved via info.Uses on the call's Fun ident (not a name-string anchor).
// Containment is by source position range, so it covers both the direct-funcval
// form (sel == construct arg) and the FuncLit form (sel nested inside it). A
// reference in any other argument slot is NOT considered routed.
func ctorRoutesThroughConstructArg(info *types.Info, sel *ast.SelectorExpr, ancestors []ast.Node, helperName string) bool {
	for _, n := range ancestors {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isAutoWireHelperCall(info, call, helperName) {
			continue
		}
		if len(call.Args) <= autoWireConstructArgIndex {
			continue
		}
		arg := call.Args[autoWireConstructArgIndex]
		if arg.Pos() <= sel.Pos() && sel.End() <= arg.End() {
			return true
		}
	}
	return false
}

// isAutoWireHelperCall reports whether call invokes the unqualified helperName
// func, resolved via info.Uses on the call's Fun ident.
func isAutoWireHelperCall(info *types.Info, call *ast.CallExpr, helperName string) bool {
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	return ok && fn.Name() == helperName
}

// constructFuncLitPassthroughViolation enforces the passthrough lock: when the
// construct argument of a helper call is a FuncLit, its body must be exactly
// `return <ctor>(...)`. Returns a human-readable reason and true on violation;
// returns false when the construct arg is not a FuncLit (the direct-funcval form,
// to which the lock does not apply) or when the body is a clean passthrough.
func constructFuncLitPassthroughViolation(info *types.Info, call *ast.CallExpr, ctorSymbols map[[2]string]string) (string, bool) {
	if len(call.Args) <= autoWireConstructArgIndex {
		return "", false
	}
	fl, ok := call.Args[autoWireConstructArgIndex].(*ast.FuncLit)
	if !ok {
		return "", false
	}
	if fl.Body == nil || len(fl.Body.List) != 1 {
		return "is not a single-statement passthrough.", true
	}
	ret, ok := fl.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "does not return a single constructor-call expression.", true
	}
	inner, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return "does not return a constructor call.", true
	}
	sel, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok {
		return "does not return a recognized constructor call.", true
	}
	if _, isCtor := autoWireCtorSymbolIn(info, sel, ctorSymbols); !isCtor {
		return "returns a call that is not one of the funneled constructors.", true
	}
	return "", false
}

// enclosingFuncOrScope names the function enclosing pos, or "<package scope>".
func enclosingFuncOrScope(file *ast.File, pos token.Pos) string {
	if fn := enclosingFuncName(file, pos); fn != "" {
		return fn
	}
	return "<package scope>"
}

func sortedAutoWireCtorSymbols() []string {
	out := make([]string, 0, len(autoWireCtorSymbols))
	for _, sym := range autoWireCtorSymbols {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}
