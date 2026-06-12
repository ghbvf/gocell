//go:build archtest

// INVARIANT: METRICS-CANCEL-CTX-CONFORMANCE-01
//
// AI-robust: Medium (test-existence backstop — NOT a funnel upstream lock).
//
// This archtest is a *test-existence backstop*: it enforces that every concrete
// production kernel/observability/metrics.Provider implementation is exercised
// by the no-skip-on-cancel behavioral conformance harness
// (kernel/observability/metrics/metricstest.RunCanceledCtxConformance). It is
// the Medium membership layer that the Hard behavioral harness sits on.
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware, identifies every
//     concrete named type (exported OR unexported) that satisfies
//     kernel/observability/metrics.Provider (or *T), restricted to the
//     enrollable production layer adapters/ (see scope below).
//   - conformance 调用扫描: per *implementation*. Each RunCanceledCtxConformance
//     call's Provider argument (args[1]) is resolved via *types.Info to its
//     concrete type key, so a package with N Provider impls must
//     conformance-test each of the N — enrolling one does not cover its
//     same-package siblings.
//   - 综合: Medium is the **ceiling by construction** — Go cannot require a test
//     to exist at compile time. This is the established shape of the sibling
//     CELL-REPO-READYZ-PROBE-01. The behavioral correctness of the harness
//     itself is Hard: RunCanceledCtxConformance records via an already-canceled
//     ctx and asserts the measured values through an adapter-supplied readback,
//     so a regression such as `if ctx.Err() != nil { return }` inside an
//     adapter's Add/Observe/Set makes the readback return 0/wrong and the
//     conformance test fails — a no-op cannot satisfy it.
//
// Enforces: every concrete type under adapters/ that implements
// kernel/observability/metrics.Provider must be passed as the Provider argument
// to at least one metricstest.RunCanceledCtxConformance call in a _test.go
// file. Impls with no such call are reported as violations.
//
// Contract source: kernel/observability/metrics/metrics.go §contract (1):
// "实现禁止因 ctx 已取消/超时而跳过记录——ctx 取消不得导致数据丢失". Honored by
// adapters/otel (forwards ctx to inner.Add/Record without an Err() gate) and
// adapters/prometheus (discards ctx entirely). This archtest + the harness keep
// both honoring it.
//
// # Scope (enrollable production layer only)
//
//   - In scope: package paths under adapters/. Both production Provider impls
//     (otel.MetricProvider, prometheus.MetricProvider) live here and can hang a
//     RunCanceledCtxConformance call off a _test.go in their own package.
//
//   - Excluded: kernel/. The kernel nop Provider (kernel/observability/metrics.nop*)
//     records nothing — it has no observable failure domain and is structurally
//     un-conformance-testable (no readback can assert a value a no-op never
//     stored). This mirrors CELL-REPO-READYZ-PROBE-01's kernel exclusion: there
//     is nothing to enroll and nothing to backlog. runtime/cells/examples define
//     no metrics.Provider impl (providerCollector is a Collector, not a Provider;
//     spyProvider lives in _test.go, excluded by the production load pass).
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations: no production code uses this
//     pattern; confirmed by TestMetricsCancelCtxConformance_ReverseBlindSpot,
//     which scans for the interface name "Provider" string literal in adapters/
//     non-test files as reflect bait.
//   - B2. generated mock implementations in _test.go are excluded (Tests=false in
//     the production impl scan); a mock in a production non-test file would be
//     flagged — intentionally.
//
// ref: tools/archtest/cell_repo_readyz_probe_test.go (sibling backstop pattern)
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	metricsProviderIfaceName   = "Provider"
	cancelCtxConformanceFunc   = "RunCanceledCtxConformance"
	metricsProviderScopePrefix = "adapters/"
)

