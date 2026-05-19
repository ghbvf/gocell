// invariants:
//   - INVARIANT: OBS-01
//   - INVARIANT: METRICS-GAUGEVEC-FUNNEL-01

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
// Production code outside adapters/prometheus/ and adapters/otel/ must NEVER
// call prom.NewGaugeVec or otelmetric.Meter.Float64UpDownCounter directly.
// All GaugeVec creation must route through
// kernel/observability/metrics.Provider.GaugeVec.
//
// # Funnel grade: Medium upstream + Hard downstream
//
// Downstream (Hard): callee resolved via *types.Info to the two banned
// symbols; form-uniqueness gives Hard — there is no "looks-like-but-isn't"
// gray zone for either callee.
//
// Upstream (Medium): prom.NewGaugeVec is a public exported function and the
// Go type system cannot prevent business packages from importing prometheus
// directly and calling it. The archtest scope filter (excluding adapters/prometheus/
// and adapters/otel/) is the only caller-allowlist mechanism today.
//
// Backlog upgrade path: METRICS-GAUGEVEC-UPSTREAM-HARD-01
// (docs/backlog/cap-13-observability.md) — wrap prom.NewGaugeVec (and Counter /
// Histogram peers) inside adapters/prometheus/internal/promwrap/ (a Go-internal
// package). The internal mechanism prevents external imports at compile time,
// making upstream Hard. Same approach for the OTel side.
//
// # Blind spots (production AST forms NOT matched; asserted absent below)
//
// BS-1 Functions calling NewGaugeVec via reflection (reflect.ValueOf):
// kept Medium — production code does not use reflect for metrics construction.
// Reverse self-check: TestGaugeVecFunnel_SelfCheck BS-1 asserts no
// reflect.ValueOf call site references the banned symbol names in any
// production file.
//
// BS-2 Cross-package value forwarding of *prom.Registry then calling
// RegisterOrReuse internally: out of scope — callers forwarding a Registry do
// not themselves call NewGaugeVec; legitimate provider-internal usage.
//
// BS-3 Method-value indirection: `var fn = prom.NewGaugeVec; fn(...)` — the
// function-value form has Fun as *ast.Ident (variable) not *ast.SelectorExpr,
// so *types.Info.Uses resolve to a *types.Var, not *types.Func. Accepted:
// production code has no such pattern. For the OTel side this manifests as
// `var fn = meter.Float64UpDownCounter` (method-value capture): the
// SelectorExpr exists but is not the Fun of a CallExpr, so it is also not
// caught by the direct-call scan. Reverse self-check: BS-3 check below covers
// both the Prom function-value form and the OTel method-value capture form.
//
// # RED fixture
//
// tools/archtest/internal/metricsgaugevecfixture/fixture.go provides two
// intentional violations: BadPromGaugeVec (prom.NewGaugeVec) and
// BadOtelUpDownCounter (meter.Float64UpDownCounter). The RED check asserts
// exactly 2 diagnostics; the GREEN check asserts 0 production diagnostics.
func TestGaugeVecFunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// RED check: fixture must produce exactly 2 violations.
	// The fixture rule does NOT exclude the fixture package itself — only the
	// adapter allowlist exclusions apply. This is what allows the RED check to
	// detect the violations in the fixture.
	redDiags := RunTypedFixture(t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/metricsgaugevecfixture/..."},
		gaugeVecFunnelRuleRaw,
	)
	for _, d := range redDiags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	require.Len(t, redDiags, 2,
		"RED fixture must trigger METRICS-GAUGEVEC-FUNNEL-01 for both bad calls "+
			"(BadPromGaugeVec + BadOtelUpDownCounter); got %d diagnostics", len(redDiags))

	// GREEN check: production code must produce zero violations.
	// gaugeVecFunnelRule (with full exclusion set) is used for production.
	prodDiags := RunTypedProduction(t, TypedOpts{}, gaugeVecFunnelRule)
	for _, d := range prodDiags {
		t.Errorf("METRICS-GAUGEVEC-FUNNEL-01 %s:%d: %s", d.Rel, d.Line, d.Message)
	}
	assert.Empty(t, prodDiags,
		"METRICS-GAUGEVEC-FUNNEL-01: production code must not call prom.NewGaugeVec or "+
			"otelmetric.Meter.Float64UpDownCounter directly; route through "+
			"kernel/observability/metrics.Provider.GaugeVec")
}

