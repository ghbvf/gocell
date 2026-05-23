// invariants:
//   - INVARIANT: OBS-01
//   - INVARIANT: METRICS-GAUGEVEC-FUNNEL-01
//   - INVARIANT: METRICS-GAUGEVEC-UPSTREAM-HARD-01
//   - INVARIANT: METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01

package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/metricschema"
)

// TestMetricLabelErrcodeClassifiersRequireAck enforces OBS-01 in the typed
// tools layer, where go/packages can identify the real errcode classifier
// functions and CI can treat missing machine-readable acknowledgements as a
// merge blocker.
func TestMetricLabelErrcodeClassifiersRequireAck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based observability archtest in -short mode")
	}
	root := findModuleRoot(t)

	diagnostics, err := metricschema.CheckOBS01(t.Context(), root)
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

// TestGaugeVecFunnel enforces METRICS-GAUGEVEC-FUNNEL-01.
//
// Production code outside adapters/prometheus/internal/promwrap/ and
// adapters/otel/internal/otelwrap/ must NEVER call prom.New* constructors or
// otelmetric.Meter synchronous instrument constructors directly. All metric
// construction must route through the respective internal wrap packages.
//
// # Funnel grade: Hard upstream + Hard downstream
//
// Downstream (Hard): callee resolved via *types.Info to the banned symbols;
// form-uniqueness on (callee-pkg, callee-name) gives Hard — there is no
// "looks-like-but-isn't" gray zone for the prom.New* or Meter.Float64*/Int64*
// call forms.
//
// Upstream (Hard): Go internal/ package closure prevents external imports of
// adapters/prometheus/internal/promwrap and adapters/otel/internal/otelwrap
// from outside the respective adapter subtrees at compile time. The ban's
// allowed scope is now restricted to those exact internal packages, not the
// broader adapter directory.
//
// # Allowed callsite scopes
//
//   - adapters/prometheus/internal/promwrap/ — the sole sanctioned Prometheus
//     implementation site; Go internal/ gate enforces adapter-subtree ownership.
//   - adapters/otel/internal/otelwrap/ — the sole sanctioned OTel meter
//     implementation site; Go internal/ gate enforces adapter-subtree ownership.
//
// # Blind spots (production AST forms NOT matched; asserted absent below)
//
// BS-1 Functions calling New* via reflection (reflect.ValueOf):
// kept documented — production code does not use reflect for metrics
// construction. Reverse self-check: TestGaugeVecFunnel_SelfCheck BS-1 asserts
// no reflect.ValueOf call site references the banned symbol names in any
// production file.
//
// BS-2 Cross-package value forwarding of *prom.Registry then calling
// RegisterOrReuse internally: out of scope — callers forwarding a Registry do
// not themselves call New*; legitimate provider-internal usage.
//
// BS-3 Method-value indirection: `var fn = prom.NewGaugeVec; fn(...)` — the
// function-value form has Fun as *ast.Ident (variable) not *ast.SelectorExpr,
// so *types.Info.Uses resolve to a *types.Var, not *types.Func. Accepted:
// production code has no such pattern. For the OTel side this manifests as
// `var fn = meter.Float64Gauge` (method-value capture). Reverse self-check:
// BS-3 check below covers both prom function-value and OTel method-value forms.
//
// # RED fixture
//
// tools/archtest/internal/metricsgaugevecfixture/fixture.go provides nineteen
// intentional violations:
//
//   - BadPromGaugeVec (prom.NewGaugeVec)
//   - BadPromCounter (prom.NewCounter)
//   - BadPromCounterVec (prom.NewCounterVec)
//   - BadPromHistogramVec (prom.NewHistogramVec)
//   - BadPromGauge (prom.NewGauge) — B2 follow-up prefix predicate
//   - BadPromGaugeFunc (prom.NewGaugeFunc) — B2 follow-up prefix predicate
//   - BadPromHistogram (prom.NewHistogram) — defensive ban
//   - BadPromSummary (prom.NewSummary) — defensive ban
//   - BadPromSummaryVec (prom.NewSummaryVec) — defensive ban
//   - BadPromCounterFunc (prom.NewCounterFunc) — defensive ban
//   - BadPromUntypedFunc (prom.NewUntypedFunc) — defensive ban
//   - BadOtelUpDownCounter (meter.Float64UpDownCounter)
//   - BadOtelFloat64Gauge (meter.Float64Gauge)
//   - BadOtelFloat64Counter (meter.Float64Counter)
//   - BadOtelFloat64Histogram (meter.Float64Histogram)
//   - BadOtelInt64Counter (meter.Int64Counter)
//   - BadOtelInt64UpDownCounter (meter.Int64UpDownCounter) — defensive ban
//   - BadOtelInt64Gauge (meter.Int64Gauge) — defensive ban
//   - BadOtelInt64Histogram (meter.Int64Histogram) — defensive ban
//
// The RED check asserts exactly 19 diagnostics; the GREEN check asserts 0
// production diagnostics.
func TestGaugeVecFunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// RED check: fixture must produce exactly 19 violations.
	// The fixture rule does NOT exclude the fixture package itself — only the
	// adapter allowlist exclusions apply. This is what allows the RED check to
	// detect the violations in the fixture.
	redDiags := RunTypedFixture(
		t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/metricsgaugevecfixture/..."},
		gaugeVecFunnelRuleRaw,
	)
	for _, d := range redDiags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	require.Len(t, redDiags, 19,
		"RED fixture must trigger METRICS-GAUGEVEC-FUNNEL-01 for all nineteen bad calls "+
			"(BadPromGaugeVec + BadPromCounter + BadPromCounterVec + BadPromHistogramVec + "+
			"BadPromGauge + BadPromGaugeFunc + BadPromHistogram + BadPromSummary + "+
			"BadPromSummaryVec + BadPromCounterFunc + BadPromUntypedFunc + "+
			"BadOtelUpDownCounter + BadOtelFloat64Gauge + BadOtelFloat64Counter + "+
			"BadOtelFloat64Histogram + BadOtelInt64Counter + "+
			"BadOtelInt64UpDownCounter + BadOtelInt64Gauge + BadOtelInt64Histogram); got %d diagnostics",
		len(redDiags))

	// GREEN check: production code must produce zero violations.
	// gaugeVecFunnelRule (with full exclusion set) is used for production.
	prodDiags := RunTypedProduction(t, TypedOpts{}, gaugeVecFunnelRule)
	for _, d := range prodDiags {
		t.Errorf("METRICS-GAUGEVEC-FUNNEL-01 %s:%d: %s", d.Rel, d.Line, d.Message)
	}
	assert.Empty(t, prodDiags,
		"METRICS-GAUGEVEC-FUNNEL-01: production code must not call prom.New* or "+
			"otelmetric.Meter.{Float64,Int64}* directly; route through "+
			"adapters/prometheus/internal/promwrap or adapters/otel/internal/otelwrap")
}

