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
// through. This archtest is the machine backstop that keeps it single-source:
// the four metric-collector CONSTRUCTORS may only be referenced (called, or
// passed as a function value) from WITHIN a call to autoWireCachedCollector —
// either as the construct argument directly (event/outbox/projection) or inside
// the construct closure (HTTP, which needs an extra config arg). A future
// auto-wire that calls a constructor directly and re-implements the
// skip/cache/fail-fast control flow inline (the exact #1399 failure mode) has
// no autoWireCachedCollector ancestor → CI red.
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
	"go/types"
	"sort"
	"strings"
	"testing"
)

const autoWireBootstrapPkgPath = "github.com/ghbvf/gocell/runtime/bootstrap"

// autoWireHelperName is the single-source generic helper every bootstrap metric
// auto-wire must route through. It is an unexported package-local func in
// runtime/bootstrap, so a name match on a *types.Func resolved via info.Uses is
// unambiguous (no cross-package homonym is reachable as a bare ident here).
const autoWireHelperName = "autoWireCachedCollector"

// autoWireCtorSymbols maps each {pkgPath, funcName} metric-collector constructor
// to a stable display symbol. These are the only collector constructors the
// bootstrap auto-wire funnel may invoke, and only from inside autoWireCachedCollector.
var autoWireCtorSymbols = map[[2]string]string{
	{"github.com/ghbvf/gocell/runtime/observability/metrics", "NewProviderCollector"}:     "metricsmiddleware.NewProviderCollector",
	{"github.com/ghbvf/gocell/runtime/observability/metrics", "NewEventRouterCollector"}:  "metricsmiddleware.NewEventRouterCollector",
	{"github.com/ghbvf/gocell/runtime/observability/metrics", "NewOutboxRejectCollector"}: "metricsmiddleware.NewOutboxRejectCollector",
	{"github.com/ghbvf/gocell/kernel/projection", "RegisterMetrics"}:                      "projection.RegisterMetrics",
}

// autoWireFunneledPkgs is the set of packages whose collector constructors the
// funnel guards. A bootstrap dot-import of any of these would expose those
// constructors as bare identifiers and bypass the SelectorExpr scan, so the
// reverse self-check forbids it.
var autoWireFunneledPkgs = map[string]struct{}{
	"github.com/ghbvf/gocell/runtime/observability/metrics": {},
	"github.com/ghbvf/gocell/kernel/projection":             {},
}

// TestBootstrapAutoWireCollectorFunnel01 asserts that every reference to a
// metric-collector constructor inside runtime/bootstrap production code is
// nested within a call to autoWireCachedCollector (directly as the construct arg
// or inside its construct closure), and that no constructor symbol is unreferenced
// (anti-vacuity reverse check).
func TestBootstrapAutoWireCollectorFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]bool{}

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != autoWireBootstrapPkgPath {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			// Blind-spot reverse self-check: a dot-import of a funneled package
			// would let its constructor be referenced as a bare *ast.Ident (not a
			// SelectorExpr), bypassing the scan below. Assert bootstrap never
			// dot-imports either funneled package so that blind spot stays vacuous.
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
			// ancestors holds the AST path from the file root down to (but not
			// including) the node currently being visited. Push after the check,
			// pop on the post-visit nil callback — every non-nil node returns
			// true so each push has a matching pop.
			var ancestors []ast.Node
			ast.Inspect(file, func(n ast.Node) bool {
				if n == nil {
					ancestors = ancestors[:len(ancestors)-1]
					return true
				}
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if symbol, matched := autoWireCtorSymbol(p.TypesInfo, sel); matched {
						observed[symbol] = true
						if !ancestorRoutesThroughAutoWire(p.TypesInfo, ancestors) {
							pos := p.Fset.Position(sel.Pos())
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: pos.Line,
								Message: fmt.Sprintf(
									"BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01: %s is referenced outside a call to "+
										"%s. Every bootstrap metric-collector construction MUST route through the "+
										"single-source helper so the skip/cache/fail-fast discipline cannot be "+
										"re-implemented inline (the #1399 warn-then-degrade regression). Pass the "+
										"constructor as the construct argument of autoWireCachedCollector (or invoke "+
										"it inside that construct closure); do not call it directly. Correct form: "+
										"`autoWireCachedCollector(b, &b.myCollector, %s, \"...conflict msg...\")`.",
									symbol, autoWireHelperName, symbol),
							})
						}
					}
				}
				ancestors = append(ancestors, n)
				return true
			})
		}
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

// autoWireCtorSymbol resolves a SelectorExpr REFERENCE (call or function value)
// to one of the metric-collector constructor symbols, alias-proof via go/types.
func autoWireCtorSymbol(info *types.Info, sel *ast.SelectorExpr) (string, bool) {
	pkgPath, name, ok := ResolvePackageRef(info, sel)
	if !ok {
		return "", false
	}
	sym, found := autoWireCtorSymbols[[2]string{pkgPath, name}]
	return sym, found
}

// ancestorRoutesThroughAutoWire reports whether any ancestor node is a call to
// the autoWireCachedCollector helper, resolved via info.Uses on the call's Fun
// ident (not a name-string anchor). Covers both the direct-function-value arg
// form and the construct-closure form.
func ancestorRoutesThroughAutoWire(info *types.Info, ancestors []ast.Node) bool {
	for _, n := range ancestors {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			continue
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			continue
		}
		if fn, ok := info.Uses[id].(*types.Func); ok && fn.Name() == autoWireHelperName {
			return true
		}
	}
	return false
}

func sortedAutoWireCtorSymbols() []string {
	out := make([]string, 0, len(autoWireCtorSymbols))
	for _, sym := range autoWireCtorSymbols {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}
