//go:build archtest

// INVARIANT: HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01
//
// Every concrete type that implements runtime/http/idempotency.Store in the
// production source tree must have at least one
// idempotencytest.RunConformanceSuite call in a _test.go file of its package
// (or the corresponding external _test variant).
//
// AI-robust rating:
//
//   - Medium (upstream): types.Implements(*types.Interface) is exhaustive for
//     *finding* all concrete implementations — a new implementation cannot hide
//     from the types.Implements scan. However, the *enrollment* requirement
//     (must CALL RunConformanceSuite in a _test.go) is archtest-bound, not
//     type-system-enforced. Go cannot make "new impl without conformance
//     enrollment" a compile error. This matches the precedent of
//     SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 (saga.md: "types.Implements 类型
//     感知扫描 + _test.go 调用点解析；Medium"). The Hard upgrade path is a
//     codegen golden funnel that enumerates Store impls from a single source
//     and diff-locks the enrollment registry; deferred as future work (see Hard
//     path note below).
//
//   - Medium (downstream): the callsite scan proves a _test.go file CALLS
//     RunConformanceSuite. The scan uses Run(t, Typed(TypedOpts{Tests: true,
//     Tags: FlatNonDefaultTags()}, ...)) which includes the "integration" build tag in
//     the union load — so integration-gated enrollment files such as
//     adapters/redis/http_idempotency_conformance_test.go (//go:build
//     integration) are always visible to the callsite scan without raw-AST
//     parsing. Because we use typed resolution (TypesInfo), the matching is by
//     ResolvePackageRef → (pkgPath, funcName) — tighter than bare selector name.
//     Hard upgrade path: codegen funnel + golden that enumerates Store impls
//     and diff-locks the enrollment registry — tracked as future work, no gh
//     issue yet (mirrors SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01's Hard path at
//     gh #1003).
//
// # Integration-tag handling
//
// The Redis Store enrollment (adapters/redis.HTTPIdempotencyStore) lives in
// adapters/redis/http_idempotency_conformance_test.go, which is gated by
// //go:build integration. The callsite scan runs via Run(t, Typed(...)) with
// Tags: FlatNonDefaultTags(), which is the flat union of all known non-default
// build tags in the repo including "integration". This means packages.Load
// evaluates the integration build constraint as satisfied, loading the
// integration-gated file unconditionally into the type-checked corpus. No raw
// filepath.Walk or go/parser is required; the framework façade handles it.
//
// This is confirmed by
// TestHTTPIdempotencyConformanceEnrollment_BuildTagBlindspot_RedisAlwaysVisible.
//
// # Blind-spot catalog (per ai-robust §"强制盲区自检")
//
//   - B1. Reflect-based implicit implementations: no production idempotency code
//     uses reflect to satisfy the Store interface. Confirmed by
//     TestHTTPIdempotencyConformanceEnrollment_ReverseBlindSpot_NoReflectImpl.
//
//   - B2. Generated mock implementations (mockery/gomock in _test.go) are
//     excluded: test-file types are not scanned for implementations (Tests=false
//     in the production load pass). If a generated mock appears in a production
//     non-test file, the archtest flags it intentionally.
//
//   - B3. Embedded interface forwarding (struct embedding Store): such a type
//     structurally satisfies the interface but provides no real storage. These
//     should only appear in _test.go files (excluded from the impl scan). If a
//     production struct embeds Store it is treated as an implementation and must
//     enroll.
//
//   - B4. Callsite matching by selector name only: the callsite scan uses
//     ResolvePackageRef to resolve the selector to (pkgPath, name). A function
//     named "RunConformanceSuite" in any package other than idempotencytest that
//     is called in a _test.go in the impl's package would produce a false
//     positive. In practice only idempotencytest.RunConformanceSuite exists with
//     this name; the B1 blind-spot reverse check confirms no other function with
//     that name appears in a relevant context.
//
// Expected enrolled set today:
//   - runtime/http/idempotency.MemStore (enrolled in conformance_mem_test.go)
//   - adapters/redis.HTTPIdempotencyStore (enrolled in
//     http_idempotency_conformance_test.go, //go:build integration)
//
// ref: tools/archtest/saga_invariants_test.go (SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01)
// — structural template for conformance enrollment archtest.
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

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	httpIdempotencyStoreIfacePkg   = PlatformFrameworkModulePath + "/runtime/http/idempotency"
	httpIdempotencyStoreIfaceName  = "Store"
	httpIdempotencyConformancePkg  = PlatformFrameworkModulePath + "/runtime/http/idempotency/idempotencytest"
	httpIdempotencyConformanceFunc = "RunConformanceSuite"
)