// TestGaugeVecFunnel_SelfCheck verifies the blind-spot reverse self-checks for
// METRICS-GAUGEVEC-FUNNEL-01. These asserts confirm that the AST forms listed
// as blind spots (BS-1, BS-3) do not appear in production code, so the rule's
// Medium grade is not silently undermined.
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
			"production code must not store NewGaugeVec/Float64UpDownCounter in a variable")
}

// bannedPromPkg is the import path of the prometheus package whose NewGaugeVec
// is banned outside the adapter.
const bannedPromPkg = "github.com/prometheus/client_golang/prometheus"

// bannedPromFunc is the function name banned by METRICS-GAUGEVEC-FUNNEL-01
// in the prometheus package.
const bannedPromFunc = "NewGaugeVec"

// bannedOtelPkg is the import path of the OTel metric package whose Meter methods
// are banned outside the adapter.
const bannedOtelPkg = "go.opentelemetry.io/otel/metric"

// bannedOtelMethod is the Meter method name banned by METRICS-GAUGEVEC-FUNNEL-01
// in production code outside adapters/.
//
// Float64UpDownCounter is BANNED outside adapter because gauge semantics
// must go through metrics.Provider.GaugeVec funnel, not direct OTel SDK
// usage. (Note: the adapter itself uses Float64Gauge as of PR #625, but
// the funnel ban target stays Float64UpDownCounter because it is the
// historical leak surface; adapter is allow-listed via isGaugeVecAdapterPkg.)
//
// Residual risk — adapter self-misuse: adapters/otel/ is excluded from this
// ban (it is the sanctioned implementation site). If adapters/otel/ were to
// revert from Float64Gauge back to Float64UpDownCounter this archtest would NOT
// catch it — the exclusion covers all methods in that package. This is an
// accepted risk (documented in ADR 202605191500 §威胁矩阵); adapters/otel/ is
// a single, easily audited file and any regression would surface via the OTel
// adapter unit tests (sync-diff correctness) rather than this archtest.
const bannedOtelMethod = "Float64UpDownCounter"

// gaugeVecFunnelRuleRaw is the core rule for METRICS-GAUGEVEC-FUNNEL-01 without
// fixture-package exclusion. It is used by the RED fixture check so that the
// fixture package's violations are detected.
//
// Scope exclusions applied here:
//   - adapters/prometheus/ — the sanctioned Prometheus implementation site
//   - adapters/otel/ — the sanctioned OTel implementation site
func gaugeVecFunnelRuleRaw(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	if p.Pkg != nil && isGaugeVecAdapterPkg(p.Pkg.Path()) {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if isGaugeVecAdapterRel(rel) {
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
//   - adapters/prometheus/ — the sanctioned implementation site
//   - adapters/otel/ — the sanctioned implementation site
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

// isGaugeVecAdapterPkg returns true for adapter packages that are permitted to
// call the banned constructors directly.
func isGaugeVecAdapterPkg(pkgPath string) bool {
	return strings.Contains(pkgPath, "adapters/prometheus") ||
		strings.Contains(pkgPath, "adapters/otel")
}

// isGaugeVecAdapterRel returns true for adapter file paths.
func isGaugeVecAdapterRel(rel string) bool {
	return strings.HasPrefix(rel, "adapters/prometheus/") ||
		strings.HasPrefix(rel, "adapters/otel/")
}

// isGaugeVecExcludedPkg returns true for packages that are permitted to call
// the banned constructors directly (the adapter implementations and the RED fixture).
func isGaugeVecExcludedPkg(pkgPath string) bool {
	return isGaugeVecAdapterPkg(pkgPath) ||
		strings.Contains(pkgPath, "metricsgaugevecfixture")
}

// isGaugeVecExcludedRel returns true for file paths that are permitted to call
// the banned constructors (adapter implementation files and fixture).
func isGaugeVecExcludedRel(rel string) bool {
	return isGaugeVecAdapterRel(rel) ||
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
		if d := checkPromNewGaugeVec(fset, call, rel, info); d != nil {
			diags = append(diags, *d)
			return
		}
		if d := checkOtelFloat64UpDownCounter(fset, call, rel, info); d != nil {
			diags = append(diags, *d)
		}
	})
	return diags
}

// checkPromNewGaugeVec returns a Diagnostic when call resolves to
// prometheus.NewGaugeVec, or nil if it does not.
func checkPromNewGaugeVec(fset *token.FileSet, call *ast.CallExpr, rel string, info *types.Info) *Diagnostic {
	pkgPath, name, ok := resolveCalleePackageRef(call.Fun, info)
	if !ok {
		return nil
	}
	if pkgPath != bannedPromPkg || name != bannedPromFunc {
		return nil
	}
	line := fset.Position(call.Pos()).Line
	return &Diagnostic{
		Rel:  rel,
		Line: line,
		Message: fmt.Sprintf(
			"METRICS-GAUGEVEC-FUNNEL-01: %s calls forbidden %s.%s; "+
				"route through kernel/observability/metrics.Provider.GaugeVec",
			rel, bannedPromPkg, bannedPromFunc),
	}
}

// checkOtelFloat64UpDownCounter returns a Diagnostic when call resolves to
// otelmetric.Meter.Float64UpDownCounter, or nil if it does not.
func checkOtelFloat64UpDownCounter(fset *token.FileSet, call *ast.CallExpr, rel string, info *types.Info) *Diagnostic {
	fn, ok := ResolveMethodCall(info, callFunAsSel(call))
	if !ok || fn == nil {
		return nil
	}
	if fn.Name() != bannedOtelMethod {
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
				"route through kernel/observability/metrics.Provider.GaugeVec",
			rel, bannedOtelPkg, bannedOtelMethod),
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
			// Check if any string arg contains a banned symbol name.
			for _, arg := range call.Args {
				if s, ok := EvaluateConstString(p.TypesInfo, arg); ok {
					if strings.Contains(s, bannedPromFunc) || strings.Contains(s, bannedOtelMethod) {
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: p.Fset.Position(call.Pos()).Line,
							Message: fmt.Sprintf(
								"METRICS-GAUGEVEC-FUNNEL-01 BS-1: reflect.ValueOf with banned symbol name %q", s),
						})
					}
				}
			}
		})
	}
	return diags
}