// TestGaugeVecFunnel_BansFloat64Gauge pins the PR #593 review fix-up Fix 4
// invariant: the OTel ban set must cover Float64Gauge (the current adapter
// primitive since PR #625), not just Float64UpDownCounter (the historical
// leak surface). The RED fixture's BadOtelFloat64Gauge call must produce a
// diagnostic naming Float64Gauge.
func TestGaugeVecFunnel_BansFloat64Gauge(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	redDiags := RunTypedFixture(
		t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/metricsgaugevecfixture/..."},
		gaugeVecFunnelRuleRaw,
	)

	var float64GaugeHits int
	for _, d := range redDiags {
		if strings.Contains(d.Message, "Float64Gauge") {
			float64GaugeHits++
		}
	}
	assert.Equal(t, 1, float64GaugeHits,
		"METRICS-GAUGEVEC-FUNNEL-01 must flag the BadOtelFloat64Gauge fixture call "+
			"so the funnel covers the current adapter primitive; got %d Float64Gauge hits",
		float64GaugeHits)
}

// TestGaugeVecFunnel_SelfCheck verifies the blind-spot reverse self-checks for
// METRICS-GAUGEVEC-FUNNEL-01. These asserts confirm that the AST forms listed
// as blind spots (BS-1, BS-3) do not appear in production code, so the rule's
// Hard grade is not silently undermined.
func TestGaugeVecFunnel_SelfCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// BS-1 reverse self-check: no reflect.ValueOf call in production references
	// the banned symbol names as string literals.
	bs1Diags := RunTypedProduction(t, TypedOpts{}, gaugeVecBS1ReflectCheck)
	assert.Empty(t, bs1Diags,
		"METRICS-GAUGEVEC-FUNNEL-01 BS-1: found reflect.ValueOf with banned symbol name string; "+
			"production code must not construct metrics via reflection")

	// BS-3 reverse self-check: no function-value indirection for the banned symbols.
	bs3Diags := RunTypedProduction(t, TypedOpts{}, gaugeVecBS3FuncValueCheck)
	assert.Empty(t, bs3Diags,
		"METRICS-GAUGEVEC-FUNNEL-01 BS-3: found function-value indirection for banned symbol; "+
			"production code must not store prom.New* or OTel Meter methods in a variable")
}

