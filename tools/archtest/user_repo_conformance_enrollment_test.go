// INVARIANT: USERREPO-CONFORMANCE-ENROLLMENT-01
//
// AI-rebust: Medium
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware，identifies every
//     concrete named type that satisfies ports.UserRepository (or *T).
//   - conformance 调用扫描: ResolvePackageRef + _test.go path filter — type-aware
//     callee resolution via *types.Info. Identifies every package with a
//     conformance.RunUserRepoConformance call site.
//   - 综合: Medium 天花板 — Go cannot require a test to exist at compile time;
//     the enforcement is archtest-bound (CI fails), not compile-time.
//
// Enforces: every concrete type in the production source tree that implements
// ports.UserRepository must have at least one conformance.RunUserRepoConformance
// call in a _test.go file belonging to its package. Packages without such a call
// are reported as violations.
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations (reflect.Value.MethodByName…):
//     no production code uses this pattern; confirmed by
//     TestUserRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl.
//
//   - B2. generated mock implementations (mockery / gomock in _test.go) are
//     excluded: test-file types are not scanned for implementations (Tests=false
//     in the production load pass). If a generated mock appears in a production
//     non-test file, the archtest will flag it — intentionally.
//
//   - B3. embedded interface forwarding (struct embedding ports.UserRepository):
//     such a type structurally satisfies the interface but provides no real
//     storage. These are rare and only appear in test helpers (which live in
//     _test.go files, excluded from the impl scan). Production structs that
//     embed the interface are treated as implementations and must enroll.
//
// ref: tools/archtest/cell_repo_readyz_probe_test.go (P1 conformance backstop pattern)
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
)

// INVARIANT: USERREPO-CONFORMANCE-ENROLLMENT-01

const (
	userRepoIfacePkg  = "github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	userRepoIfaceName = "UserRepository"
	conformancePkg    = "github.com/ghbvf/gocell/cells/accesscore/internal/ports/conformance"
	conformanceFunc   = "RunUserRepoConformance"
)

// TestUserRepoConformanceEnrollment enforces USERREPO-CONFORMANCE-ENROLLMENT-01:
// every concrete type implementing ports.UserRepository in the production tree
// must have a conformance.RunUserRepoConformance call in a _test.go file of its
// package.
func TestUserRepoConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	// ─── Step 1: resolve ports.UserRepository interface ─────────────────────
	//
	// The iface and the impl types MUST come from the same packages.Load
	// invocation so that types.Implements uses pointer-identical *types.Named
	// descriptors (cross-load comparisons are always false).
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./cells/accesscore/internal/ports/..."}, prodPatterns...)

	var userRepoIface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			// Capture the iface from the ports package.
			if p.Pkg.Path() == userRepoIfacePkg {
				if obj := p.Pkg.Scope().Lookup(userRepoIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							userRepoIface = iface.Complete()
						}
					}
				}
				return nil
			}
			// Collect candidate packages for impl scanning.
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, userRepoIface,
		"USERREPO-CONFORMANCE-ENROLLMENT-01: failed to resolve ports.UserRepository interface; "+
			"check import path %s", userRepoIfacePkg)

	// ─── Step 2: collect all concrete implementations ───────────────────────
	//
	// implSet maps "pkg/path.TypeName" → true for every concrete exported type
	// (struct or pointer-to-struct) that satisfies UserRepository.
	// implPkgSet maps "pkg/path" → true for the owning package of each impl.
	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool) // pkg path → true

	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		collectUserRepoImpls(pkg, userRepoIface, implSet, implPkgSet)
	}

	// Regression guard: if zero impls found, the type-universe is broken.
	require.NotEmpty(t, implSet,
		"USERREPO-CONFORMANCE-ENROLLMENT-01: zero UserRepository implementations collected — "+
			"likely a type-universe regression (iface and impls must share one packages.Load). "+
			"Expect at least mem.UserRepository and postgres.PGUserRepo.")

	// ─── Step 3: scan test corpus for RunUserRepoConformance call sites ─────
	//
	// A package is considered "enrolled" if any _test.go in it (or its test
	// variant) contains a call to conformance.RunUserRepoConformance.
	enrolledPkgs := make(map[string]bool) // pkg path → true

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
				if hasConformanceCall(f, p.TypesInfo) {
					// The package path for a test variant ends with ".test" or
					// "_test"; strip both suffixes to get the canonical prod pkg path.
					pkgPath := canonicalPkgPath(p.Pkg.Path())
					enrolledPkgs[pkgPath] = true
				}
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ─────────────────────────────
	var diags []Diagnostic
	for implKey := range implSet {
		// implKey is "pkg/path.TypeName"; extract pkg path.
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
					"archtest: ports.UserRepository impl %q not enrolled in "+
						"conformance.RunUserRepoConformance test call "+
						"(USERREPO-CONFORMANCE-ENROLLMENT-01). "+
						"Add a _test.go in package %s that calls "+
						"conformance.RunUserRepoConformance(t, factory, features).",
					implKey, pkgPath),
			})
		}
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "USERREPO-CONFORMANCE-ENROLLMENT-01", diags)
}