// gaugeVecBS3FuncValueCheck implements the BS-3 reverse self-check: no production
// code stores prom.NewGaugeVec or meter.Float64UpDownCounter in a function value
// variable. We detect this by scanning ValueSpec and AssignStmt nodes where the
// RHS is a SelectorExpr (not a CallExpr) that resolves to a banned symbol.
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
//     via ResolvePackageRef to bannedPromPkg.bannedPromFunc.
//  2. OTel method-value capture: `var fn = meter.Float64UpDownCounter` —
//     SelectorExpr whose Sel matches bannedOtelMethod AND whose receiver X
//     resolves via *types.Info to otelmetric.Meter (checked via ResolveMethodCall
//     on a synthetic call-shaped node, or by checking the selector name AND the
//     receiver type's package path). We use a conservative name-only check for
//     the method-value capture form: any non-call SelectorExpr whose Sel is
//     bannedOtelMethod is flagged. This is conservative (could match other
//     types with the same method name) but Float64UpDownCounter is
//     OTel-specific and unlikely to collide in production GoCell code.
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

		// Check 1: Prom function-value indirection (prom.NewGaugeVec used as value).
		pkgPath, name, ok := ResolvePackageRef(info, sel)
		if ok && pkgPath == bannedPromPkg && name == bannedPromFunc {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf(
					"METRICS-GAUGEVEC-FUNNEL-01 BS-3: %s.%s used as function value (not called directly); "+
						"route through metrics.Provider.GaugeVec", bannedPromPkg, bannedPromFunc),
			})
			return
		}

		// Check 2: OTel method-value capture (meter.Float64UpDownCounter used as value).
		// We use ResolveMethodCall on a synthesized selector to get the full type info.
		// If the selector name matches and the method resolves to the OTel banned pkg,
		// it is a BS-3 violation.
		if sel.Sel != nil && sel.Sel.Name == bannedOtelMethod {
			fn, resolved := ResolveMethodCall(info, sel)
			if resolved && fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == bannedOtelPkg {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: fset.Position(sel.Pos()).Line,
					Message: fmt.Sprintf(
						"METRICS-GAUGEVEC-FUNNEL-01 BS-3: %s.Meter.%s used as method value (not called directly); "+
							"route through metrics.Provider.GaugeVec", bannedOtelPkg, bannedOtelMethod),
				})
			}
		}
	})
	return diags
}