// TestMetricsFunnel_SymbolSentinel pins the exported function set of the two
// wrap packages and the outer-ring funnel surface of adapters/prometheus.
// Any new export, removal, or rename in promwrap / otelwrap must come with
// an explicit sentinel update — AI co-authors cannot silently expand the
// funnel surface (per AI-rebust Hard funnel principle).
//
// INVARIANT: METRICS-GAUGEVEC-UPSTREAM-HARD-01
//
// Also enforces METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 outer-ring surface:
// any new New*/Register* export in adapters/prometheus must be explicitly
// acknowledged in adapterPromAllowedNewRegisterExports in the same PR.
func TestMetricsFunnel_SymbolSentinel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based sentinel in -short mode")
	}

	type wrapSpec struct {
		importPath string
		expected   []string
	}
	cases := []wrapSpec{
		{
			importPath: "github.com/ghbvf/gocell/adapters/prometheus/internal/promwrap",
			expected:   []string{"NewCounter", "NewCounterVec", "NewGauge", "NewGaugeFunc", "NewGaugeVec", "NewHistogramVec"},
		},
		{
			importPath: "github.com/ghbvf/gocell/adapters/otel/internal/otelwrap",
			expected:   []string{"Float64Counter", "Float64Gauge", "Float64Histogram"},
		},
	}

	// Collect the exported function names from each wrap package by scanning
	// the module for imports of those packages and inspecting their scope.
	// We use RunTyped to reuse the SharedResolver cache; the scan func
	// accumulates exported function names from p.Pkg.Imports().
	observed := make(map[string]map[string]struct{}) // importPath → set of func names
	for _, c := range cases {
		observed[c.importPath] = make(map[string]struct{})
	}

	scan := func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		for _, imp := range p.Pkg.Imports() {
			set, ok := observed[imp.Path()]
			if !ok {
				continue
			}
			scope := imp.Scope()
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if _, isFunc := obj.(*types.Func); !isFunc {
					continue
				}
				if !obj.Exported() {
					continue
				}
				set[name] = struct{}{}
			}
		}
		return nil
	}

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./..."}, scan)

	for _, c := range cases {
		set := observed[c.importPath]
		// Assert all expected names are present.
		for _, name := range c.expected {
			_, ok := set[name]
			assert.True(t, ok,
				"METRICS-GAUGEVEC-UPSTREAM-HARD-01 sentinel: expected export %s.%s not found — "+
					"function removed or renamed? Update expected list in TestMetricsFunnel_SymbolSentinel "+
					"AND verify the ban predicate in gaugeVecFunnelRule still covers the replacement.",
				c.importPath, name)
		}
		// Fail on any observed exports not in the expected list — new exports must
		// be explicitly acknowledged by updating this sentinel test.
		for name := range set {
			found := false
			for _, e := range c.expected {
				if e == name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("METRICS-GAUGEVEC-UPSTREAM-HARD-01 sentinel: %s has new export %s not in expected list — "+
					"add it to the expected slice in this test or remove it from the wrap package "+
					"(AI co-authors cannot silently expand the funnel surface)",
					c.importPath, name)
			}
		}
	}

	// Outer ring sentinel: adapters/prometheus must not gain unexpected New*/Register*
	// prefix exports. Factory exports (NewMetricProvider, NewHookObserver) are
	// intentionally excluded — we only lock the five funnel-shape symbols.
	// Any new New*/Register* export not in adapterPromAllowedNewRegisterExports
	// must be explicitly acknowledged in the same PR.
	outerRingObserved := make(map[string]struct{})
	outerScan := func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		for _, imp := range p.Pkg.Imports() {
			if imp.Path() != adapterPromPkg {
				continue
			}
			scope := imp.Scope()
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if _, isFunc := obj.(*types.Func); !isFunc {
					continue
				}
				if !obj.Exported() {
					continue
				}
				if strings.HasPrefix(name, "New") || strings.HasPrefix(name, "Register") {
					outerRingObserved[name] = struct{}{}
				}
			}
		}
		return nil
	}
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./..."}, outerScan)

	for name := range outerRingObserved {
		if _, ok := adapterPromAllowedNewRegisterExports[name]; !ok {
			t.Errorf("METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 sentinel: %s has unexpected "+
				"New*/Register* export %s not in adapterPromAllowedNewRegisterExports — "+
				"add it to the allowlist map (and extend adapterPromCallerAllowlist) in the "+
				"same PR, or remove it from adapters/prometheus",
				adapterPromPkg, name)
		}
	}
	// Also assert all expected symbols are still present.
	for name := range adapterPromAllowedNewRegisterExports {
		if _, ok := outerRingObserved[name]; !ok {
			t.Errorf("METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 sentinel: expected export %s.%s "+
				"not found — function removed or renamed? Update adapterPromAllowedNewRegisterExports "+
				"AND adapterPromCallerAllowlist in the same PR",
				adapterPromPkg, name)
		}
	}
}

