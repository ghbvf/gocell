// INVARIANT: USERREPO-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
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
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// INVARIANT: USERREPO-CONFORMANCE-ENROLLMENT-01

// TestUserRepoConformanceEnrollment enforces USERREPO-CONFORMANCE-ENROLLMENT-01:
// every concrete type implementing ports.UserRepository in the production tree
// must have a conformance.RunUserRepoConformance call in a _test.go file of its
// package.
func TestUserRepoConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	Report(t, ruleUserRepoConformanceEnrollment01,
		CheckUserRepoConformanceEnrollment01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
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
	// corecells is a separate go module, so prodscan.Patterns's root-relative
	// ./... does not cross the module boundary into it — the explicit
	// ./corecells/... is required to load the iface + impls in one packages.Load.
	ifacePatterns := append([]string{"./corecells/..."}, prodPatterns...)

	var userRepoIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns),
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

	// Run the real flagging logic with the simulated enrolled set.
	diags := flagUnenrolledImpls(implSet, enrolledPkgs)

	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing pkg %q from enrolledPkgs must produce at least 1 violation, got 0", targetPkg)

	// Extra: confirm at least one diagnostic targets the removed pkg.
	var foundTarget bool
	for _, d := range diags {
		if strings.HasPrefix(d.Rel, targetPkg) {
			foundTarget = true
			break
		}
	}
	assert.True(t, foundTarget,
		"REDFixture: expected at least one diagnostic with Rel prefix %q, got %v", targetPkg, diags)
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

	diags := Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Skip _test.go (a reflect-based impl would live in a production
			// non-test file, not a test) and the archtest tooling package itself.
			// tools/archtest rule files legitimately carry "UserRepository" as a
			// string literal to identify the target type (userRepoIfaceName /
			// userRepoType / the refreshGuardedMethods table) — that is the rule's
			// own type-name spec, not a reflect-based implementation. Before #1633
			// these literals lived in archtest _test.go (already excluded here); the
			// rule-logic MOVE to non-test .go relocated them into production scope,
			// so the exclusion is restated explicitly. A genuine reflect-based
			// ports.UserRepository impl can only occur in cells/ runtime/ adapters/,
			// all still scanned by ModuleScope below.
			if strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "tools/archtest/") {
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
