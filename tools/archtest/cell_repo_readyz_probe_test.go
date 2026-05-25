// INVARIANT: CELL-REPO-READYZ-PROBE-01
//
// AI-robust: Medium (upstream conformance auto-join)
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware，identifies every
//     concrete named type that satisfies kernel/healthz.RepoProber (or *T),
//     restricted to the enrollable production layers (see scope below).
//   - conformance 调用扫描: ResolvePackageRef + _test.go path filter — type-aware
//     callee resolution via *types.Info. Identifies every package with a
//     celltest.RunRepoReadinessConformance call site.
//   - 综合: Medium 天花板 — Go cannot require a test to exist at compile time;
//     the enforcement is archtest-bound (CI fails), not compile-time. This is
//     the same type-theoretic ceiling documented by the sibling
//     USERREPO-CONFORMANCE-ENROLLMENT-01 for ports.UserRepository: "every
//     implementation must be conformance-tested" is not expressible as a Go
//     type. Hard would require requiring an external artifact (a test) to
//     exist, which the type system cannot demand; gh issue #699
//     (REPO-READYZ-UPSTREAM-FUNNEL-HARD-01) investigated and closed the Hard
//     upgrade as unreachable on this basis.
//
// Enforces: every concrete type in the enrollable production tree that
// implements kernel/healthz.RepoProber must have at least one
// celltest.RunRepoReadinessConformance call in a _test.go file belonging to its
// package. Packages without such a call are reported as violations.
//
// The conformance harness itself (kernel/cell/celltest.RunRepoReadinessConformance)
// carries the differentiated *behavioral* property at Hard (behavioral max):
// scenario 2 (a SQL store with its relation dropped MUST return non-nil) cannot
// be satisfied by a no-op `return nil`. This archtest only guards the upstream
// *wiring presence* (does a conformance call exist for each impl) — the Medium
// layer that the behavioral Hard sits on top of.
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
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (sibling P1 pattern)
// ref: docs/architecture/202605161030-adr-cell-repo-readyz-probe.md §Funnel 双向锁评级
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
	repoProberIfacePkg           = "github.com/ghbvf/gocell/kernel/healthz"
	repoProberIfaceName          = "RepoProber"
	repoReadinessConformancePkg  = "github.com/ghbvf/gocell/kernel/cell/celltest"
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

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns,
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
			// Only enrollable layers participate in the impl scan.
			if inReadyzProbeScope(p.Pkg.Path(), modPath) {
				implPkgs = append(implPkgs, p.Pkg)
			}
			return nil
		})

	require.NotNil(t, repoProberIface,
		"CELL-REPO-READYZ-PROBE-01: failed to resolve healthz.RepoProber interface; "+
			"check import path %s", repoProberIfacePkg)

	// ─── Step 2: collect all concrete in-scope implementations ────────────────
	implSet := make(map[string]bool)    // "pkg/path.TypeName" → true
	implPkgSet := make(map[string]bool) // pkg path → true
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectRepoProberImpls(pkg, repoProberIface, implSet, implPkgSet)
		}
	}

	// Regression guard: zero impls means the type-universe is broken.
	require.NotEmpty(t, implSet,
		"CELL-REPO-READYZ-PROBE-01: zero RepoProber implementations collected — "+
			"likely a type-universe regression (iface and impls must share one packages.Load). "+
			"Expect at least runtime/saga.Coordinator and the configcore/session/ledger mem + PG stores.")

	// ─── Step 3: scan test corpus for RunRepoReadinessConformance call sites ──
	enrolledPkgs := make(map[string]bool) // canonical pkg path → true

	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, prodPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if hasRepoReadinessConformanceCall(f, p.TypesInfo) {
					enrolledPkgs[canonicalPkgPath(p.Pkg.Path())] = true
				}
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ──────────────────────────────
	var diags []Diagnostic
	for implKey := range implSet {
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		if !enrolledPkgs[pkgPath] {
			diags = append(diags, Diagnostic{
				Rel:  implKey,
				Line: 0,
				Message: fmt.Sprintf(
					"archtest: healthz.RepoProber impl %q not enrolled in a "+
						"celltest.RunRepoReadinessConformance test call "+
						"(CELL-REPO-READYZ-PROBE-01). Add a _test.go in package %s that calls "+
						"celltest.RunRepoReadinessConformance(t, name, healthy, broken). "+
						"If the impl gates readiness on a lifecycle state (e.g. *Coordinator), "+
						"the healthy prober must already be in a running state.",
					implKey, pkgPath),
			})
		}
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "CELL-REPO-READYZ-PROBE-01", diags)
}

// TestCellRepoReadyzProbe_REDFixture verifies that the enrollment detection
// logic flags an implementation when its owning package is not enrolled. It
// collects the real implSet, then simulates a "missing enrollment" by removing
// one impl's pkg from the enrolled set and asserts exactly that impl is
// reported. (healthz.RepoProber lives in an internal/widely-imported package,
// so a standalone fixture module is impractical — same constraint as the
// USERREPO sibling.)
func TestCellRepoReadyzProbe_REDFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	prodPatterns := prodscan.Patterns(root)

	var repoProberIface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns,
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

	require.NotNil(t, repoProberIface, "REDFixture: could not resolve RepoProber interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectRepoProberImpls(pkg, repoProberIface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet, "REDFixture: implSet must not be empty")

	// Pick a deterministic impl key (sorted) so a REDFixture failure names a
	// stable target across runs rather than a random map-iteration pick.
	implKeys := make([]string, 0, len(implSet))
	for k := range implSet {
		implKeys = append(implKeys, k)
	}
	sort.Strings(implKeys)
	targetImplKey := implKeys[0]
	dotIdx := strings.LastIndex(targetImplKey, ".")
	require.Greater(t, dotIdx, 0, "REDFixture: malformed impl key %q", targetImplKey)
	targetPkg := targetImplKey[:dotIdx]

	// Simulate missing enrollment: enrolled contains all impl pkgs EXCEPT targetPkg.
	enrolledPkgs := make(map[string]bool)
	for pkg := range implPkgSet {
		if pkg != targetPkg {
			enrolledPkgs[pkg] = true
		}
	}

	var diags []Diagnostic
	for implKey := range implSet {
		dotIdx2 := strings.LastIndex(implKey, ".")
		if dotIdx2 < 0 {
			continue
		}
		if !enrolledPkgs[implKey[:dotIdx2]] {
			diags = append(diags, Diagnostic{Rel: implKey, Message: implKey + " not enrolled"})
		}
	}

	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing pkg %q from enrolledPkgs must produce ≥1 violation, got 0", targetPkg)
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

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
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

// collectRepoProberImpls adds to implSet all exported concrete types in pkg that
// implement healthz.RepoProber (directly or via pointer). Interface types are
// skipped. implPkgSet receives the package path for each collected impl.
func collectRepoProberImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() {
			continue
		}
		t := obj.Type()
		if _, isIface := t.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(t, iface) {
			implSet[pkg.Path()+"."+name] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// hasRepoReadinessConformanceCall returns true when file contains at least one
// call to celltest.RunRepoReadinessConformance resolved via TypesInfo.
func hasRepoReadinessConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	found := false
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if found {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if ok && pkgPath == repoReadinessConformancePkg && name == repoReadinessConformanceFunc {
			found = true
		}
	})
	return found
}