// TestMetricsCancelCtxConformance enforces METRICS-CANCEL-CTX-CONFORMANCE-01:
// every concrete type under adapters/ implementing metrics.Provider must have a
// metricstest.RunCanceledCtxConformance call in a _test.go file of its package.
func TestMetricsCancelCtxConformance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	modPath := readModulePath(t, root)

	// ─── Step 1: resolve metrics.Provider + collect adapters/ impl pkgs ───────
	// The iface and the impl types MUST come from the same packages.Load so
	// types.Implements uses pointer-identical *types.Named descriptors.
	// Production is workspace-aware: post-#1558 the Provider impls live in their
	// own satellite modules (adapters/otel, adapters/prometheus); a module-local
	// Typed(..., prodscan.Patterns) load never reaches them, collapsing implPkgs
	// to empty (false-red). Production loads the kernel iface AND every satellite
	// impl through ONE LoadProductionPackages resolver, preserving the
	// pointer-identical *types.Named universe types.Implements requires.
	var providerIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == metricsProviderIfacePkg {
				if obj := p.Pkg.Scope().Lookup(metricsProviderIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							providerIface = iface.Complete()
						}
					}
				}
				return nil
			}
			if inMetricsProviderScope(p.Pkg.Path(), modPath) {
				implPkgs = append(implPkgs, p.Pkg)
			}
			return nil
		})

	require.NotNil(t, providerIface,
		"METRICS-CANCEL-CTX-CONFORMANCE-01: failed to resolve metrics.Provider interface; "+
			"check import path %s", metricsProviderIfacePkg)

	// ─── Step 2: collect all concrete adapters/ implementations ───────────────
	implSet := make(map[string]bool) // "pkg/path.TypeName" → true
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectMetricsProviderImpls(pkg, providerIface, implSet)
		}
	}
	require.NotEmpty(t, implSet,
		"METRICS-CANCEL-CTX-CONFORMANCE-01: zero metrics.Provider implementations collected under "+
			"adapters/ — likely a type-universe regression (iface and impls must share one "+
			"packages.Load). Expect otel.MetricProvider and prometheus.MetricProvider.")

	// ─── Step 3: scan test corpus for RunCanceledCtxConformance call sites ───
	enrolledImpls := make(map[string]bool) // "pkg/path.TypeName" → true
	_ = Run(t, Production(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				if !strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				collectEnrolledMetricsProviders(f, p.TypesInfo, enrolledImpls)
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ──────────────────────────────
	diags := unenrolledMetricsProviders(implSet, enrolledImpls)
	Report(t, "METRICS-CANCEL-CTX-CONFORMANCE-01", diags)
}

// TestMetricsCancelCtxConformance_REDFixture exercises the per-impl flag logic
// (unenrolledMetricsProviders) with synthetic impl/enrollment sets — pure
// function, no packages.Load. The same-package-partial case is the regression
// guard against package-granularity enrollment.
func TestMetricsCancelCtxConformance_REDFixture(t *testing.T) {
	t.Parallel()

	const (
		otelPkg = metricsConformanceOtelPkg
		promPkg = metricsConformancePromPkg
		otelP   = otelPkg + ".MetricProvider"
		promP   = promPkg + ".MetricProvider"
	)
	implSet := map[string]bool{otelP: true, promP: true}

	flaggedKeys := func(diags []Diagnostic) map[string]bool {
		out := make(map[string]bool, len(diags))
		for _, d := range diags {
			out[d.Rel] = true
		}
		return out
	}

	t.Run("missing-enrollment", func(t *testing.T) {
		t.Parallel()
		diags := unenrolledMetricsProviders(implSet, map[string]bool{otelP: true})
		got := flaggedKeys(diags)
		assert.True(t, got[promP], "prometheus.MetricProvider must be flagged when unenrolled")
		assert.Len(t, diags, 1, "only the unenrolled impl should be flagged: %v", diags)
	})

	t.Run("all-enrolled-green", func(t *testing.T) {
		t.Parallel()
		diags := unenrolledMetricsProviders(implSet, implSet)
		assert.Empty(t, diags, "fully enrolled implSet must produce zero violations")
	})
}

// TestMetricsCancelCtxConformance_ReverseBlindSpot (blind spot B1) confirms no
// adapters/ non-test file references the interface name "Provider" as a string
// literal — which would hint at a reflect-based implicit implementation the
// *types.Info impl scan cannot see.
func TestMetricsCancelCtxConformance_ReverseBlindSpot(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}
	root := findModuleRoot(t)
	scope := ModuleScope(root)

	diags := Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			if strings.HasSuffix(rel, "_test.go") || !strings.HasPrefix(rel, metricsProviderScopePrefix) {
				continue
			}
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok || val != metricsProviderIfaceName {
					return
				}
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(lit.Pos()).Line,
					Message: "blind-spot B1: string literal \"Provider\" in adapters/ production code " +
						"may indicate reflect-based impl (METRICS-CANCEL-CTX-CONFORMANCE-01)",
				})
			})
		}
		return out
	})

	assert.Empty(t, diags,
		"B1 reverse: no adapters/ non-test file should contain the string literal %q as reflect bait",
		metricsProviderIfaceName)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// inMetricsProviderScope reports whether pkgPath is under adapters/.
func inMetricsProviderScope(pkgPath, modPath string) bool {
	rel := strings.TrimPrefix(pkgPath, modPath+"/")
	if rel == pkgPath {
		return false
	}
	return strings.HasPrefix(rel, metricsProviderScopePrefix)
}

// collectMetricsProviderImpls adds to implSet every concrete (non-interface)
// named type in pkg that implements metrics.Provider (directly or via pointer).
func collectMetricsProviderImpls(pkg *types.Package, iface *types.Interface, implSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		tt := obj.Type()
		if _, isIface := tt.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(tt, iface) {
			implSet[pkg.Path()+"."+name] = true
		}
	}
}

// collectEnrolledMetricsProviders records the concrete impl key of every
// Provider argument (args[1]) passed to a metricstest.RunCanceledCtxConformance
// call in file. Signature: RunCanceledCtxConformance(t, p, rb) — args[1] is
// resolved to its concrete type so enrollment is per-implementation.
func collectEnrolledMetricsProviders(file *ast.File, info *types.Info, enrolled map[string]bool) {
	if info == nil {
		return
	}
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != cancelCtxConformancePkg || name != cancelCtxConformanceFunc {
			return
		}
		if len(call.Args) < 2 {
			return
		}
		if key, ok := concreteImplKey(info, call.Args[1]); ok {
			enrolled[key] = true
		}
	})
}

// unenrolledMetricsProviders returns a sorted Diagnostic for every impl in
// implSet absent from enrolledImpls. Pure function — exercised by the REDFixture.
func unenrolledMetricsProviders(implSet, enrolledImpls map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		pkgPath := implKey
		if dotIdx := strings.LastIndex(implKey, "."); dotIdx >= 0 {
			pkgPath = implKey[:dotIdx]
		}
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: metrics.Provider impl %q not enrolled in a "+
					"metricstest.RunCanceledCtxConformance test call "+
					"(METRICS-CANCEL-CTX-CONFORMANCE-01). Add a _test.go in package %s that calls "+
					"metricstest.RunCanceledCtxConformance(t, provider, readback) passing this "+
					"concrete Provider, asserting the no-skip-on-cancel contract "+
					"(kernel/observability/metrics/metrics.go §contract (1)).",
				implKey, pkgPath,
			),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}
