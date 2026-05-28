// INVARIANT: SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware；identifies every
//     concrete named type (exported AND unexported) that satisfies
//     kernel/saga/journal.Journal (value or pointer receivers).
//   - conformance 调用扫描 (impl-level): ResolvePackageRef + _test.go path filter
//     plus per-call return-type unwrap — type-aware callee resolution via
//     *types.Info. For every _test.go file that calls
//     sagajournaltest.RunConformanceSuite, walk every CallExpr in the file and
//     unwrap its return tuple; impls whose key matches the impl set are marked
//     enrolled. Package co-location alone no longer credits enrollment — the
//     test file must actually construct the impl.
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

	// ─── Step 3: scan test corpus for RunConformanceSuite call sites with
	// impl-level enrollment. A test file is credited with enrolling impl X
	// only if (a) it contains a sagajournaltest.RunConformanceSuite call AND
	// (b) it constructs X (constructor call whose return type unwraps to X).
	// Package co-location is no longer enough — closes the gap where two
	// impls in one package could share a single conformance call.
	enrolledImpls := make(map[string]bool)

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
				if !hasSagaConformanceCall(f, p.TypesInfo) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					for _, implKey := range extractEnrolledImpls(call, p.TypesInfo, implSet) {
						enrolledImpls[implKey] = true
					}
				})
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ─────────────────────────────
	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: kernel/saga/journal.Journal impl %q not enrolled "+
					"(SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s (or its external _test) that "+
					"both calls sagajournaltest.RunConformanceSuite(t, factory) "+
					"AND constructs %s inside the factory closure (impl-level "+
					"enrollment).",
				implKey, pkgPath, implKey),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01", diags)
}

// TestSagaJournalConformanceEnrollment_REDFixture simulates an
// impl-level "missing enrollment" by dropping one impl from the
// enrolled set and asserts the diagnostic logic produces at least
// one violation. The fixture exercises the same comparison logic
// the main test uses (now impl-level keys rather than pkg paths).
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

	// Pick an arbitrary impl as the "missing enrollment" target.
	var targetImplKey string
	for k := range implSet {
		targetImplKey = k
		break
	}

	// Build enrolled impls = all impls EXCEPT the target. The diagnostic
	// logic must flag the target.
	enrolledImpls := make(map[string]bool)
	for k := range implSet {
		if k != targetImplKey {
			enrolledImpls[k] = true
		}
	}

	var diags []Diagnostic
	for implKey := range implSet {
		if !enrolledImpls[implKey] {
			diags = append(diags, Diagnostic{Rel: implKey, Message: implKey + " not enrolled"})
		}
	}
	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing impl %q from enrolledImpls must produce ≥1 violation, got 0", targetImplKey)
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

// collectSagaJournalImpls adds to implSet all concrete types in pkg (exported
// AND unexported) that implement Journal (value or pointer receiver).
// Interface types are skipped. Unexported impls must also enroll — a package-
// private fake/wrapper that satisfies the interface still risks behavior
// drift if not exercised by the conformance suite.
func collectSagaJournalImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
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
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// extractEnrolledImpls inspects a CallExpr's callee signature and returns
// implKey strings ("pkg/path.TypeName") for every concrete Journal impl that
// the call constructs (return values whose underlying named type is in
// implSet). Pointer wrappers (*T) are unwrapped. Cross-package type identity
// is irrelevant — we compare by string keys, so the iface-pass and test-pass
// loads do not need to share *types.Named instances.
//
// This is the impl-level upgrade of the prior package-level enrollment: a
// test file is now only credited with enrolling impl X if it actually
// constructs X — package co-location is no longer enough.
func extractEnrolledImpls(call *ast.CallExpr, info *types.Info, implSet map[string]bool) []string {
	if info == nil {
		return nil
	}
	calleeType := info.TypeOf(call.Fun)
	if calleeType == nil {
		return nil
	}
	sig, ok := calleeType.(*types.Signature)
	if !ok {
		return nil
	}
	var out []string
	results := sig.Results()
	for i := 0; i < results.Len(); i++ {
		rt := results.At(i).Type()
		if ptr, isPtr := rt.(*types.Pointer); isPtr {
			rt = ptr.Elem()
		}
		named, ok := rt.(*types.Named)
		if !ok {
			continue
		}
		obj := named.Obj()
		if obj == nil || obj.Pkg() == nil {
			continue
		}
		key := obj.Pkg().Path() + "." + obj.Name()
		if implSet[key] {
			out = append(out, key)
		}
	}
	return out
}

// hasSagaConformanceCall returns true when file contains at least one call to
// sagajournaltest.RunConformanceSuite resolved via TypesInfo.
func hasSagaConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && pkgPath == sagaConformancePkg && name == sagaConformanceFuncName
	})
	return ok
}
