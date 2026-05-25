// INVARIANT: SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware；identifies every
//     concrete named type that satisfies kernel/saga/journal.Journal (value or
//     pointer receivers).
//   - conformance 调用扫描: ResolvePackageRef + _test.go path filter — type-aware
//     callee resolution via *types.Info. Identifies every package with at least
//     one sagajournaltest.RunConformanceSuite call site.
//   - 综合 Medium 天花板: Go cannot require a _test.go file to exist for a type at
//     compile time. The enforcement is archtest-bound (CI fails), not
//     compile-time. The Hard upgrade path is a codegen funnel + golden that
//     enumerates Journal impls from a single source and diff-locks the registry;
//     deferred as cross-PR governance work — tracked in gh issue #1003. The
//     mirror precedent is USERREPO-CONFORMANCE-ENROLLMENT-01.
//
// Enforces: every concrete type in the production source tree that implements
// kernel/saga/journal.Journal must have at least one
// sagajournaltest.RunConformanceSuite call in a _test.go file belonging to its
// package. Packages without such a call are reported as violations.
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations: no production saga code uses
//     this pattern; confirmed by
//     TestSagaJournalConformanceEnrollment_ReverseBlindSpot_NoReflectImpl.
//
//   - B2. generated mock implementations (mockery / gomock in _test.go) are
//     excluded: test-file types are not scanned for implementations (Tests=false
//     in the production load pass). If a generated mock appears in a production
//     non-test file, the archtest will flag it — intentionally.
//
//   - B3. embedded interface forwarding (struct embedding journal.Journal):
//     such a type structurally satisfies the interface but provides no real
//     storage. These are rare and only appear in test helpers (which live in
//     _test.go files, excluded from the impl scan). Production structs that
//     embed the interface are treated as implementations and must enroll.
//
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (USERREPO-CONFORMANCE-ENROLLMENT-01)
// ref: docs/plans/202605230231-046-saga-l3-workflow-implementation-plan.md §PR-04
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
	sagaJournalIfacePkg     = "github.com/ghbvf/gocell/kernel/saga/journal"
	sagaJournalIfaceName    = "Journal"
	sagaConformancePkg      = "github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
	sagaConformanceFuncName = "RunConformanceSuite"
)

// TestSagaJournalConformanceEnrollment enforces SAGA-JOURNAL-CONFORMANCE-
// ENROLLMENT-01: every concrete type implementing kernel/saga/journal.Journal
// in the production tree must have a sagajournaltest.RunConformanceSuite call
// in a _test.go file of its package (or the corresponding external _test
// variant).
func TestSagaJournalConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	// ─── Step 1: resolve journal.Journal interface ──────────────────────────
	//
	// The iface and the impl types MUST come from the same packages.Load
	// invocation so that types.Implements uses pointer-identical *types.Named
	// descriptors (cross-load comparisons are always false).
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/saga/journal/..."}, prodPatterns...)

	var iface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == sagaJournalIfacePkg {
				if obj := p.Pkg.Scope().Lookup(sagaJournalIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							iface = i.Complete()
						}
					}
				}
				// The iface package's MemJournal is also an impl; keep this pkg
				// in implPkgs so its concrete types are scanned.
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, iface,
		"SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01: failed to resolve journal.Journal interface; "+
			"check import path %s", sagaJournalIfacePkg)

	// ─── Step 2: collect all concrete implementations ───────────────────────
	implSet := make(map[string]bool)    // "pkg/path.TypeName" → true
	implPkgSet := make(map[string]bool) // pkg path → true
	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		collectSagaJournalImpls(pkg, iface, implSet, implPkgSet)
	}

	require.NotEmpty(t, implSet,
		"SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01: zero Journal implementations collected — "+
			"likely a type-universe regression (iface and impls must share one packages.Load). "+
			"Expect at least journal.MemJournal and saga.PGJournal.")

	// ─── Step 3: scan test corpus for RunConformanceSuite call sites ────────
	enrolledPkgs := make(map[string]bool)

	testPatterns := prodscan.Patterns(root)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, testPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if hasSagaConformanceCall(f, p.TypesInfo) {
					enrolledPkgs[canonicalPkgPath(p.Pkg.Path())] = true
				}
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
		if !enrolledPkgs[pkgPath] {
			diags = append(diags, Diagnostic{
				Rel:  implKey,
				Line: 0,
				Message: fmt.Sprintf(
					"archtest: kernel/saga/journal.Journal impl %q not enrolled in "+
						"sagajournaltest.RunConformanceSuite test call "+
						"(SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01). "+
						"Add a _test.go in package %s (or its external _test) that calls "+
						"sagajournaltest.RunConformanceSuite(t, factory).",
					implKey, pkgPath),
			})
		}
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01", diags)
}

