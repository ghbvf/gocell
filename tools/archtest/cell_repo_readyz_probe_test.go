// INVARIANT: CELL-REPO-READYZ-PROBE-01
//
// AI-robust: Medium (test-existence backstop — NOT a funnel upstream lock).
//
// This archtest is a *test-existence backstop*: it enforces that every concrete
// kernel/healthz.RepoProber implementation is exercised by the behavioral
// conformance harness. It is deliberately NOT the upstream half of the
// RegisterRepoReady registration funnel — that funnel's downstream caller-side
// guards (HEALTHZ-WRITE-01/A2 + HEALTHZ-TYPED-REGISTER-01) carry its grading
// (see those rules). Its upstream type-system seal is the only Hard upstream form
// and is infeasible, so HEALTHZ-HOLDER-SEAL-01 (gh #893) is won't-do (see
// HEALTHZ-WRITE-01/A3 godoc). §"Funnel 双向锁评级" governs that funnel, not this
// backstop, so no funnel Hard-ization task is owed here.
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware, identifies every
//     concrete named type (exported OR unexported) that satisfies
//     kernel/healthz.RepoProber (or *T), restricted to the enrollable production
//     layers (see scope below).
//   - conformance 调用扫描: per *implementation*. Each RunRepoReadinessConformance
//     call's healthy/broken prober argument is resolved via *types.Info to its
//     concrete type key, so a package with N impls must conformance-test each of
//     the N — enrolling one does not cover its same-package siblings.
//   - 综合: Medium is the **ceiling by construction** — Go cannot require a test
//     to exist at compile time. This is not a transitional rung with a Hard
//     upgrade path; it is the established shape of the sibling
//     USERREPO-CONFORMANCE-ENROLLMENT-01. The behavioral correctness of the
//     harness itself is Hard (see below).
//
// Enforces: every concrete type in the enrollable production tree that
// implements kernel/healthz.RepoProber must be passed as a prober to at least
// one celltest.RunRepoReadinessConformance call in a _test.go file. Impls with
// no such call are reported as violations.
//
// The conformance harness itself (kernel/cell/celltest.RunRepoReadinessConformance)
// carries the differentiated *behavioral* property at Hard (behavioral max):
// scenario 2 (a SQL store with its relation dropped MUST return non-nil) cannot
// be satisfied by a no-op `return nil`. This archtest guards the complementary
// *per-impl test-existence* property — the Medium layer the behavioral Hard sits on.
//
// # Scope (enrollable production layers only)
//
//   - In scope: package paths under cells/ + adapters/ + runtime/ + examples/.
//     All RepoProber impls there can hang a RunRepoReadinessConformance call off
//     a _test.go in their own package.
//
//   - Excluded: kernel/. CELLTEST-IMPORT-BOUNDARY-01 (CELLTEST-B) forbids
//     kernel/** (including _test.go) from importing kernel/cell/celltest, so the
//     kernel RepoProber impls (kernel/saga/journal.MemJournal,
//     kernel/command.InMemQueue) are architecturally un-enrollable via this
//     harness. Both are in-memory no-ops with no differentiated failure domain;
//     their differentiated PG siblings live in adapters/postgres and ARE
//     enrolled (command-queue-pg, etc.). This is a layering invariant, not a
//     deferred task — there is nothing to enroll and nothing to backlog.
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations (reflect.Value.MethodByName…):
//     no production code uses this pattern; confirmed by
//     TestCellRepoReadyzProbe_ReverseBlindSpot_NoReflectImpl, which scans for the
//     interface name "RepoProber" as a string literal in production non-test
//     files (zero legitimate occurrences — pure reflect bait).
//
//   - B2. generated mock implementations (mockery / gomock in _test.go) are
//     excluded: test-file types are not scanned for implementations (Tests=false
//     in the production load pass). A generated mock in a production non-test
//     file would be flagged — intentionally.
//
//   - B3. embedded interface forwarding (struct embedding healthz.RepoProber):
//     such a type structurally satisfies the interface but provides no real
//     storage. These appear only in test helpers (excluded from the impl scan).
//     Production structs that embed the interface are treated as implementations
//     and must enroll — and the enrolling test must supply a real backing value,
//     since calling RepoReady on a struct whose embedded RepoProber field is nil
//     will nil-panic inside the harness.
//
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (sibling backstop pattern)
// ref: docs/architecture/202605161030-adr-cell-repo-readyz-probe.md §AI-robust Rating (Mechanism 2)
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
	// repoProberIfacePkg aliases repoProberIfacePkgPath (declared in the
	// non-test cell_repo_readyz_probe.go) so this _test.go remains
	// self-descriptive without reintroducing a bare literal.
	repoProberIfacePkg = repoProberIfacePkgPath

	repoProberIfaceName = "RepoProber"

	// repoReadinessConformancePkg aliases repoReadinessConformancePkgPath
	// (declared in the non-test cell_repo_readyz_probe.go).
	repoReadinessConformancePkg  = repoReadinessConformancePkgPath
	repoReadinessConformanceFunc = "RunRepoReadinessConformance"
)