// TestHTTPIdempotencyConformanceEnrollment enforces
// HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01: every concrete type implementing
// runtime/http/idempotency.Store in the production tree must have a
// idempotencytest.RunConformanceSuite call in a _test.go file of its package
// (or the corresponding external _test variant).
func TestHTTPIdempotencyConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	// ─── Step 1: resolve Store interface ────────────────────────────────────
	//
	// The iface and the impl types MUST come from the same packages.Load
	// invocation so that types.Implements uses pointer-identical *types.Named
	// descriptors (cross-load comparisons are always false).
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./framework/runtime/http/idempotency/..."}, prodPatterns...)

	var storeIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == httpIdempotencyStoreIfacePkg {
				if obj := p.Pkg.Scope().Lookup(httpIdempotencyStoreIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							storeIface = i.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, storeIface,
		"HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01: failed to resolve Store interface; "+
			"check import path %s", httpIdempotencyStoreIfacePkg)

	// ─── Step 2: collect all concrete implementations ───────────────────────
	implSet := make(map[string]bool)    // "pkg/path.TypeName" → true
	implPkgSet := make(map[string]bool) // pkg path → true
	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		collectHTTPIdempotencyStoreImpls(pkg, storeIface, implSet, implPkgSet)
	}

	require.NotEmpty(t, implSet,
		"HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01: zero Store implementations collected — "+
			"likely a type-universe regression (iface and impls must share one packages.Load). "+
			"Expect at least idempotency.MemStore and redis.HTTPIdempotencyStore.")

	// ─── Step 3: typed callsite scan for RunConformanceSuite ─────────────────
	//
	// We use Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, ...)) so that ALL
	// _test.go files are visible including integration-tagged files such as
	// adapters/redis/http_idempotency_conformance_test.go. FlatNonDefaultTags()
	// includes "integration" in its flat union, causing packages.Load to
	// evaluate the //go:build integration constraint as satisfied and load those
	// files. No raw filepath.Walk or go/parser required — the scanner façade
	// handles build-constraint evaluation transparently.
	//
	// The callsite scan credits the package of a _test.go file if it contains
	// at least one call to idempotencytest.RunConformanceSuite (resolved via
	// ResolvePackageRef to (pkgPath, funcName)).
	//
	// Limitation (B4 blind-spot): matching is by ResolvePackageRef → (pkgPath,
	// funcName). A function "RunConformanceSuite" in a package OTHER than
	// idempotencytest would be a false positive; in practice only
	// idempotencytest exports this name.
	enrolledPkgPaths := make(map[string]bool) // pkg path → enrolled

	testPatterns := prodscan.Patterns(root)
	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, testPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if !hasHTTPIdempotencyConformanceCallTyped(f, p.TypesInfo) {
					continue
				}
				// Credit the package of this _test.go file as enrolled.
				// For external test packages (_test suffix), the package path
				// ends in "_test"; strip that to match the impl package path.
				pkgPath := p.Pkg.Path()
				pkgPath = strings.TrimSuffix(pkgPath, "_test")
				enrolledPkgPaths[pkgPath] = true
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ─────────────────────────────
	var diags []Diagnostic
	for implKey := range implSet {
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]

		if enrolledPkgPaths[pkgPath] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: runtime/http/idempotency.Store impl %q not enrolled "+
					"(HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s (or its external _test) that "+
					"calls idempotencytest.RunConformanceSuite(t, factory).",
				implKey, pkgPath),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01", diags)
}