// TestSagaJournalConformanceEnrollment_REDFixture simulates a "missing
// enrollment" by removing one impl's pkg from the enrolled set and asserts
// the diagnostic logic produces at least one violation. Mirrors the
// USERREPO REDFixture pattern.
func TestSagaJournalConformanceEnrollment_REDFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/saga/journal/..."}, prodPatterns...)

	var iface *types.Interface
	var implPkgs []*types.Package
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == sagaJournalIfacePkg {
				if obj := p.Pkg.Scope().Lookup(sagaJournalIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							iface = i.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, iface, "REDFixture: could not resolve Journal interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectSagaJournalImpls(pkg, iface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet, "REDFixture: implSet must not be empty")

	var targetImplKey string
	for k := range implSet {
		targetImplKey = k
		break
	}
	dotIdx := strings.LastIndex(targetImplKey, ".")
	require.Greater(t, dotIdx, 0, "REDFixture: malformed impl key %q", targetImplKey)
	targetPkg := targetImplKey[:dotIdx]

	enrolledPkgs := make(map[string]bool)
	for pkg := range implPkgSet {
		if pkg != targetPkg {
			enrolledPkgs[pkg] = true
		}
	}

	var diags []Diagnostic
	for implKey := range implSet {
		idx := strings.LastIndex(implKey, ".")
		if idx < 0 {
			continue
		}
		if !enrolledPkgs[implKey[:idx]] {
			diags = append(diags, Diagnostic{Rel: implKey, Message: implKey + " not enrolled"})
		}
	}
	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing pkg %q from enrolledPkgs must produce ≥1 violation, got 0", targetPkg)
}

// TestSagaJournalConformanceEnrollment_ReverseBlindSpot_NoReflectImpl (B1)
// confirms no production non-test file uses the string literal "Journal" as
// a reflect target that could construct an implicit impl bypassing types.
func TestSagaJournalConformanceEnrollment_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
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
			// Skip test files and the saga packages that legitimately mention
			// "Journal" in docstrings / godoc / interface names.
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if strings.HasPrefix(rel, "kernel/saga/journal/") ||
				strings.HasPrefix(rel, "kernel/saga/sagajournaltest/") ||
				strings.HasPrefix(rel, "adapters/postgres/saga/") ||
				strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok {
					return
				}
				if val == sagaJournalIfaceName {
					const b1msg = "blind-spot B1: string literal \"Journal\" in production code " +
						"outside saga packages may indicate reflect-based impl " +
						"(SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01)"
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
	assert.Empty(t, diags,
		"B1 reverse: no production non-test file outside saga packages should contain string literal %q", sagaJournalIfaceName)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectSagaJournalImpls adds to implSet all exported concrete types in pkg
// that implement Journal (value or pointer receiver). Interface types are
// skipped.
func collectSagaJournalImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
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
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// hasSagaConformanceCall returns true when file contains at least one call to
// sagajournaltest.RunConformanceSuite resolved via TypesInfo.
func hasSagaConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	found := false
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if found {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if ok && pkgPath == sagaConformancePkg && name == sagaConformanceFuncName {
			found = true
		}
	})
	return found
}
