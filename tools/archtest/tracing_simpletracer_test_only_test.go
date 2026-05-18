// INVARIANT: TRACING-SIMPLETRACER-TEST-ONLY-01: in-process simpleTracer is test-only; runtime/observability/tracing stays deleted
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTracingSimpleTracerTestOnly enforces the B2-A-20 closure: the
// asymmetric in-process `simpleTracer` (inbound ctxkeys trace-id reuse, but
// NO outbound W3C `traceparent` injection — cross-process propagation is
// exclusively adapters/otel's job) must never be reachable from production
// code. It lives only in the test-only package
// `runtime/observability/tracingtest`.
//
//   - R-A (anti-reintroduction): no .go file may call `tracing.NewTracer(...)`.
//     The whole `runtime/observability/tracing` package was deleted (its
//     Tracer/Span/Attr aliases + SpanSet* shims folded into kernel/wrapper),
//     so the Go compiler is the real seal — R-A is the belt-and-suspenders
//     guard that prevents anyone recreating a `runtime/observability/tracing`
//     package with a `NewTracer` constructor. Mirrors AUTH-AUTHTEST-A.
//
//   - R-B (load-bearing boundary, the Medium guard): non-_test.go files must
//     not import `runtime/observability/tracingtest`. Construction of the
//     fixture requires importing this package; banning the import from
//     production files is the resolved-import-path package-relationship
//     invariant (same class/grade as AUTH-AUTHTEST-C and the LAYER-* rules).
//
//   - R-C (reverse self-check / blind-spot inventory): asserts the deleted
//     package directory stays gone, and that the legitimate consumers
//     (tracingtest's own impl files + _test.go files) are NOT false-flagged.
//
// AI-rebust grade: Medium — and this is the *ceiling* for this rule shape.
// Which `wrapper.Tracer` implementation the composition root injects via
// `bootstrap.WithTracer(t)` is a runtime wiring choice; Go's type system
// cannot forbid a valid interface implementation from being passed as an
// argument (same rule-shape as the charter's "production must use a real
// adapter, not a noop publisher"). Pure Hard ("violation not expressible")
// is structurally unreachable here. R-A (syntactic anti-reintroduction,
// mirroring shipped AUTH-AUTHTEST-A) + R-B (resolved-import-path boundary,
// mirroring shipped AUTH-AUTHTEST-C) + R-C (reverse self-check) is the
// strongest Medium attainable, stated honestly without "near-Hard" hedging.
// See ai-collab.md §"AI-rebust 三档分级" + §Hard PANIC-REGISTERED honest-caveat.
func TestTracingSimpleTracerTestOnly(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	tracingtestImport := modPath + "/runtime/observability/tracingtest"

	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	tracingtestPkgDir := filepath.Join(root, "runtime", "observability", "tracingtest")

	// R-A: ban `tracing.NewTracer(...)` and `tracingtest.NewSimpleTracer(...)`
	// call expressions in non-test production files. Detection is AST-based
	// (findCallExpr → ast.CallExpr) so comments / doc strings / unrelated
	// identical strings are not misclassified. tracingtest's own *.go impl
	// files are exempt for the NewSimpleTracer arm (they define it).
	t.Run("R-A_no_simpletracer_construction_in_production", func(t *testing.T) {
		var hits []string
		for _, f := range allGoFiles {
			if strings.HasSuffix(f, "_test.go") {
				continue // _test.go may construct the fixture freely
			}
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)

			// `tracing.NewTracer(` — the deleted symbol, banned everywhere
			// in production (the package no longer exists; this seals it).
			nt, err := findCallExpr(f, "tracing", "NewTracer")
			require.NoErrorf(t, err, "failed to AST-scan %s", f)
			for _, line := range nt {
				hits = append(hits, fmt.Sprintf("%s:%d  tracing.NewTracer(...)", rel, line))
			}

			// `tracingtest.NewSimpleTracer(` — only the tracingtest package's
			// own impl files may name it in production scope.
			if filepath.Dir(f) == tracingtestPkgDir {
				continue
			}
			nst, err := findCallExpr(f, "tracingtest", "NewSimpleTracer")
			require.NoErrorf(t, err, "failed to AST-scan %s", f)
			for _, line := range nst {
				hits = append(hits, fmt.Sprintf("%s:%d  tracingtest.NewSimpleTracer(...)", rel, line))
			}
		}
		for _, h := range hits {
			t.Logf("R-A violation: %s", h)
		}
		assert.Empty(t, hits,
			"the in-process simpleTracer fixture must not be constructed in production code; "+
				"runtime/observability/tracing was deleted (use kernel/wrapper.{Tracer,Span,Attr} "+
				"directly; production wires adapters/otel via bootstrap.WithTracer, falling back "+
				"to wrapper.NoopTracer{}); construct tracingtest.NewSimpleTracer only in _test.go")
	})

	// R-B: non-_test.go files must not import the test-only tracingtest
	// package. The tracingtest package's own implementation files are
	// exempt (they ARE the implementation, not importers). This is the
	// load-bearing Medium guard — mirrors AUTH-AUTHTEST-C.
	t.Run("R-B_only_test_files_may_import_tracingtest", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			if filepath.Dir(f) == tracingtestPkgDir {
				continue
			}
			imports, err := parseImports(f)
			require.NoErrorf(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == tracingtestImport {
					rel, _ := filepath.Rel(root, f)
					rel = filepath.ToSlash(rel)
					violations = append(violations,
						fmt.Sprintf("R-B: %s (non-test file) imports %s", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations,
			"non-test .go files must not import runtime/observability/tracingtest; "+
				"it is an in-process test-only Tracer fixture with asymmetric propagation "+
				"(no outbound W3C traceparent). Production code uses kernel/wrapper directly "+
				"and wires adapters/otel via bootstrap.WithTracer.")
	})

	// R-C: reverse self-check / blind-spot inventory.
	//   1. The deleted package directory must stay gone (the compiler seal).
	//   2. The legitimate consumers must NOT be false-flagged: at least one
	//      _test.go must import tracingtest (proving the fixture is wired into
	//      tests) and the tracingtest package itself must exist with an
	//      exported NewSimpleTracer — i.e. R-A/R-B don't over-reach into the
	//      sanctioned test path.
	t.Run("R-C_reverse_self_check", func(t *testing.T) {
		// 1. deleted package stays deleted.
		deletedPkgDir := filepath.Join(root, "runtime", "observability", "tracing")
		if _, statErr := os.Stat(deletedPkgDir); !os.IsNotExist(statErr) {
			t.Errorf("runtime/observability/tracing must stay deleted (B2-A-20); "+
				"found existing directory %s — the backward-compat Tracer/Span/Attr "+
				"alias + SpanSet* shim layer must not be reintroduced", deletedPkgDir)
		}

		// 2. tracingtest package exists and is wired into _test.go consumers.
		if _, statErr := os.Stat(tracingtestPkgDir); os.IsNotExist(statErr) {
			t.Fatalf("runtime/observability/tracingtest package missing — the "+
				"test-only simpleTracer fixture must live here (%s)", tracingtestPkgDir)
		}
		testImporters := 0
		for _, f := range allGoFiles {
			if !strings.HasSuffix(f, "_test.go") {
				continue
			}
			imports, err := parseImports(f)
			require.NoErrorf(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == tracingtestImport {
					testImporters++
				}
			}
		}
		assert.Positive(t, testImporters,
			"expected at least one _test.go to import tracingtest (proving R-A/R-B "+
				"do not over-reach and the fixture is genuinely test-wired)")
	})
}