// adapterPromPkg is the import path of the public adapter passthrough package
// whose New*/Register* constructors are locked by
// METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01.
const adapterPromPkg = "github.com/ghbvf/gocell/adapters/prometheus"

// adapterPromCallerAllowlist maps each public funnel symbol to its allowed
// caller files (relative to module root). Any callee match + caller-rel not
// in the allowlist produces a diagnostic.
//
// Adding a new caller must be done in the same PR as the allowlist update,
// preventing AI co-authors from silently expanding the funnel surface.
//
// Adding a new public symbol to adapters/prometheus is locked by
// TestMetricsFunnel_SymbolSentinel (extended to cover this package).
var adapterPromCallerAllowlist = map[string]map[string]struct{}{
	"RegisterOrReuseCounter": {"cmd/corebundle/config_module.go": {}},
	"NewCounter":             {"adapters/vault/transit_provider.go": {}},
	// NewCounterVec is a labeled-metric carve-out pending removal (issue #885):
	// vault's loginOutcome migrates to metrics.Provider.CounterVec, after which
	// this entry and the public NewCounterVec are deleted. Do NOT add callers.
	"NewCounterVec": {"adapters/vault/transit_provider.go": {}},
	"NewGauge":      {"adapters/vault/transit_provider.go": {}},
	"NewGaugeFunc":  {"adapters/vault/transit_provider.go": {}},
}

// adapterPromAllowedNewRegisterExports is the complete set of New*/Register*
// prefix exports expected in adapters/prometheus. Used by
// TestMetricsFunnel_SymbolSentinel to detect unexpected new funnel-shape
// symbols. Any new New*/Register* export must be added here explicitly.
//
// Two categories:
//   - Funnel symbols (also in adapterPromCallerAllowlist): the five passthrough
//     wrappers for adapter-external callers blocked by Go internal/ closure.
//   - Factory exports (NewMetricProvider, NewHookObserver): structural factory
//     constructors, NOT instrument construction funnels; they do not need
//     caller allowlist entries because they are not instrument wrapping paths.
var adapterPromAllowedNewRegisterExports = map[string]struct{}{
	// Funnel passthrough wrappers — also locked by adapterPromCallerAllowlist.
	"RegisterOrReuseCounter": {},
	"NewCounter":             {},
	"NewCounterVec":          {},
	"NewGauge":               {},
	"NewGaugeFunc":           {},
	// Factory exports — structural constructors, not instrument funnels.
	"NewMetricProvider": {},
	"NewHookObserver":   {},
}

// TestAdapterPromCallerAllowlist enforces METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01.
//
// The five public funnel functions in adapters/prometheus exist for adapter-
// external callers (adapters/vault, cmd/corebundle) that cannot reach the
// internal/promwrap subtree due to Go internal/ closure. Any new caller must
// extend adapterPromCallerAllowlist in the same PR — preventing silent funnel
// expansion.
//
// # Funnel grade: Medium upstream + Hard downstream
//
// Downstream (Hard): callee resolved via *types.Info to the five symbols;
// form-uniqueness via callee-name set lookup.
//
// Upstream (Medium): caller is checked by file path against a hand-maintained
// allowlist. Go has no friend-package mechanism and functions cannot be
// sealed, so this is the highest tier reachable for function-level funnels
// in the Go type system (parallels panicregister.Approved archtest-bound
// ceiling). Long-term Hard upgrade path tracked in issue #885 — vault &
// cmd/corebundle migrate to kernel/observability/metrics.Provider so the
// outer ring (and these five functions) disappear entirely.
//
// # Blind spots & reverse self-checks
//
// adapterPromCallerAllowlistRule only inspects direct CallExpr.Fun positions,
// so a non-call *value reference* to a funnel symbol would route around the
// caller allowlist. Both documented bypass forms are SelectorExpr nodes that
// are NOT the Fun of a CallExpr, so adapterPromValueRefCheck (run below) closes
// both in one scan:
//
//   - BS-A1 reflect-value indirection: reflect.ValueOf(promadapter.NewXxx) —
//     the symbol SelectorExpr sits in argument position, never called directly.
//   - BS-A2 function-value capture: var fn = promadapter.NewCounter; fn(...) —
//     the symbol SelectorExpr sits in a var/assign initializer.
//
// String-name reflection (reflect.ValueOf("NewCounter")) is intentionally not
// a separate check: a package-level func cannot be reflectively obtained from a
// name string without first taking its value via a SelectorExpr, which BS-A1
// already catches.
func TestAdapterPromCallerAllowlist(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := RunTypedProduction(t, TypedOpts{}, adapterPromCallerAllowlistRule)
	for _, d := range diags {
		t.Errorf("METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 %s:%d: %s", d.Rel, d.Line, d.Message)
	}

	// BS-A1/BS-A2 reverse self-check: no production file outside
	// adapters/prometheus references a funnel symbol as a value (function-value
	// capture or reflect-value indirection), which would bypass the direct-call
	// allowlist above.
	valueRefDiags := RunTypedProduction(t, TypedOpts{}, adapterPromValueRefCheck)
	assert.Empty(t, valueRefDiags,
		"METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 BS-A1/BS-A2: found a funnel symbol used as a "+
			"value (not a direct call); this bypasses the caller allowlist — route through "+
			"kernel/observability/metrics.Provider")
}