// readyzProbeScopePrefixes are the module-relative package-path prefixes whose
// RepoProber implementations are enrollable in RunRepoReadinessConformance.
// kernel/ is deliberately absent (CELLTEST-B forbids the celltest import).
var readyzProbeScopePrefixes = []string{"cells/", "adapters/", "runtime/", "examples/"}

// TestCellRepoReadyzProbe enforces CELL-REPO-READYZ-PROBE-01: every concrete
// type implementing kernel/healthz.RepoProber in the enrollable production tree
// must have a celltest.RunRepoReadinessConformance call in a _test.go file of
// its package.
func TestCellRepoReadyzProbe(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	modPath := readModulePath(t, root)

	// ─── Step 1: resolve healthz.RepoProber + collect in-scope impl pkgs ──────
	//
	// The iface and the impl types MUST come from the same packages.Load
	// invocation so types.Implements uses pointer-identical *types.Named
	// descriptors. prodscan.Patterns includes kernel/, so the healthz package is
	// loaded and the iface is captured here.
	prodPatterns := prodscan.Patterns(root)

	var repoProberIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == repoProberIfacePkg {
				if obj := p.Pkg.Scope().Lookup(repoProberIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							repoProberIface = iface.Complete()
						}
					}
				}
				return nil
			}

			if inReadyzProbeScope(p.Pkg.Path(), modPath) {
				implPkgs = append(implPkgs, p.Pkg)
			}
			return nil
		})

	require.NotNil(t, repoProberIface,
		"CELL-REPO-READYZ-PROBE-01: failed to resolve healthz.RepoProber interface; "+
			"check import path %s", repoProberIfacePkg)

	// ─── Step 2: collect all concrete in-scope implementations ────────────────
	implSet := make(map[string]bool) // "pkg/path.TypeName" → true
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectRepoProberImpls(pkg, repoProberIface, implSet)
		}
	}

	// Regression guard: zero impls means the type-universe is broken.
	require.NotEmpty(t, implSet,
		"CELL-REPO-READYZ-PROBE-01: zero RepoProber implementations collected — "+
			"likely a type-universe regression (iface and impls must share one packages.Load). "+
			"Expect at least runtime/saga.Coordinator and the configcore/session/ledger mem + PG stores.")

	// ─── Step 3: scan test corpus for RunRepoReadinessConformance call sites ──
	//
	// Enrollment is tracked per *concrete implementation* (not per package): each
	// call's healthy/broken prober argument is resolved to its concrete type key
	// via TypesInfo. A package with N RepoProber impls must conformance-test each
	// of the N — enrolling one does not cover its same-package siblings.
	enrolledImpls := make(map[string]bool) // "pkg/path.TypeName" → true

	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				if !strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				collectEnrolledRepoProberImpls(f, p.TypesInfo, enrolledImpls)
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ──────────────────────────────
	diags := unenrolledRepoProberImpls(implSet, enrolledImpls)
	Report(t, "CELL-REPO-READYZ-PROBE-01", diags)
}

// TestCellRepoReadyzProbe_REDFixture exercises the per-impl flag logic
// (unenrolledRepoProberImpls) with synthetic impl/enrollment sets. It needs no
// packages.Load because the flag logic is a pure function. The
// same-package-partial case is the F1 regression guard: package-granularity
// enrollment would FALSE-GREEN it (both impls share an enrolled package), so
// this case fails unless enrollment is tracked per concrete implementation.
func TestCellRepoReadyzProbe_REDFixture(t *testing.T) {
	t.Parallel()

	const (
		pgPkg   = repoProberAdapterPostgresPkgPath
		sagaPkg = repoProberRuntimeSagaPkgPath
		ledger  = pgPkg + ".LedgerStore"
		session = pgPkg + ".PGSessionStore" // same package as ledger
		coord   = sagaPkg + ".Coordinator"
	)
	implSet := map[string]bool{ledger: true, session: true, coord: true}

	flaggedKeys := func(diags []Diagnostic) map[string]bool {
		out := make(map[string]bool, len(diags))
		for _, d := range diags {
			out[d.Rel] = true
		}
		return out
	}

	t.Run("missing-impl-enrollment", func(t *testing.T) {
		t.Parallel()
		// ledger + session enrolled; Coordinator omitted → only Coordinator flagged.
		diags := unenrolledRepoProberImpls(implSet, map[string]bool{ledger: true, session: true})
		got := flaggedKeys(diags)
		assert.True(t, got[coord], "Coordinator must be flagged when unenrolled")
		assert.Len(t, diags, 1, "only the unenrolled impl should be flagged: %v", diags)
	})

	t.Run("same-package-partial-enrollment", func(t *testing.T) {
		t.Parallel()
		// pgPkg has TWO impls; only ledger enrolled. Per-impl logic must still
		// flag session (package-granularity would false-green here).
		diags := unenrolledRepoProberImpls(implSet, map[string]bool{ledger: true, coord: true})
		got := flaggedKeys(diags)
		assert.True(t, got[session],
			"same-package sibling PGSessionStore must be flagged when only LedgerStore is enrolled")
		assert.False(t, got[ledger], "enrolled LedgerStore must not be flagged")
		assert.Len(t, diags, 1, "exactly the unenrolled sibling should be flagged: %v", diags)
	})

	t.Run("all-enrolled-green", func(t *testing.T) {
		t.Parallel()
		diags := unenrolledRepoProberImpls(implSet, implSet)
		assert.Empty(t, diags, "fully enrolled implSet must produce zero violations")
	})
}

