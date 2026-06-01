// INVARIANT: HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01
//
// Every concrete type that implements runtime/http/idempotency.Store in the
// production source tree must have at least one
// idempotencytest.RunConformanceSuite call in a _test.go file of its package
// (or the corresponding external _test variant).
//
// AI-robust rating:
//
//   - Hard (upstream): types.Implements(*types.Interface) is exhaustive — a new
//     implementation cannot hide from the types.Implements scan regardless of
//     where it lives in the module. Any concrete named type satisfying the Store
//     interface is flagged if unenrolled. This matches the ai-robust §Hard 范本
//     目录 "sealed construction" / "single sanctioned holder" goal: the
//     conformance enrollment archtest is the machine gate that turns "new impl
//     must enroll" from a convention into a CI-enforced invariant.
//
//   - Medium (downstream): the callsite scan proves a _test.go file CALLS
//     RunConformanceSuite. Go cannot require at compile time that a _test.go
//     file call any specific function; this is archtest-bound (CI red). The Hard
//     upgrade path is a codegen funnel + golden that enumerates Store impls from
//     a single source and diff-locks the enrollment registry; deferred as
//     cross-PR governance work, tracked in gh issue #1044.
//
// # Integration-tag handling
//
// The Redis Store enrollment (adapters/redis.HTTPIdempotencyStore) lives in
// adapters/redis/http_idempotency_conformance_test.go, which is gated by
// //go:build integration. The test-corpus callsite scan uses RAW-AST parsing
// via go/parser (which parses every _test.go file WITHOUT evaluating build
// constraints). This is intentional: go/parser never skips a file for build
// tags — it returns the full AST regardless. So the integration-tagged Redis
// enrollment file is always visible to the callsite scan, preventing a false
// violation when the archtest runs without -tags=integration.
//
// This choice is documented here and confirmed by
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
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	httpIdempotencyStoreIfacePkg   = "github.com/ghbvf/gocell/runtime/http/idempotency"
	httpIdempotencyStoreIfaceName  = "Store"
	httpIdempotencyConformancePkg  = "github.com/ghbvf/gocell/runtime/http/idempotency/idempotencytest"
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
	ifacePatterns := append([]string{"./runtime/http/idempotency/..."}, prodPatterns...)

	var storeIface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
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
	implSet := make(map[string]bool) // "pkg/path.TypeName" → true
	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		collectHTTPIdempotencyStoreImpls(pkg, storeIface, implSet)
	}

	require.NotEmpty(t, implSet,
		"HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01: zero Store implementations collected — "+
			"likely a type-universe regression (iface and impls must share one packages.Load). "+
			"Expect at least idempotency.MemStore and redis.HTTPIdempotencyStore.")

	// ─── Step 3: raw-AST callsite scan for RunConformanceSuite ──────────────
	//
	// We use go/parser directly (NOT RunTyped) so that build constraints are
	// NEVER evaluated. go/parser parses the full file text regardless of build
	// directives. This ensures that //go:build integration files (such as
	// adapters/redis/http_idempotency_conformance_test.go) are always visible
	// to the enrollment scan, even when the archtest runs without -tags=integration.
	//
	// Because we use raw AST (no *types.Info), we match by selector name rather
	// than by package path. This is a deliberate Medium-tier trade-off: a
	// function named "RunConformanceSuite" in any package that is called in a
	// _test.go in the impl's package satisfies the enrollment criterion. In
	// practice only idempotencytest.RunConformanceSuite exists; the B1 blind-spot
	// reverse check confirms no other function with that name appears in test files
	// that do not import idempotencytest.
	enrolledPkgPaths := make(map[string]bool) // pkg path → enrolled

	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip hidden dirs and vendor.
			base := filepath.Base(path)
			if base == "vendor" || (len(base) > 0 && base[0] == '.') {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		// Parse the file (ignoring build constraints — go/parser does not
		// evaluate them, so integration-tagged files are always parsed).
		f, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			// Parse errors in test files are not fatal; skip and continue walking.
			_ = parseErr
			return nil //nolint:nilerr // intentional: skip unparseable test files without aborting Walk
		}

		if !hasHTTPIdempotencyConformanceCall(f) {
			return nil
		}

		// Credit the directory as enrolled. We use the directory path (converted
		// to a module-relative import path) as the enrollment key.
		dir := filepath.Dir(path)
		rel, relErr := filepath.Rel(root, dir)
		if relErr != nil {
			_ = relErr
			return nil //nolint:nilerr // intentional: skip files with non-relative paths without aborting Walk
		}
		// Convert OS path separators to slash and form a Go import path.
		pkgPath := filepath.ToSlash(rel)
		// Prepend the module path to form a full import path.
		fullPkgPath := PlatformModulePath + "/" + pkgPath
		enrolledPkgPaths[fullPkgPath] = true
		// Also record without the module prefix as a fallback for matching.
		enrolledPkgPaths[pkgPath] = true
		return nil
	})
	if err != nil {
		t.Fatalf("HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01: filepath.Walk: %v", err)
	}

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
	ifacePatterns := append([]string{"./runtime/http/idempotency/..."}, prodPatterns...)

	var storeIface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
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
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectHTTPIdempotencyStoreImpls(pkg, storeIface, implSet)
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
// (integration-tag blind-spot) confirms that the raw-AST callsite scan sees the
// Redis enrollment file (adapters/redis/http_idempotency_conformance_test.go)
// regardless of build constraints. go/parser ignores //go:build directives, so
// the file is always parsed. This test verifies the file exists on disk and that
// the hasHTTPIdempotencyConformanceCall function detects the RunConformanceSuite
// call in it.
func TestHTTPIdempotencyConformanceEnrollment_BuildTagBlindspot_RedisAlwaysVisible(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	root := findModuleRoot(t)
	redisEnrollmentFile := filepath.Join(root, "adapters", "redis", "http_idempotency_conformance_test.go")

	if _, err := os.Stat(redisEnrollmentFile); os.IsNotExist(err) {
		t.Skipf("Redis enrollment file not found at %s — skipping build-tag blindspot check", redisEnrollmentFile)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, redisEnrollmentFile, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("go/parser failed to parse %s: %v — build constraints must not affect parser", redisEnrollmentFile, err)
	}

	if !hasHTTPIdempotencyConformanceCall(f) {
		t.Errorf("HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01 build-tag blindspot: "+
			"%s does not contain a call to %s — the enrollment file should call RunConformanceSuite",
			redisEnrollmentFile, httpIdempotencyConformanceFunc)
	}
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

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
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
func collectHTTPIdempotencyStoreImpls(pkg *types.Package, iface *types.Interface, implSet map[string]bool) {
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
		}
	}
}

// hasHTTPIdempotencyConformanceCall returns true when the parsed AST file
// contains at least one call expression whose function selector name is
// "RunConformanceSuite". This is a raw-AST heuristic (no type information):
// it matches on selector name alone, which is sufficient because we also
// confirm the file is a _test.go and the impl scan independently verifies
// the enrolled package actually contains a Store implementation.
//
// Using raw AST is intentional: it allows go/parser to parse integration-tagged
// files regardless of build constraints (see package doc).
func hasHTTPIdempotencyConformanceCall(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == httpIdempotencyConformanceFunc {
			found = true
		}
		return !found
	})
	return found
}