// adapterPromCallerAllowlistRule is the rule function for
// METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01. It checks that every callsite of
// the five public adapters/prometheus funnel symbols is in the caller allowlist.
func adapterPromCallerAllowlistRule(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	// Exclude the adapters/prometheus package itself (impl + own tests).
	if p.Pkg != nil && p.Pkg.Path() == adapterPromPkg {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasPrefix(rel, "adapters/prometheus/") {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := resolveCalleePackageRef(call.Fun, p.TypesInfo)
			if !ok || pkgPath != adapterPromPkg {
				return
			}
			allowed, inAllowlist := adapterPromCallerAllowlist[name]
			if !inAllowlist {
				return
			}
			if _, ok := allowed[rel]; ok {
				return
			}
			line := p.Fset.Position(call.Pos()).Line
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01: %s calls %s.%s but is not "+
						"in adapterPromCallerAllowlist for that symbol. Either route through "+
						"kernel/observability/metrics.Provider OR add this file to the "+
						"allowlist in the same PR (and explain why the new caller is justified)",
					rel, adapterPromPkg, name,
				),
			})
		})
	}
	return diags
}

// isAdapterPromFunnelSymbol reports whether name is one of the public
// adapters/prometheus funnel symbols governed by the caller allowlist. The
// allowlist map is the single source of truth for the symbol set, shared by
// the direct-call rule and the BS-A1/BS-A2 value-reference self-check.
func isAdapterPromFunnelSymbol(name string) bool {
	_, ok := adapterPromCallerAllowlist[name]
	return ok
}

// adapterPromValueRefCheck implements the BS-A1/BS-A2 reverse self-check for
// METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01: no production code outside
// adapters/prometheus references a funnel symbol as a *value* — a SelectorExpr
// resolving to adapterPromPkg + a funnel symbol that is NOT the Fun of a
// CallExpr. This closes the bypass the direct-call allowlist cannot see:
// `var fn = promadapter.NewCounter; fn(opts)` (the capture point is the
// SelectorExpr in the var initializer) and `reflect.ValueOf(promadapter.NewXxx)`
// (the SelectorExpr in argument position). Value indirection of a funnel symbol
// is banned everywhere outside adapters/prometheus — there is no legitimate need
// for it, so unlike the direct-call rule there is no per-file allowlist.
//
// Mirrors the inner-ring scanFuncValueIndirections (BS-3) shape.
func adapterPromValueRefCheck(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	if p.Pkg != nil && p.Pkg.Path() == adapterPromPkg {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasPrefix(rel, "adapters/prometheus/") {
			continue
		}
		callFunPositions := make(map[token.Pos]bool)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			callFunPositions[call.Fun.Pos()] = true
		})
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			if callFunPositions[sel.Pos()] {
				return
			}
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, sel)
			if !ok || pkgPath != adapterPromPkg || !isAdapterPromFunnelSymbol(name) {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf(
					"METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 BS-A1/BS-A2: %s.%s used as a value "+
						"(not a direct call), bypassing the caller allowlist; route through "+
						"kernel/observability/metrics.Provider", adapterPromPkg, name,
				),
			})
		})
	}
	return diags
}

// bannedPromPkg is the import path of the prometheus package whose New*
// constructors are banned outside adapters/prometheus/internal/promwrap/.
const bannedPromPkg = "github.com/prometheus/client_golang/prometheus"