// TestCellRepoReadyzProbe_ReverseBlindSpot_NoReflectImpl (blind spot B1) confirms
// no production non-test file references the interface name "RepoProber" as a
// string literal — which would indicate a reflect-based implicit implementation
// the *types.Info impl scan cannot see. Zero legitimate occurrences exist, so a
// hit is pure reflect bait.
func TestCellRepoReadyzProbe_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
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
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok || val != repoProberIfaceName {
					return
				}
				const b1msg = "blind-spot B1: string literal \"RepoProber\" in production code " +
					"may indicate reflect-based impl (CELL-REPO-READYZ-PROBE-01)"
				out = append(out, Diagnostic{
					Rel:     rel,
					Line:    p.Fset.Position(lit.Pos()).Line,
					Message: b1msg,
				})
			})
		}
		return out
	})

	assert.Empty(t, diags,
		"B1 reverse: no production non-test file should contain the string literal %q as reflect bait",
		repoProberIfaceName)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// inReadyzProbeScope reports whether pkgPath (a full import path) is under one
// of the enrollable production layers. The iface package (kernel/healthz) and
// all other kernel/ packages return false.
func inReadyzProbeScope(pkgPath, modPath string) bool {
	rel := strings.TrimPrefix(pkgPath, modPath+"/")
	if rel == pkgPath {
		return false // module root package, or not under the module — no enrollable prefix
	}
	for _, prefix := range readyzProbeScopePrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// collectRepoProberImpls adds to implSet every concrete (non-interface) named
// type in pkg that implements healthz.RepoProber (directly or via pointer).
// Both exported and unexported types are collected — RegisterRepoReady accepts
// any healthz.RepoProber, so an unexported store is just as registerable and
// must be conformance-tested (fail-closed: "every concrete type").
func collectRepoProberImpls(pkg *types.Package, iface *types.Interface, implSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		t := obj.Type()
		if _, isIface := t.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(t, iface) {
			implSet[pkg.Path()+"."+name] = true
		}
	}
}

// collectEnrolledRepoProberImpls records, into enrolled, the concrete impl key
// of every prober argument passed to a celltest.RunRepoReadinessConformance
// call in file. Signature: RunRepoReadinessConformance(t, name, healthy, broken)
// — args[2]/args[3] are resolved to their concrete types so enrollment is
// per-implementation, not per-package. Conformance calls must therefore pass
// concretely-typed prober arguments (constructors / := vars), not values stored
// in interface-typed variables.
func collectEnrolledRepoProberImpls(file *ast.File, info *types.Info, enrolled map[string]bool) {
	if info == nil {
		return
	}
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != repoReadinessConformancePkg || name != repoReadinessConformanceFunc {
			return
		}
		for _, idx := range []int{2, 3} { // healthy, broken
			if idx >= len(call.Args) {
				continue
			}
			if key, ok := concreteImplKey(info, call.Args[idx]); ok {
				enrolled[key] = true
			}
		}
	})
}

// concreteImplKey resolves expr's static type to a concrete impl key
// ("pkg/path.TypeName"), unwrapping a single pointer. It returns ok=false for
// nil, interface-typed, or unnamed expressions (e.g. a literal nil broken).
func concreteImplKey(info *types.Info, expr ast.Expr) (string, bool) {
	t := info.TypeOf(expr)
	if t == nil {
		return "", false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", false
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name(), true
}

// unenrolledRepoProberImpls returns a sorted Diagnostic for every impl in
// implSet whose concrete key is absent from enrolledImpls. It is a pure
// function so the REDFixture can exercise the per-impl flag logic — including
// the same-package-partial-enrollment case — without packages.Load.
func unenrolledRepoProberImpls(implSet, enrolledImpls map[string]bool) []Diagnostic {
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
				"archtest: healthz.RepoProber impl %q not enrolled in a "+
					"celltest.RunRepoReadinessConformance test call "+
					"(CELL-REPO-READYZ-PROBE-01). Add a _test.go in package %s that calls "+
					"celltest.RunRepoReadinessConformance(t, name, healthy, broken) passing this "+
					"concrete type as the healthy prober. If the impl gates readiness on a "+
					"lifecycle state (e.g. *Coordinator), the healthy prober must already be "+
					"in a running state.",
				implKey, pkgPath,
			),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}
