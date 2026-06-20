//go:build archtest

// INVARIANT: POLICYREPO-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware，identifies every
//     concrete named type that satisfies ports.PolicyRepository (or *T).
//   - conformance 调用扫描: ResolvePackageRef + _test.go path filter — type-aware
//     callee resolution via *types.Info. Identifies every package with a
//     conformance.RunPolicyRepoConformance call site.
//   - 综合: Medium 天花板 — Go cannot require a test to exist at compile time;
//     the enforcement is archtest-bound (CI fails), not compile-time.
//
// Enforces: every concrete type in the production source tree that implements
// ports.PolicyRepository must have at least one conformance.RunPolicyRepoConformance
// call in a _test.go file belonging to its package. Packages without such a call
// are reported as violations.
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations (reflect.Value.MethodByName…):
//     no production code uses this pattern; confirmed by
//     TestPolicyRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl.
//
//   - B2. generated mock implementations (mockery / gomock in _test.go) are
//     excluded: the single Tests=true load collects impls through a non-_test.go
//     declaration filter (productionImplCandidates / isTestDeclaredObj), so
//     _test.go-declared types are dropped. If a generated mock appears in a
//     production non-test file, the archtest will flag it — intentionally.
//
//   - B3. embedded interface forwarding (struct embedding ports.PolicyRepository):
//     such a type structurally satisfies the interface but provides no real
//     storage. These are rare and only appear in test helpers (which live in
//     _test.go files, excluded from the impl scan). Production structs that
//     embed the interface are treated as implementations and must enroll.
//
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (sibling pattern)
package archtest

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// INVARIANT: POLICYREPO-CONFORMANCE-ENROLLMENT-01

// TestPolicyRepoConformanceEnrollment enforces POLICYREPO-CONFORMANCE-ENROLLMENT-01:
// every concrete type implementing ports.PolicyRepository in the production tree
// must have a conformance.RunPolicyRepoConformance call in a _test.go file of its
// package.
func TestPolicyRepoConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	Report(t, rulePolicyRepoConformanceEnrollment01,
		checkRepoConformanceEnrollment(t, policyRepoConformanceSpec(), ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestPolicyRepoConformanceEnrollment_REDFixture proves the enrollment detector is
// non-vacuous for PolicyRepository: removing one impl's package from the enrolled
// set must flag exactly that package. The shared body lives in
// runRepoEnrollmentREDFixture (repo_conformance_enrollment_helpers_test.go).
func TestPolicyRepoConformanceEnrollment_REDFixture(t *testing.T) {
	t.Parallel()
	runRepoEnrollmentREDFixture(t, repoPortsPkg, policyRepoIfaceName)
}

// TestPolicyRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl (blind spot B1)
// confirms no production non-test file uses reflect.MethodByName("PolicyRepository")
// or reflect.Value.MethodByName to construct an implicit impl. If this test ever
// triggers, the scan would miss that impl.
func TestPolicyRepoConformanceEnrollment_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
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
			// Skip _test.go and the archtest tooling package itself.
			// tools/archtest rule files legitimately carry "PolicyRepository" as a
			// string literal to identify the target type (policyRepoIfaceName) —
			// that is the rule's own type-name spec, not a reflect-based
			// implementation. A genuine reflect-based ports.PolicyRepository impl
			// can only occur in cells/ runtime/ adapters/, all still scanned.
			if strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "tools/archtest/") {
				continue
			}
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok {
					return
				}
				if val == policyRepoIfaceName {
					const b1msg = "blind-spot B1: string literal \"PolicyRepository\" in production code " +
						"may indicate reflect-based impl (POLICYREPO-CONFORMANCE-ENROLLMENT-01)"
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
		"B1 reverse: no production non-test file should contain the string literal %q as reflect bait", policyRepoIfaceName)
}