// isBannedPromFunc reports whether name is a prometheus package-level
// constructor banned by METRICS-GAUGEVEC-FUNNEL-01. The predicate matches
// ^New(Counter|Gauge|Histogram|Summary|Untyped)(Vec|Func)?$ — covering the full
// synchronous instrument construction surface:
//
//   - NewCounter, NewCounterVec, NewCounterFunc
//   - NewGauge, NewGaugeVec, NewGaugeFunc
//   - NewHistogram, NewHistogramVec
//   - NewSummary, NewSummaryVec
//   - NewUntypedFunc
//
// All variants are banned; production code must route through
// adapters/prometheus/internal/promwrap (via the public
// adapters/prometheus.New* wrappers for packages outside the subtree).
//
// Drift in the promwrap export set is caught by TestMetricsFunnel_SymbolSentinel.
func isBannedPromFunc(name string) bool {
	// Must start with "New".
	if !strings.HasPrefix(name, "New") {
		return false
	}
	// Match the instrument type root after "New".
	rest := name[len("New"):]
	var root string
	for _, r := range []string{"Counter", "Gauge", "Histogram", "Summary", "Untyped"} {
		if strings.HasPrefix(rest, r) {
			root = r
			break
		}
	}
	if root == "" {
		return false
	}
	// After the root, only "", "Vec", or "Func" are allowed.
	suffix := rest[len(root):]
	return suffix == "" || suffix == "Vec" || suffix == "Func"
}

// bannedOtelPkg is the import path of the OTel metric package whose Meter methods
// are banned outside adapters/otel/internal/otelwrap/.
const bannedOtelPkg = "go.opentelemetry.io/otel/metric"

// bannedOtelMethods is the set of OTel Meter synchronous instrument constructor
// method names banned by METRICS-GAUGEVEC-FUNNEL-01 in production code outside
// adapters/otel/internal/otelwrap/. All synchronous instrument creation must
// route through the otelwrap funnel.
//
// Observable (async) variants (Int64ObservableUpDownCounter, etc.) are NOT
// included: they use a callback registration pattern that cannot be cleanly
// funneled through a synchronous single-call wrapper, and their callsites in
// adapters/otel/*.go are legitimate implementation sites.
//
// Both the historical leak surfaces (Float64UpDownCounter pre-PR #625,
// Float64Gauge PR #625+) and all other synchronous Float64*/Int64* constructors
// are covered. The Int64 variants are a defensive ban: no production callsite
// exists yet, but banning them now prevents future bypass paths.
var bannedOtelMethods = map[string]struct{}{
	"Float64UpDownCounter": {}, // historical leak surface (pre-PR #625)
	"Float64Gauge":         {}, // current adapter primitive (PR #625+)
	"Float64Counter":       {}, // B2 expansion — synchronous counter
	"Float64Histogram":     {}, // B2 expansion — synchronous histogram
	"Int64Counter":         {}, // B2 defensive ban — no production callsite yet
	"Int64UpDownCounter":   {}, // B2 defensive ban
	"Int64Gauge":           {}, // B2 defensive ban
	"Int64Histogram":       {}, // B2 defensive ban
}

// gaugeVecFunnelRuleRaw is the core rule for METRICS-GAUGEVEC-FUNNEL-01 without
// fixture-package exclusion. It is used by the RED fixture check so that the
// fixture package's violations are detected.
//
// Scope exclusions applied here:
//   - adapters/prometheus/internal/promwrap/ — the sanctioned Prometheus implementation site
//   - adapters/otel/internal/otelwrap/ — the sanctioned OTel implementation site
func gaugeVecFunnelRuleRaw(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	if p.Pkg != nil && isGaugeVecWrapPkg(p.Pkg.Path()) {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if isGaugeVecWrapRel(rel) {
			continue
		}
		diags = append(diags, scanGaugeVecCallsInFile(p.Fset, file, rel, p.TypesInfo)...)
	}
	return diags
}

// gaugeVecFunnelRule is the Rule function for METRICS-GAUGEVEC-FUNNEL-01 used
// for production scans. It extends gaugeVecFunnelRuleRaw by also excluding the
// RED fixture package (which is not production code).
//
// Scope exclusions (applied by checking the package import path via p.Pkg):
//   - adapters/prometheus/internal/promwrap/ — the sanctioned implementation site
//   - adapters/otel/internal/otelwrap/ — the sanctioned implementation site
//   - tools/archtest/internal/metricsgaugevecfixture/ — the RED fixture itself
func gaugeVecFunnelRule(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	// Apply scope exclusions at the package level.
	if p.Pkg != nil && isGaugeVecExcludedPkg(p.Pkg.Path()) {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		// Also exclude by file path in case Pkg is not populated.
		if isGaugeVecExcludedRel(rel) {
			continue
		}
		diags = append(diags, scanGaugeVecCallsInFile(p.Fset, file, rel, p.TypesInfo)...)
	}
	return diags
}

// promwrapPkg and otelwrapPkg are the exact import paths for the two internal
// wrap packages that are permitted to call the banned constructors directly.
// Exact equality is used instead of strings.Contains to prevent sibling
// packages with similar names (e.g. adapters/prometheus/internal/promwrap_bypass)
// from being accidentally allowed.
const (
	promwrapPkg = "github.com/ghbvf/gocell/adapters/prometheus/internal/promwrap"
	otelwrapPkg = "github.com/ghbvf/gocell/adapters/otel/internal/otelwrap"
)