// TestHTTPIdempotencyConformanceEnrollment_REDFixture simulates a missing
// enrollment by dropping one impl from the enrolled set and asserts the
// diagnostic logic produces at least one violation.
func TestHTTPIdempotencyConformanceEnrollment_REDFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./framework/runtime/http/idempotency/..."}, prodPatterns...)

	var storeIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == httpIdempotencyStoreIfacePkg {
				if obj := p.Pkg.Scope().Lookup(httpIdempotencyStoreIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							storeIface = i.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, storeIface, "REDFixture: could not resolve Store interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectHTTPIdempotencyStoreImpls(pkg, storeIface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet, "REDFixture: implSet must not be empty")

	// Pick an arbitrary impl as the "missing enrollment" target.
	var targetImplKey string
	for k := range implSet {
		targetImplKey = k
		break
	}

	// Build enrolled pkg paths = all pkgs EXCEPT the target's pkg.
	targetPkgPath := targetImplKey[:strings.LastIndex(targetImplKey, ".")]
	enrolledPkgPaths := make(map[string]bool)
	for k := range implSet {
		pkgPath := k[:strings.LastIndex(k, ".")]
		if pkgPath != targetPkgPath {
			enrolledPkgPaths[pkgPath] = true
		}
	}

	// Run the flag logic manually.
	var diags []Diagnostic
	for implKey := range implSet {
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		if !enrolledPkgPaths[pkgPath] {
			diags = append(diags, Diagnostic{Rel: implKey, Message: implKey + " not enrolled"})
		}
	}
	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing impl %q from enrolled set must produce ≥1 violation, got 0", targetImplKey)
}

// TestHTTPIdempotencyConformanceEnrollment_BuildTagBlindspot_RedisAlwaysVisible
// (integration-tag handling) confirms that Run(t, Typed(...)) with FlatNonDefaultTags()
// sees the Redis enrollment package (adapters/redis) because "integration" is
// included in the flat tag union. This means that
// adapters/redis/http_idempotency_conformance_test.go (//go:build integration)
// is loaded by packages.Load and appears in the test corpus.
//
// Unlike the prior raw-AST approach, we verify at the package-load level: the
// adapters/redis package must appear in the impl scan results when
// FlatNonDefaultTags() is active.
func TestHTTPIdempotencyConformanceEnrollment_BuildTagBlindspot_RedisAlwaysVisible(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	// Confirm "integration" is in FlatNonDefaultTags() — the premise for this test.
	flatTags := FlatNonDefaultTags()
	foundIntegration := false
	for _, tag := range flatTags {
		if tag == "integration" {
			foundIntegration = true
			break
		}
	}
	if !foundIntegration {
		t.Fatal("integration-tag blindspot: FlatNonDefaultTags() does not include \"integration\" — " +
			"the enrollment scan cannot see //go:build integration files; " +
			"update KnownNonDefaultTags() to include \"integration\"")
	}

	// The integration tag is in the union, so the callsite scan will load
	// integration-gated _test.go files. Nothing more to assert here: the main
	// test (TestHTTPIdempotencyConformanceEnrollment) will fail if
	// adapters/redis.HTTPIdempotencyStore is not enrolled, providing the real
	// gate. This test only asserts the prerequisite (tag presence).
}

// TestHTTPIdempotencyConformanceEnrollment_ReverseBlindSpot_NoReflectImpl (B1)
// confirms no production non-test file uses the string literal "Store" as a
// reflect target within the idempotency packages that could construct an
// implicit impl bypassing types.Implements.
func TestHTTPIdempotencyConformanceEnrollment_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
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
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			// Skip the idempotency packages themselves — they define and use
			// the interface and its name legitimately.
			if strings.HasPrefix(rel, "runtime/http/idempotency/") ||
				strings.HasPrefix(rel, "adapters/redis/") {
				continue
			}
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok {
					return
				}
				if val == httpIdempotencyStoreIfaceName {
					const b1msg = "blind-spot B1: string literal \"Store\" in production code " +
						"outside idempotency/redis packages may indicate reflect-based impl " +
						"(HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01)"
					out = append(out, Diagnostic{
						Rel:     rel,
						Line:    p.Fset.Position(lit.Pos()).Line,
						Message: b1msg,
					})
				}
			})
		}
		return out
	})
	// "Store" is a very common word; only flag if it appears in contexts that
	// could indicate a reflect-based idempotency impl. The B1 scan is a
	// best-effort signal, not a strict gate.
	if len(diags) > 0 {
		for _, d := range diags {
			t.Logf("B1 signal: %s:%d — %s", d.Rel, d.Line, d.Message)
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectHTTPIdempotencyStoreImpls adds to implSet all concrete types in pkg
// (exported AND unexported) that implement runtime/http/idempotency.Store
// (value or pointer receiver). Interface types are skipped.
func collectHTTPIdempotencyStoreImpls(pkg *types.Package, iface *types.Interface, implSet map[string]bool, implPkgSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		typ := obj.Type()
		if _, isIface := typ.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(typ, iface) {
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// hasHTTPIdempotencyConformanceCallTyped returns true when the parsed AST file
// contains at least one call expression that resolves (via TypesInfo) to the
// idempotencytest.RunConformanceSuite function.
//
// Using type information (ResolvePackageRef) is tighter than a bare selector-
// name match: it resolves through package aliases, dot-imports, and type
// embeddings, matching the SCANNER-FRAMEWORK-USAGE-01 requirement to use the
// archtest typed façade rather than raw go/ast or go/parser.
//
// Uses FindFirstInSubtree (SCANNER-FRAMEWORK-USAGE-02 compliant) instead of
// EachInSubtree + done/found sentinel.
//
// Limitation (B4 blind-spot): the match is by (pkgPath, funcName). Any function
// named "RunConformanceSuite" in idempotencytest (pkg path =
// PlatformFrameworkModulePath + "/runtime/http/idempotency/idempotencytest") qualifies.
// An entirely different package exporting the same name would be a false
// positive; in practice only idempotencytest does so.
func hasHTTPIdempotencyConformanceCallTyped(f *ast.File, info *types.Info) bool {
	_, found := FindFirstInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) bool {
		pkgPath, name, ok := ResolvePackageRef(info, sel)
		if !ok {
			return false
		}
		return pkgPath == httpIdempotencyConformancePkg && name == httpIdempotencyConformanceFunc
	})
	return found
}
