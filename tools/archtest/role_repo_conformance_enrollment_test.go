//go:build archtest

// INVARIANT: ROLEREPO-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware，identifies every
//     concrete named type that satisfies ports.RoleRepository (or *T).
//   - conformance 调用扫描: ResolvePackageRef + _test.go path filter — type-aware
//     callee resolution via *types.Info. Identifies every package with a
//     conformance.RunRoleRepoConformance call site.
//   - 综合: Medium 天花板 — Go cannot require a test to exist at compile time;
//     the enforcement is archtest-bound (CI fails), not compile-time.
//
// Enforces: every concrete type in the production source tree that implements
// ports.RoleRepository must have at least one conformance.RunRoleRepoConformance
// call in a _test.go file belonging to its package. Packages without such a call
// are reported as violations.
//
// # Why this exists (#1709)
//
// RunRoleRepoConformance gained the shared RowScope×subject obligation matrix
// (GetByUserID / ListByUserID across Self/Device/Tenant/All) when accesscore wired
// RowVisibility into the Role read path. The matrix is the defense-in-depth that
// proves a RoleRepository impl HONORS the obligation (does not ignore vis). That
// proof is only as strong as its coverage: an impl that never calls the suite is
// never asserted against the matrix. This rule is the RoleRepository sibling of
// USERREPO-CONFORMANCE-ENROLLMENT-01, closing the same "future backend silently
// skips the vis cases" gap for the second repo that now carries a vis param.
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations (reflect.Value.MethodByName…):
//     no production code uses this pattern; confirmed by
//     TestRoleRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl.
//
//   - B2. generated mock implementations (mockery / gomock in _test.go) are
//     excluded: test-file types are not scanned for implementations (Tests=false
//     in the production load pass). If a generated mock appears in a production
//     non-test file, the archtest will flag it — intentionally.
//
//   - B3. embedded interface forwarding (struct embedding ports.RoleRepository):
//     such a type structurally satisfies the interface but provides no real
//     storage. These are rare and only appear in test helpers (which live in
//     _test.go files, excluded from the impl scan). Production structs that
//     embed the interface are treated as implementations and must enroll.
//
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (sibling pattern)
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

// INVARIANT: ROLEREPO-CONFORMANCE-ENROLLMENT-01

// TestRoleRepoConformanceEnrollment enforces ROLEREPO-CONFORMANCE-ENROLLMENT-01:
// every concrete type implementing ports.RoleRepository in the production tree
// must have a conformance.RunRoleRepoConformance call in a _test.go file of its
// package.
func TestRoleRepoConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	Report(t, ruleRoleRepoConformanceEnrollment01,
		CheckRoleRepoConformanceEnrollment01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestRoleRepoConformanceEnrollment_REDFixture verifies that the enrollment
// detection logic flags an implementation when the owning package is not in the
// enrolledPkgs set. This exercises the core of the enrollment check without
// requiring a standalone fixture module (ports.RoleRepository lives in an
// internal package, making cross-module fixture modules impossible).
//
// Strategy: collect the real implSet and enrolledPkgs from the production tree,
// then simulate a "missing enrollment" by removing one impl's pkg from enrolled.
// Assert that exactly that impl is reported as a violation.
func TestRoleRepoConformanceEnrollment_REDFixture(t *testing.T) {
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

	var roleRepoIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == roleRepoIfacePkg {
				if obj := p.Pkg.Scope().Lookup(roleRepoIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							roleRepoIface = iface.Complete()
						}
					}
				}
				return nil
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, roleRepoIface, "REDFixture: could not resolve RoleRepository interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectRoleRepoImpls(pkg, roleRepoIface, implSet, implPkgSet)
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
	diags := flagUnenrolledRoleImpls(implSet, enrolledPkgs)

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

// TestRoleRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl (blind spot B1)
// confirms no production non-test file uses reflect.MethodByName("RoleRepository")
// or reflect.Value.MethodByName to construct an implicit impl. If this test ever
// triggers, the scan would miss that impl.
func TestRoleRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
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
			// non-test file, not a test) and the archtest tooling package itself
			// (rule files carry "RoleRepository" as a type-name spec literal, not a
			// reflect-based implementation — same exclusion rationale as the
			// UserRepository sibling). A genuine reflect-based ports.RoleRepository
			// impl can only occur in cells/ runtime/ adapters/, all still scanned by
			// ModuleScope below.
			if strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "tools/archtest/") {
				continue
			}
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok {
					return
				}
				if val == roleRepoIfaceName {
					const b1msg = "blind-spot B1: string literal \"RoleRepository\" in production code " +
						"may indicate reflect-based impl (ROLEREPO-CONFORMANCE-ENROLLMENT-01)"
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
		"B1 reverse: no production non-test file should contain the string literal %q as reflect bait", roleRepoIfaceName)
}