// isGaugeVecWrapPkg returns true for the exact internal wrap packages that are
// permitted to call the banned constructors directly. Narrowed from the former
// adapter-directory allowlist to the internal/ subpackages only; Go's internal/
// closure provides compile-time enforcement of the package boundary.
//
// Uses exact import-path equality (not strings.Contains) to prevent sibling
// packages from being accidentally allowed.
func isGaugeVecWrapPkg(pkgPath string) bool {
	return pkgPath == promwrapPkg || pkgPath == otelwrapPkg
}

// isGaugeVecWrapRel returns true for files within the exact internal wrap
// package paths that are permitted to call the banned constructors directly.
func isGaugeVecWrapRel(rel string) bool {
	return strings.HasPrefix(rel, "adapters/prometheus/internal/promwrap/") ||
		strings.HasPrefix(rel, "adapters/otel/internal/otelwrap/")
}

// isGaugeVecExcludedPkg returns true for packages that are permitted to call
// the banned constructors directly (the wrap packages and the RED fixture).
func isGaugeVecExcludedPkg(pkgPath string) bool {
	return isGaugeVecWrapPkg(pkgPath) ||
		strings.Contains(pkgPath, "metricsgaugevecfixture")
}

// isGaugeVecExcludedRel returns true for file paths that are permitted to call
// the banned constructors (wrap packages and fixture).
func isGaugeVecExcludedRel(rel string) bool {
	return isGaugeVecWrapRel(rel) ||
		strings.Contains(rel, "metricsgaugevecfixture")
}

// scanGaugeVecCallsInFile walks call expressions in file and returns diagnostics
// for any that resolve to a banned constructor.
func scanGaugeVecCallsInFile(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if d := checkPromBannedConstructor(fset, call, rel, info); d != nil {
			diags = append(diags, *d)
			return
		}
		if d := checkOtelBannedGaugeMethod(fset, call, rel, info); d != nil {
			diags = append(diags, *d)
		}
	})
	return diags
}

// checkPromBannedConstructor returns a Diagnostic when call resolves to any
// banned prometheus constructor matched by isBannedPromFunc, or nil if not.
func checkPromBannedConstructor(fset *token.FileSet, call *ast.CallExpr, rel string, info *types.Info) *Diagnostic {
	pkgPath, name, ok := resolveCalleePackageRef(call.Fun, info)
	if !ok {
		return nil
	}
	if pkgPath != bannedPromPkg {
		return nil
	}
	if !isBannedPromFunc(name) {
		return nil
	}
	line := fset.Position(call.Pos()).Line
	return &Diagnostic{
		Rel:  rel,
		Line: line,
		Message: fmt.Sprintf(
			"METRICS-GAUGEVEC-FUNNEL-01: %s calls forbidden %s.%s; "+
				"route through kernel/observability/metrics.Provider for labeled metrics, "+
				"OR (adapter-external bare/Func variants only) use adapters/prometheus."+
				"{NewCounter,NewGauge,NewGaugeFunc} — Go internal/ closure "+
				"blocks direct adapters/prometheus/internal/promwrap imports outside the "+
				"prometheus adapter subtree (NewCounterVec is a vault-only carve-out "+
				"pending removal, not a remedy here — see issue #885)",
			rel, bannedPromPkg, name,
		),
	}
}