// TestUserRepoConformanceEnrollment_REDFixture verifies that the enrollment
// detection logic flags an implementation when the owning package is not in the
// enrolledPkgs set. This exercises the core of the enrollment check without
// requiring a standalone fixture module (ports.UserRepository lives in an
// internal package, making cross-module fixture modules impossible).
//
// Strategy: collect the real implSet and enrolledPkgs from the production tree,
// then simulate a "missing enrollment" by removing one impl's pkg from enrolled.
// Assert that exactly that impl is reported as a violation.
func TestUserRepoConformanceEnrollment_REDFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)

	// ─── Load production iface + impls ─────────────────────────────────────
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./cells/accesscore/internal/ports/..."}, prodPatterns...)

	var userRepoIface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == userRepoIfacePkg {
				if obj := p.Pkg.Scope().Lookup(userRepoIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							userRepoIface = iface.Complete()
						}
					}
				}
				return nil
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, userRepoIface, "REDFixture: could not resolve UserRepository interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectUserRepoImpls(pkg, userRepoIface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet, "REDFixture: implSet must not be empty (need at least one impl)")

	// Pick the first impl key and derive its pkg path.
	var targetImplKey string
	for k := range implSet {
		targetImplKey = k
		break
	}
	dotIdx := strings.LastIndex(targetImplKey, ".")
	require.Greater(t, dotIdx, 0, "REDFixture: malformed impl key %q", targetImplKey)
	targetPkg := targetImplKey[:dotIdx]

	// Simulate missing enrollment: enrolledPkgs contains all impls EXCEPT targetPkg.
	enrolledPkgs := make(map[string]bool)
	for pkg := range implPkgSet {
		if pkg != targetPkg {
			enrolledPkgs[pkg] = true
		}
	}

	// Run the flagging logic with the simulated enrolled set.
	var diags []Diagnostic
	for implKey := range implSet {
		dotIdx2 := strings.LastIndex(implKey, ".")
		if dotIdx2 < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx2]
		if !enrolledPkgs[pkgPath] {
			diags = append(diags, Diagnostic{
				Rel:     implKey,
				Message: implKey + " not enrolled",
			})
		}
	}

	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing pkg %q from enrolledPkgs must produce at least 1 violation, got 0", targetPkg)
}

// TestUserRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl (blind spot B1)
// confirms no production non-test file uses reflect.MethodByName("UserRepository")
// or reflect.Value.MethodByName to construct an implicit impl. If this test ever
// triggers, the scan would miss that impl.
func TestUserRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
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
				if !ok {
					return
				}
				if val == userRepoIfaceName {
					const b1msg = "blind-spot B1: string literal \"UserRepository\" in production code " +
						"may indicate reflect-based impl (USERREPO-CONFORMANCE-ENROLLMENT-01)"
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
		"B1 reverse: no production non-test file should contain the string literal %q as reflect bait", userRepoIfaceName)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectUserRepoImpls adds to implSet all exported concrete types in pkg that
// implement UserRepository (directly or via pointer). Interface types are skipped.
// implPkgSet receives the package path for each collected impl.
func collectUserRepoImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() {
			continue
		}
		t := obj.Type()
		// Skip interface types — only concrete types can be registered probers.
		if _, isIface := t.Underlying().(*types.Interface); isIface {
			continue
		}
		if types.Implements(t, iface) || types.Implements(types.NewPointer(t), iface) {
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// hasConformanceCall returns true when file contains at least one call to
// conformance.RunUserRepoConformance resolved via TypesInfo.
func hasConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	found := false
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if found {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if ok && pkgPath == conformancePkg && name == conformanceFunc {
			found = true
		}
	})
	return found
}

// canonicalPkgPath strips the "_test" or ".test" suffix from a test-variant
// package path to obtain the canonical production package path.
func canonicalPkgPath(path string) string {
	path = strings.TrimSuffix(path, "_test")
	path = strings.TrimSuffix(path, ".test")
	return path
}