// checkOtelBannedGaugeMethod returns a Diagnostic when call resolves to any
// Meter method in bannedOtelMethods, or nil if it does not.
func checkOtelBannedGaugeMethod(fset *token.FileSet, call *ast.CallExpr, rel string, info *types.Info) *Diagnostic {
	fn, ok := ResolveMethodCall(info, callFunAsSel(call))
	if !ok || fn == nil {
		return nil
	}
	if _, banned := bannedOtelMethods[fn.Name()]; !banned {
		return nil
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != bannedOtelPkg {
		return nil
	}
	line := fset.Position(call.Pos()).Line
	return &Diagnostic{
		Rel:  rel,
		Line: line,
		Message: fmt.Sprintf(
			"METRICS-GAUGEVEC-FUNNEL-01: %s calls forbidden %s.Meter.%s; "+
				"route through kernel/observability/metrics.Provider (preferred) or "+
				"adapters/otel/internal/otelwrap (OTel adapter subtree only)",
			rel, bannedOtelPkg, fn.Name(),
		),
	}
}

// resolveCalleePackageRef is a thin wrapper for package-level function calls
// (SelectorExpr like prom.NewGaugeVec). It returns (pkgPath, name, true) when
// fun is a SelectorExpr whose selector resolves to a *types.Func in a named
// package via info.Uses.
func resolveCalleePackageRef(fun ast.Expr, info *types.Info) (pkgPath, name string, ok bool) {
	return ResolvePackageRef(info, fun)
}

// callFunAsSel returns the SelectorExpr of a call's Fun if it is one, else nil.
// Used to feed method calls to ResolveMethodCall.
func callFunAsSel(call *ast.CallExpr) *ast.SelectorExpr {
	sel, _ := call.Fun.(*ast.SelectorExpr)
	return sel
}

// gaugeVecBS1ReflectCheck implements the BS-1 reverse self-check: no production
// code calls reflect.ValueOf with a string argument that contains a banned symbol
// name. This is a conservative heuristic (string literal only); it guards the most
// obvious bypass pattern without requiring full reflect type tracing.
func gaugeVecBS1ReflectCheck(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if isGaugeVecExcludedRel(rel) {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "ValueOf" {
				return
			}
			xIdent, ok := sel.X.(*ast.Ident)
			if !ok || xIdent.Name != "reflect" {
				return
			}
			// Check if any string arg contains a banned symbol name (prom funcs or
			// any of the OTel ban-set method names). For prom, use the prefix
			// predicate (isBannedPromFunc) on each whitespace-delimited token in
			// the string; for OTel, check the banned method map.
			for _, arg := range call.Args {
				s, ok := EvaluateConstString(p.TypesInfo, arg)
				if !ok {
					continue
				}
				match := false
				for _, tok := range strings.Fields(s) {
					if isBannedPromFunc(tok) {
						match = true
						break
					}
				}
				if !match {
					for methodName := range bannedOtelMethods {
						if strings.Contains(s, methodName) {
							match = true
							break
						}
					}
				}
				if !match {
					continue
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Message: fmt.Sprintf(
						"METRICS-GAUGEVEC-FUNNEL-01 BS-1: reflect.ValueOf with banned symbol name %q", s,
					),
				})
			}
		})
	}
	return diags
}

// gaugeVecBS3FuncValueCheck implements the BS-3 reverse self-check: no production
// code stores any banned prom.New* function or OTel Meter instrument method
// in a function value variable. We detect this by scanning ValueSpec and
// AssignStmt nodes where the RHS is a SelectorExpr (not a CallExpr) that
// resolves to a banned symbol.
func gaugeVecBS3FuncValueCheck(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if isGaugeVecExcludedRel(rel) {
			continue
		}
		diags = append(diags, scanFuncValueIndirections(p.Fset, file, rel, p.TypesInfo)...)
	}
	return diags
}

// scanFuncValueIndirections walks the file looking for any SelectorExpr that
// resolves to a banned symbol but is NOT the Fun of a CallExpr (i.e. used as a
// function value, not a direct call). These represent BS-3 method-value
// indirection patterns.
//
// Two forms are checked:
//  1. Prom function-value: `var fn = prom.NewGaugeVec` — SelectorExpr resolves
//     via ResolvePackageRef to bannedPromPkg + name in bannedPromFuncs.
//  2. OTel method-value capture: `var fn = meter.Float64Gauge` — SelectorExpr
//     whose Sel matches any entry in bannedOtelMethods AND whose receiver X
//     resolves via *types.Info to otelmetric.Meter.
func scanFuncValueIndirections(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	// Collect all CallExpr.Fun positions so we can exclude them below.
	callFunPositions := make(map[token.Pos]bool)
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		callFunPositions[call.Fun.Pos()] = true
	})

	var diags []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		// Skip if this SelectorExpr is itself used as a call target.
		if callFunPositions[sel.Pos()] {
			return
		}

		// Check 1: Prom function-value indirection (prom.New* used as value).
		pkgPath, name, ok := ResolvePackageRef(info, sel)
		if ok && pkgPath == bannedPromPkg && isBannedPromFunc(name) {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf(
					"METRICS-GAUGEVEC-FUNNEL-01 BS-3: %s.%s used as function value (not called directly); "+
						"route through adapters/prometheus/internal/promwrap", bannedPromPkg, name,
				),
			})
			return
		}

		// Check 2: OTel method-value capture (any banned Meter method used as value).
		if sel.Sel == nil {
			return
		}
		if _, banned := bannedOtelMethods[sel.Sel.Name]; !banned {
			return
		}
		fn, resolved := ResolveMethodCall(info, sel)
		if resolved && fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == bannedOtelPkg {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf(
					"METRICS-GAUGEVEC-FUNNEL-01 BS-3: %s.Meter.%s used as method value (not called directly); "+
						"route through adapters/otel/internal/otelwrap", bannedOtelPkg, fn.Name(),
				),
			})
		}
	})
	return diags
}
